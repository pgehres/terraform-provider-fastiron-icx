// Package acctest is the acceptance test harness: it runs the provider's
// resources against a live lab switch through terraform-plugin-testing.
//
// Every test here is gated on TF_ACC and the FASTIRON_* connection variables
// and skips cleanly without them, so `go test ./...` in CI never needs a
// switch. With them set, each test
//
//   - snapshots the running configuration before touching anything,
//   - pre-flights the scratch resources it is allowed to use (an unused
//     port, unused VLAN IDs, a scratch user) and refuses to run if any of
//     them is in use on the device,
//   - drives the resource through create, update and destroy with
//     terraform-plugin-testing, which fails a step whose post-apply plan is
//     not empty (the zero-diff rule, enforced after every apply),
//   - verifies each step on the device itself by re-reading the running
//     configuration over its own SSH session, and
//   - after destroy, checks that the running configuration is identical to
//     the pre-test snapshot.
//
// The rules the harness enforces, matching CLAUDE.md: no port other than
// TF_ACC_SCRATCH_PORT is ever configured; VLAN 1 and any VLAN that exists on
// the device (so the management VLAN among them) are never touched; the
// management IP address and default gateway are never changed; the login
// user is never managed. See the package README and CLAUDE.md for how to run
// the suite and the pre-flight checklist.
package acctest

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/provider"
	icxresource "github.com/pgehres/terraform-provider-fastiron-icx/internal/resource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// Environment variables read by the harness. The FASTIRON_* connection
// variables are the provider's own; the provider block rendered into every
// test configuration leaves them to the environment so no secret is written
// into the test's working directory.
const (
	envTFAcc          = "TF_ACC"
	envHost           = "FASTIRON_HOST"
	envPort           = "FASTIRON_PORT"
	envUsername       = "FASTIRON_USERNAME"
	envPassword       = "FASTIRON_PASSWORD"
	envEnablePassword = "FASTIRON_ENABLE_PASSWORD"
	envHostKey        = "FASTIRON_HOST_KEY"
	// envLegacySSH is a boolean; it maps to the provider's
	// legacy_ssh_algorithms attribute, which has no environment variable of
	// its own.
	envLegacySSH = "FASTIRON_LEGACY_SSH"
	// envScratchPort names the one port (unit/slot/port) the tests may
	// configure. Required only by tests that configure a port.
	envScratchPort = "TF_ACC_SCRATCH_PORT"
	// envScratchVLAN and envScratchVLAN2 are VLAN IDs that must not exist on
	// the device. The second is needed only by the trunk-with-untagged step
	// of the interface test.
	envScratchVLAN  = "TF_ACC_SCRATCH_VLAN"
	envScratchVLAN2 = "TF_ACC_SCRATCH_VLAN_2"
	// envAllowTrunkReconcile is a boolean. icx_vlan.Create tags a new VLAN
	// onto every port it recognises as a "tag all VLANs" trunk (CLAUDE.md
	// gotcha #8), which would configure ports other than the scratch port.
	// Tests that create a VLAN therefore skip on a device with such trunks
	// unless this is true.
	envAllowTrunkReconcile = "TF_ACC_ALLOW_TRUNK_RECONCILE"

	defaultScratchVLAN  = 3999
	defaultScratchVLAN2 = 3998

	// scratchUser is the local account the icx_user test creates and
	// deletes. It must never be the login user.
	scratchUser = "tfacc-test"
)

var rePort = regexp.MustCompile(`^\d+/\d+/\d+$`)

// Env is the live-switch test environment read from the process environment.
type Env struct {
	Host           string
	Port           int
	Username       string
	Password       string
	EnablePassword string
	HostKey        string
	LegacySSH      bool

	// ScratchPort is empty when TF_ACC_SCRATCH_PORT is unset; tests that
	// need a port call requireScratchPort.
	ScratchPort         string
	ScratchVLAN         int
	ScratchVLAN2        int
	AllowTrunkReconcile bool
}

// Setup reads the environment and skips the test unless TF_ACC and the
// connection variables are set. It validates the scratch settings so a typo
// fails here, before anything connects.
func Setup(t *testing.T) *Env {
	t.Helper()
	if os.Getenv(envTFAcc) == "" {
		t.Skipf("acceptance test: set %s=1 and the FASTIRON_* connection variables to run against a live switch", envTFAcc)
	}
	e := &Env{
		Host:           os.Getenv(envHost),
		Username:       os.Getenv(envUsername),
		Password:       os.Getenv(envPassword),
		EnablePassword: os.Getenv(envEnablePassword),
		HostKey:        os.Getenv(envHostKey),
		ScratchPort:    os.Getenv(envScratchPort),
		ScratchVLAN:    defaultScratchVLAN,
		ScratchVLAN2:   defaultScratchVLAN2,
	}
	for name, value := range map[string]string{envHost: e.Host, envUsername: e.Username, envPassword: e.Password, envHostKey: e.HostKey} {
		if value == "" {
			t.Skipf("acceptance test: %s is not set (the harness never skips host key verification; "+
				"capture the key with `go run ./cmd/debug-ssh -print-host-key`)", name)
		}
	}
	e.Port = 22
	if v := os.Getenv(envPort); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 {
			t.Fatalf("%s=%q is not a port number", envPort, v)
		}
		e.Port = p
	}
	e.LegacySSH = envBool(t, envLegacySSH)
	e.AllowTrunkReconcile = envBool(t, envAllowTrunkReconcile)
	if e.ScratchPort != "" && !rePort.MatchString(e.ScratchPort) {
		t.Fatalf("%s=%q must be in unit/slot/port form, e.g. 1/1/11", envScratchPort, e.ScratchPort)
	}
	e.ScratchVLAN = envVLAN(t, envScratchVLAN, defaultScratchVLAN)
	e.ScratchVLAN2 = envVLAN(t, envScratchVLAN2, defaultScratchVLAN2)
	if e.ScratchVLAN == e.ScratchVLAN2 {
		t.Fatalf("%s and %s must differ (both are %d)", envScratchVLAN, envScratchVLAN2, e.ScratchVLAN)
	}
	if scratchUser == e.Username {
		t.Fatalf("the login user %q is the harness's scratch user name; log in as a different account", e.Username)
	}
	return e
}

func envBool(t *testing.T, name string) bool {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		t.Fatalf("%s=%q is not a boolean (use true or false)", name, v)
	}
	return b
}

// envVLAN reads a VLAN ID, rejecting VLAN 1 outright: the default VLAN is
// never a scratch VLAN.
func envVLAN(t *testing.T, name string, def int) int {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	id, err := strconv.Atoi(v)
	if err != nil || id < 2 || id > 4094 {
		t.Fatalf("%s=%q must be a VLAN ID between 2 and 4094", name, v)
	}
	return id
}

// requireScratchPort skips the test when no scratch port is configured.
func (e *Env) requireScratchPort(t *testing.T) {
	t.Helper()
	if e.ScratchPort == "" {
		t.Skipf("acceptance test: %s is not set; this test configures a port", envScratchPort)
	}
}

// ---------------------------------------------------------------------------
// Device access. Every harness operation opens its own short-lived SSH
// session: the provider's own session must never be shared, and a fresh
// session always detects the current prompt, which matters for the hostname
// test.

func (e *Env) connect() (*sshclient.Client, error) {
	return sshclient.NewClient(sshclient.Options{
		Host:                e.Host,
		Port:                e.Port,
		Username:            e.Username,
		Password:            e.Password,
		EnablePassword:      e.EnablePassword,
		HostKey:             e.HostKey,
		LegacySSHAlgorithms: e.LegacySSH,
	})
}

// Snapshot is the device's running configuration at one moment, raw and
// parsed.
type Snapshot struct {
	Raw    string
	Config *parser.RunningConfig
}

func (e *Env) snapshot() (*Snapshot, error) {
	c, err := e.connect()
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", e.Host, err)
	}
	defer func() { _ = c.Close() }()
	raw, err := c.GetRunningConfig()
	if err != nil {
		return nil, fmt.Errorf("show running-config: %w", err)
	}
	cfg, err := parser.ParseRunningConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parse running-config: %w", err)
	}
	return &Snapshot{Raw: raw, Config: cfg}, nil
}

// Snapshot reads the running configuration, failing the test on any error,
// and logs which device the test is about to use.
func (e *Env) Snapshot(t *testing.T) *Snapshot {
	t.Helper()
	s, err := e.snapshot()
	if err != nil {
		t.Fatalf("pre-test snapshot: %v", err)
	}
	t.Logf("device %s: firmware %q, image %s, hostname %q", e.Host, s.Config.Global.Version, s.Config.ImageType(), s.Config.Global.Hostname)
	return s
}

// deviceCheck returns a test check that re-reads the running configuration
// over the harness's own session and hands it to fn.
func (e *Env) deviceCheck(fn func(*parser.RunningConfig) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		s, err := e.snapshot()
		if err != nil {
			return err
		}
		return fn(s.Config)
	}
}

// checkDestroy returns a CheckDestroy that runs fn against the device and
// then requires the running configuration to match the pre-test snapshot
// line for line.
func (e *Env) checkDestroy(before *Snapshot, fn func(*parser.RunningConfig) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		s, err := e.snapshot()
		if err != nil {
			return err
		}
		if fn != nil {
			if err := fn(s.Config); err != nil {
				return err
			}
		}
		return compareConfigs(before.Raw, s.Raw)
	}
}

// normalizeConfig splits a running configuration into lines with trailing
// whitespace removed and blank lines dropped. Order is kept.
func normalizeConfig(raw string) []string {
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l == "" {
			continue
		}
		lines = append(lines, l)
	}
	return lines
}

// compareConfigs reports the lines that differ between two running
// configurations, as a multiset: a line is reported when it occurs a
// different number of times before and after. Reordering alone is not
// reported.
func compareConfigs(before, after string) error {
	b, a := normalizeConfig(before), normalizeConfig(after)
	counts := map[string]int{}
	for _, l := range b {
		counts[l]++
	}
	for _, l := range a {
		counts[l]--
	}
	var removed, added []string
	for l, n := range counts {
		switch {
		case n > 0:
			removed = append(removed, fmt.Sprintf("%s (x%d)", l, n))
		case n < 0:
			added = append(added, fmt.Sprintf("%s (x%d)", l, -n))
		}
	}
	if len(removed) == 0 && len(added) == 0 {
		return nil
	}
	sort.Strings(removed)
	sort.Strings(added)
	var sb strings.Builder
	sb.WriteString("running configuration differs from the pre-test snapshot after destroy")
	if len(removed) > 0 {
		sb.WriteString("\n  lines missing after the test:\n    ")
		sb.WriteString(strings.Join(removed, "\n    "))
	}
	if len(added) > 0 {
		sb.WriteString("\n  lines left behind by the test:\n    ")
		sb.WriteString(strings.Join(added, "\n    "))
	}
	return fmt.Errorf("%s", sb.String())
}

// ---------------------------------------------------------------------------
// Pre-flight checks. All of them fail the test (never skip) when a scratch
// resource is in use: that is a configuration mistake the operator must look
// at, not a reason to try a different one.

// preflightScratchVLANs requires that none of the VLAN IDs exist on the
// device. An existing VLAN — the management VLAN among them — is never
// touched.
func (e *Env) preflightScratchVLANs(t *testing.T, s *Snapshot, ids ...int) {
	t.Helper()
	for _, id := range ids {
		if id == 1 {
			t.Fatalf("VLAN 1 can never be a scratch VLAN")
		}
		if v := s.Config.FindVLAN(id); v != nil {
			t.Fatalf("scratch VLAN %d already exists on %s (name %q); pick an unused ID via %s / %s",
				id, e.Host, v.Name, envScratchVLAN, envScratchVLAN2)
		}
	}
}

// preflightTrunkReconcile skips the test when creating a VLAN would make
// icx_vlan.Create tag it onto trunk ports other than the scratch port, unless
// the operator has allowed that with TF_ACC_ALLOW_TRUNK_RECONCILE=true.
func (e *Env) preflightTrunkReconcile(t *testing.T, s *Snapshot, ids ...int) {
	t.Helper()
	seen := map[string]bool{}
	var ports []string
	for _, id := range ids {
		for _, p := range icxresource.FindTrunkAllPorts(s.Config, id) {
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	if len(ports) == 0 {
		return
	}
	sort.Strings(ports)
	if e.AllowTrunkReconcile {
		t.Logf("NOTE: icx_vlan.Create will tag the scratch VLAN(s) onto trunk-all port(s) %s (allowed by %s)",
			strings.Join(ports, ", "), envAllowTrunkReconcile)
		return
	}
	t.Skipf("creating a VLAN on %s would make icx_vlan.Create tag it onto trunk-all port(s) %s "+
		"(CLAUDE.md gotcha #8), which this harness refuses to do by default. Set %s=true to allow it: "+
		"the membership is removed again with the VLAN and no existing VLAN on those trunks is touched.",
		e.Host, strings.Join(ports, ", "), envAllowTrunkReconcile)
}

// preflightScratchPort requires that the scratch port is not configured in
// any way: no interface stanza, no VLAN membership besides the implicit
// default VLAN, and no link. A port that fails any of these is in use.
func (e *Env) preflightScratchPort(t *testing.T, s *Snapshot) {
	t.Helper()
	e.requireScratchPort(t)
	port := e.ScratchPort
	if iface := s.Config.FindEthernetInterface(port); iface != nil {
		t.Fatalf("scratch port %s on %s has an interface stanza (port-name %q); the scratch port must be unconfigured",
			port, e.Host, iface.PortName)
	}
	for _, v := range s.Config.VLANs {
		for _, p := range v.TaggedPorts {
			if p == port {
				t.Fatalf("scratch port %s on %s is a tagged member of VLAN %d; the scratch port must be in no VLAN", port, e.Host, v.ID)
			}
		}
		for _, p := range v.UntaggedPorts {
			if p == port {
				t.Fatalf("scratch port %s on %s is an untagged member of VLAN %d; the scratch port must be in no VLAN", port, e.Host, v.ID)
			}
		}
	}

	c, err := e.connect()
	if err != nil {
		t.Fatalf("connect to %s: %v", e.Host, err)
	}
	defer func() { _ = c.Close() }()
	out, err := c.Execute("show interfaces brief ethernet " + port)
	if err != nil {
		t.Fatalf("show interfaces brief ethernet %s: %v", port, err)
	}
	if strings.Contains(out, "Invalid") || strings.Contains(out, "Error") {
		t.Fatalf("scratch port %s does not exist on %s:\n%s", port, e.Host, strings.TrimSpace(out))
	}
	// Row format: Port Link State Dupl Speed Trunk Tag Pvid Pri MAC Name.
	found := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == port {
			found = true
			if strings.EqualFold(f[1], "Up") {
				t.Fatalf("scratch port %s on %s has link up; the scratch port must have nothing connected:\n%s",
					port, e.Host, strings.TrimSpace(line))
			}
			t.Logf("scratch port %s: link %s", port, f[1])
		}
	}
	if !found {
		t.Logf("WARNING: could not find %s in `show interfaces brief` output; link state not verified:\n%s", port, strings.TrimSpace(out))
	}
}

// preflightScratchUser requires that the scratch user does not exist.
func (e *Env) preflightScratchUser(t *testing.T, s *Snapshot) {
	t.Helper()
	for _, u := range s.Config.Users {
		if u.Username == scratchUser {
			t.Fatalf("scratch user %q already exists on %s; remove it by hand before running this test", scratchUser, e.Host)
		}
	}
}

// ---------------------------------------------------------------------------
// Provider wiring.

// ProviderFactories serves the provider in-process for terraform-plugin-
// testing. The framework asks the factory for a fresh provider before every
// terraform command and never tells it when the previous one is done, so
// each SSH session the provider opens in Configure would otherwise stay open
// until the test binary exits — and a FastIron device allows only a handful
// of concurrent SSH sessions. Commands run one after another, so closing the
// previous instance when the next is requested is safe; the last one is
// closed in t.Cleanup.
func (e *Env) ProviderFactories(t *testing.T) map[string]func() (tfprotov6.ProviderServer, error) {
	t.Helper()
	var mu sync.Mutex
	var prev *provider.FastIronICXProvider
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if prev != nil {
			_ = prev.Close()
			prev = nil
		}
	})
	return map[string]func() (tfprotov6.ProviderServer, error){
		"icx": func() (tfprotov6.ProviderServer, error) {
			mu.Lock()
			defer mu.Unlock()
			if prev != nil {
				_ = prev.Close()
			}
			p, ok := provider.New("acctest")().(*provider.FastIronICXProvider)
			if !ok {
				return nil, fmt.Errorf("provider.New did not return a *FastIronICXProvider")
			}
			prev = p
			return providerserver.NewProtocol6WithError(p)()
		},
	}
}

// ProviderConfig renders the provider block. Host, credentials and host key
// come from the FASTIRON_* environment the provider reads itself, so no
// secret lands in the configuration file; only the attributes without an
// environment variable are rendered.
func (e *Env) ProviderConfig() string {
	var b strings.Builder
	b.WriteString("provider \"icx\" {\n")
	if e.LegacySSH {
		b.WriteString("  legacy_ssh_algorithms = true\n")
	}
	b.WriteString("}\n\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Small helpers shared by the tests.

// hclStringList renders a Terraform list of strings.
func hclStringList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// addressMaskToCIDR converts the running config's global `ip address A M`
// value ("10.0.20.5 255.255.255.0", possibly followed by a keyword) to CIDR.
func addressMaskToCIDR(raw string) (string, error) {
	f := strings.Fields(raw)
	if len(f) < 2 {
		return "", fmt.Errorf("expected `address mask`, got %q", raw)
	}
	ip := net.ParseIP(f[0]).To4()
	mask := net.ParseIP(f[1]).To4()
	if ip == nil || mask == nil {
		return "", fmt.Errorf("not an IPv4 address and mask: %q", raw)
	}
	ones, bits := net.IPMask(mask).Size()
	if bits == 0 {
		return "", fmt.Errorf("non-contiguous mask in %q", raw)
	}
	return fmt.Sprintf("%s/%d", ip, ones), nil
}

// portIn reports whether port appears in ports.
func portIn(port string, ports []string) bool {
	for _, p := range ports {
		if p == port {
			return true
		}
	}
	return false
}

// vlanMembership describes the scratch port's membership on the device.
type vlanMembership struct {
	tagged   []int
	untagged []int
	dualMode *int
}

func membershipOf(cfg *parser.RunningConfig, port string) vlanMembership {
	var m vlanMembership
	for _, v := range cfg.VLANs {
		if portIn(port, v.TaggedPorts) {
			m.tagged = append(m.tagged, v.ID)
		}
		if portIn(port, v.UntaggedPorts) {
			m.untagged = append(m.untagged, v.ID)
		}
	}
	if iface := cfg.FindEthernetInterface(port); iface != nil {
		m.dualMode = iface.DualModeVLAN
	}
	return m
}

func (m vlanMembership) String() string {
	dm := "none"
	if m.dualMode != nil {
		dm = strconv.Itoa(*m.dualMode)
	}
	return fmt.Sprintf("tagged %v, untagged %v, dual-mode %s", m.tagged, m.untagged, dm)
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
