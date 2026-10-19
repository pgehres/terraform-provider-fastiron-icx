package resource

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// These tests cover the two spellings of a tag_all_vlans apply: the bulk
// `vlan-config add all-tagged` interface command on FastIron 08.0.50+
// (dialect.CapVlanConfigAllTagged) and the per-VLAN `tagged ethe` expansion
// everywhere else. They run against the real ICX 7250-24 capture (08.0.95,
// VLANs 1/10/20/30/40/50/60/70; port 1/1/16 untagged in 20, port 1/1/15
// untagged in 20 and tagged in 30-70) and the ICX 6430-C12 capture (08.0.30).

const bulkCmd = "vlan-config add all-tagged"

var (
	dialect08095 = dialect.Detect("08.0.95mT213", parser.ImageTypeRouter)
	dialect08030 = dialect.Detect("08.0.30uT311", parser.ImageTypeSwitch)
)

// runCreate runs Create for plan against cfg under dialect d and returns the
// mock, so callers can inspect the command stream including config reads.
func runCreate(t *testing.T, cfg string, d dialect.Dialect, plan *InterfaceEthernetResourceModel) (*sshclient.MockClient, resource.CreateResponse) {
	t.Helper()
	mock := sshclient.NewMockClient(cfg)
	r := &InterfaceEthernetResource{client: mock, dialect: d}
	_, p := ethStateAndPlan(t, r, nil, plan)
	state, _ := ethStateAndPlan(t, r, nil, nil)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	return mock, resp
}

func runUpdate(t *testing.T, cfg string, d dialect.Dialect, state, plan *InterfaceEthernetResourceModel) (*sshclient.MockClient, resource.UpdateResponse) {
	t.Helper()
	mock := sshclient.NewMockClient(cfg)
	r := &InterfaceEthernetResource{client: mock, dialect: d}
	s, p := ethStateAndPlan(t, r, state, plan)
	resp := resource.UpdateResponse{State: s}
	r.Update(context.Background(), resource.UpdateRequest{Plan: p, State: s}, &resp)
	return mock, resp
}

func mustOK(t *testing.T, what string, diags interface{ HasError() bool }, errs any) {
	t.Helper()
	if diags.HasError() {
		t.Fatalf("%s error: %v", what, errs)
	}
}

func countCommand(cmds []string, want string) int {
	n := 0
	for _, c := range cmds {
		if c == want {
			n++
		}
	}
	return n
}

// --- command generation ---------------------------------------------------

// 08.0.95: a tag_all_vlans create sends the untagged membership, then the one
// bulk command in interface context, then re-reads the config to verify it.
func TestInterfaceEthernetCreate_TagAllBulkOn08095(t *testing.T) {
	mock, resp := runCreate(t, loadFixture(t, "7250-24"), dialect08095, ethModel("1/1/16", 20, nil, true))
	mustOK(t, "Create", resp.Diagnostics, resp.Diagnostics)
	want := []string{
		"interface ethernet 1/1/16", "exit",
		"vlan 20", "untagged ethe 1/1/16", "exit",
		"interface ethernet 1/1/16", bulkCmd, "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if n := countCommand(mock.Commands, "show running-config"); n != 2 {
		t.Errorf("expected the bulk apply to re-read the running config once (2 reads total), got %d", n)
	}
	if mock.WriteMemoryCalls != 1 {
		t.Errorf("write memory calls = %d, want 1", mock.WriteMemoryCalls)
	}
}

// The provider's dialect may be unknown while the config's own `ver` line is
// parseable: the config is evidence enough for the bulk command.
func TestInterfaceEthernetCreate_TagAllBulkFromConfigVersion(t *testing.T) {
	mock, resp := runCreate(t, loadFixture(t, "7250-24"), dialect.Dialect{}, ethModel("1/1/16", 0, nil, true))
	mustOK(t, "Create", resp.Diagnostics, resp.Diagnostics)
	if countCommand(mock.Commands, bulkCmd) != 1 {
		t.Errorf("expected %q with an 08.0.95 ver line, got %q", bulkCmd, cliOf(mock))
	}
}

// No firmware evidence at all: the per-VLAN expansion, never a command that
// might not exist. Exactly one config read (nothing to verify).
func TestInterfaceEthernetCreate_TagAllExpansionOnUnknownFirmware(t *testing.T) {
	cfg := stripDialectEvidence(loadFixture(t, "7250-24"))
	mock, resp := runCreate(t, cfg, dialect.Dialect{}, ethModel("1/1/16", 20, nil, true))
	mustOK(t, "Create", resp.Diagnostics, resp.Diagnostics)
	want := []string{
		"interface ethernet 1/1/16", "exit",
		"vlan 20", "untagged ethe 1/1/16", "exit",
		"vlan 10", "tagged ethe 1/1/16", "exit",
		"vlan 30", "tagged ethe 1/1/16", "exit",
		"vlan 40", "tagged ethe 1/1/16", "exit",
		"vlan 50", "tagged ethe 1/1/16", "exit",
		"vlan 60", "tagged ethe 1/1/16", "exit",
		"vlan 70", "tagged ethe 1/1/16", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if n := countCommand(mock.Commands, "show running-config"); n != 1 {
		t.Errorf("expansion path must not re-read the config, got %d reads", n)
	}
}

// FastIron 08.0.30 predates vlan-config (08.0.50): the dual-mode builder's
// per-VLAN expansion is used and the bulk command never appears.
func TestInterfaceEthernetCreate_TagAllNeverBulkOn08030(t *testing.T) {
	mock, resp := runCreate(t, loadFixture(t, "6430-c12"), dialect08030, ethModel("1/1/7", 102, nil, true))
	mustOK(t, "Create", resp.Diagnostics, resp.Diagnostics)
	cmds := cliOf(mock)
	if countCommand(cmds, bulkCmd) != 0 {
		t.Errorf("08.0.30 must never send %q, got %q", bulkCmd, cmds)
	}
	if countCommand(cmds, "tagged ethe 1/1/7") == 0 {
		t.Errorf("expected per-VLAN tagging on 08.0.30, got %q", cmds)
	}
	if n := countCommand(mock.Commands, "show running-config"); n != 1 {
		t.Errorf("dual-mode path must not re-read the config, got %d reads", n)
	}
}

// An explicit tagged_vlans create is untouched by the capability: the bulk
// command is only ever a spelling of tag_all_vlans.
func TestInterfaceEthernetCreate_ExplicitTaggedNeverBulk(t *testing.T) {
	mock, resp := runCreate(t, loadFixture(t, "7250-24"), dialect08095, ethModel("1/1/16", 20, []int64{30, 10}, false))
	mustOK(t, "Create", resp.Diagnostics, resp.Diagnostics)
	want := []string{
		"interface ethernet 1/1/16", "exit",
		"vlan 20", "untagged ethe 1/1/16", "exit",
		"vlan 10", "tagged ethe 1/1/16", "exit",
		"vlan 30", "tagged ethe 1/1/16", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// Update from an explicit list to tag_all_vlans — the shape Read produces when
// it reports a drifted trunk (tag_all_vlans = false, tagged_vlans = actual) —
// adds the missing VLANs with the bulk command and removes nothing.
func TestInterfaceEthernetUpdate_ExplicitToTagAllUsesBulk(t *testing.T) {
	mock, resp := runUpdate(t, loadFixture(t, "7250-24"), dialect08095,
		ethModel("1/1/15", 20, []int64{30, 40, 50, 60, 70}, false),
		ethModel("1/1/15", 20, nil, true))
	mustOK(t, "Update", resp.Diagnostics, resp.Diagnostics)
	want := []string{"interface ethernet 1/1/15", bulkCmd, "exit"}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if mock.WriteMemoryCalls != 1 {
		t.Errorf("write memory calls = %d, want 1", mock.WriteMemoryCalls)
	}
}

// Same transition on unknown firmware: only the one missing VLAN, per VLAN.
func TestInterfaceEthernetUpdate_ExplicitToTagAllExpansionOnUnknownFirmware(t *testing.T) {
	cfg := stripDialectEvidence(loadFixture(t, "7250-24"))
	mock, resp := runUpdate(t, cfg, dialect.Dialect{},
		ethModel("1/1/15", 20, []int64{30, 40, 50, 60, 70}, false),
		ethModel("1/1/15", 20, nil, true))
	mustOK(t, "Update", resp.Diagnostics, resp.Diagnostics)
	want := []string{"vlan 10", "tagged ethe 1/1/15", "exit"}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// Changing the untagged VLAN of a tag_all_vlans trunk: removals stay per VLAN
// and precede the new untagged membership; the old untagged VLAN then joins
// the tagged set through the bulk command.
func TestInterfaceEthernetUpdate_TagAllChangeUntaggedUsesBulkForAdd(t *testing.T) {
	want := []string{
		"vlan 20", "no untagged ethe 1/1/15", "exit",
		"vlan 30", "no tagged ethe 1/1/15", "exit",
		"vlan 30", "untagged ethe 1/1/15", "exit",
		"interface ethernet 1/1/15", bulkCmd, "exit",
	}
	// The verification read sees the device as those commands leave it (bulk
	// command skipping VLAN 1 and the untagged VLAN), so nothing is corrected.
	seed, err := parser.ParseRunningConfig(loadFixture(t, "7250-24"))
	if err != nil {
		t.Fatal(err)
	}
	after := newSimSwitch(seed, false, false)
	after.run(t, want)

	mock := sshclient.NewMockClient(loadFixture(t, "7250-24"))
	mock.RunningConfigQueue = []string{loadFixture(t, "7250-24"), after.render()}
	r := &InterfaceEthernetResource{client: mock, dialect: dialect08095}
	st, p := ethStateAndPlan(t, r, ethModel("1/1/15", 20, nil, true), ethModel("1/1/15", 30, nil, true))
	resp := resource.UpdateResponse{State: st}
	r.Update(context.Background(), resource.UpdateRequest{Plan: p, State: st}, &resp)
	mustOK(t, "Update", resp.Diagnostics, resp.Diagnostics)
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if n := countCommand(mock.Commands, "show running-config"); n != 2 {
		t.Errorf("expected one verification re-read (2 reads total), got %d", n)
	}
}

// A tag_all_vlans trunk with nothing to add sends nothing — no bulk command,
// no verification read, no write memory.
func TestInterfaceEthernetUpdate_TagAllNoChangeNoBulk(t *testing.T) {
	mock, resp := runUpdate(t, loadFixture(t, "7250-24"), dialect08095,
		ethModel("1/1/15", 20, nil, true),
		ethModel("1/1/15", 20, nil, true))
	mustOK(t, "Update", resp.Diagnostics, resp.Diagnostics)
	if got := cliOf(mock); len(got) != 0 {
		t.Errorf("expected no commands, got %q", got)
	}
	if n := countCommand(mock.Commands, "show running-config"); n != 1 {
		t.Errorf("expected a single config read, got %d", n)
	}
	if mock.WriteMemoryCalls != 0 {
		t.Errorf("write memory calls = %d, want 0", mock.WriteMemoryCalls)
	}
}

// --- verification read-back -----------------------------------------------

// withBulkSideEffects returns the 7250 fixture as it would look if the bulk
// command had also tagged port in VLAN 1 and converted its untagged VLAN 20
// membership to tagged — the two side effects the provider does not assume
// away.
func withBulkSideEffects(t *testing.T, port string) string {
	t.Helper()
	cfg := loadFixture(t, "7250-24")
	cfg = strings.Replace(cfg, "vlan 1 name DEFAULT-VLAN by port\n", "vlan 1 name DEFAULT-VLAN by port\n tagged ethe "+port+"\n", 1)
	cfg = strings.Replace(cfg, " untagged ethe 1/1/1 to 1/1/18 ethe 1/1/24 ethe 1/2/1 ethe 1/2/4 to 1/2/6\n",
		" tagged ethe "+port+"\n untagged ethe 1/1/1 to 1/1/15 ethe 1/1/17 to 1/1/18 ethe 1/1/24 ethe 1/2/1 ethe 1/2/4 to 1/2/6\n", 1)
	return cfg
}

// If the re-read shows the bulk command tagged VLAN 1 or the untagged VLAN,
// those memberships are corrected so the device ends up exactly where the
// expansion would have put it.
func TestInterfaceEthernetCreate_BulkCorrectsDefaultAndUntaggedVLAN(t *testing.T) {
	mock := sshclient.NewMockClient(loadFixture(t, "7250-24"))
	mock.RunningConfigQueue = []string{loadFixture(t, "7250-24"), withBulkSideEffects(t, "1/1/16")}
	r := &InterfaceEthernetResource{client: mock, dialect: dialect08095}
	_, p := ethStateAndPlan(t, r, nil, ethModel("1/1/16", 20, nil, true))
	state, _ := ethStateAndPlan(t, r, nil, nil)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	mustOK(t, "Create", resp.Diagnostics, resp.Diagnostics)
	want := []string{
		"interface ethernet 1/1/16", "exit",
		"vlan 20", "untagged ethe 1/1/16", "exit",
		"interface ethernet 1/1/16", bulkCmd, "exit",
		"vlan 1", "no tagged ethe 1/1/16", "exit",
		"vlan 20", "no tagged ethe 1/1/16", "untagged ethe 1/1/16", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if mock.WriteMemoryCalls != 1 {
		t.Errorf("write memory calls = %d, want 1", mock.WriteMemoryCalls)
	}
}

// Without an untagged VLAN only VLAN 1 can need correcting.
func TestBulkTaggingCorrections(t *testing.T) {
	cfg, err := parser.ParseRunningConfig(withBulkSideEffects(t, "1/1/16"))
	if err != nil {
		t.Fatal(err)
	}
	if got := bulkTaggingCorrections(cfg, "1/1/16", 0); !reflect.DeepEqual(got, []string{"vlan 1", "no tagged ethe 1/1/16", "exit"}) {
		t.Errorf("no untagged VLAN: got %q", got)
	}
	// Tagged in VLAN 20 but 20 is the untagged VLAN and the untagged
	// membership survived: drop the tag only.
	cfg2 := strings.Replace(withBulkSideEffects(t, "1/1/16"), "ethe 1/1/1 to 1/1/15 ethe 1/1/17 to 1/1/18", "ethe 1/1/1 to 1/1/18", 1)
	c2, err := parser.ParseRunningConfig(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vlan 1", "no tagged ethe 1/1/16", "exit", "vlan 20", "no tagged ethe 1/1/16", "exit"}
	if got := bulkTaggingCorrections(c2, "1/1/16", 20); !reflect.DeepEqual(got, want) {
		t.Errorf("untagged survived: got %q, want %q", got, want)
	}
	// The healthy case — what the fleet's captures show — needs nothing.
	healthy, err := parser.ParseRunningConfig(loadFixture(t, "7250-24"))
	if err != nil {
		t.Fatal(err)
	}
	if got := bulkTaggingCorrections(healthy, "1/1/15", 20); len(got) != 0 {
		t.Errorf("healthy port: got %q, want none", got)
	}
	if got := bulkTaggingCorrections(healthy, "1/1/15", 0); len(got) != 0 {
		t.Errorf("healthy port, no untagged: got %q, want none", got)
	}
}

// A refused bulk command (the switch prints "error - ..." when, say, the port
// carries an IP address) fails the apply before write memory, with nothing
// else sent.
func TestInterfaceEthernetCreate_BulkRefusedFailsApply(t *testing.T) {
	mock := sshclient.NewMockClient(loadFixture(t, "7250-24"))
	mock.ErrorOnCommand[bulkCmd] = errors.New("error - IP routing, vrf, ip policy, route-only, rpf-mode, ip-mac or ip tcp mss was configured on ports  1/1/16")
	r := &InterfaceEthernetResource{client: mock, dialect: dialect08095}
	_, p := ethStateAndPlan(t, r, nil, ethModel("1/1/16", 20, nil, true))
	state, _ := ethStateAndPlan(t, r, nil, nil)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected Create to fail when the bulk command is refused")
	}
	if mock.WriteMemoryCalls != 0 {
		t.Errorf("write memory must not run after a refused bulk command, got %d", mock.WriteMemoryCalls)
	}
	if last := mock.Commands[len(mock.Commands)-1]; last != bulkCmd {
		t.Errorf("nothing may follow the refused command, got %q", mock.Commands)
	}
}

// --- convergence: both spellings yield the same membership ----------------

// simSwitch is a minimal model of FastIron VLAN membership, enough to replay
// the provider's command streams. It rejects sequences FastIron rejects (a
// port tagged and untagged in one VLAN, removing a membership that is not
// there), so a passing test also shows each stream is legal. The two bulk*
// flags parameterise what `vlan-config add all-tagged` is not documented to
// say: whether it also tags the default VLAN 1, and whether it also tags (and
// so un-untags) the port's untagged VLAN.
type simSwitch struct {
	vlans            map[int]*simVLAN
	inVLAN           int
	inIface          string
	bulkTagsDefault  bool
	bulkTagsUntagged bool
}

type simVLAN struct{ tagged, untagged map[string]bool }

func newSimSwitch(cfg *parser.RunningConfig, bulkTagsDefault, bulkTagsUntagged bool) *simSwitch {
	s := &simSwitch{vlans: map[int]*simVLAN{}, bulkTagsDefault: bulkTagsDefault, bulkTagsUntagged: bulkTagsUntagged}
	for _, v := range cfg.VLANs {
		sv := &simVLAN{tagged: map[string]bool{}, untagged: map[string]bool{}}
		for _, p := range v.TaggedPorts {
			sv.tagged[p] = true
		}
		for _, p := range v.UntaggedPorts {
			sv.untagged[p] = true
		}
		s.vlans[v.ID] = sv
	}
	return s
}

func (s *simSwitch) vlanIDs() []int {
	ids := make([]int, 0, len(s.vlans))
	for id := range s.vlans {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func (s *simSwitch) run(t *testing.T, cmds []string) {
	t.Helper()
	for _, c := range cmds {
		f := strings.Fields(c)
		switch {
		case f[0] == "vlan" && len(f) == 2:
			id := atoi(t, f[1])
			if _, ok := s.vlans[id]; !ok {
				s.vlans[id] = &simVLAN{tagged: map[string]bool{}, untagged: map[string]bool{}}
			}
			s.inVLAN, s.inIface = id, ""
		case f[0] == "interface" && f[1] == "ethernet":
			s.inVLAN, s.inIface = 0, f[2]
		case c == "exit":
			s.inVLAN, s.inIface = 0, ""
		case strings.HasPrefix(c, "tagged ethe "):
			v := s.ctxVLAN(t, c)
			if v.untagged[f[2]] {
				t.Fatalf("%q: port is untagged in vlan %d", c, s.inVLAN)
			}
			v.tagged[f[2]] = true
		case strings.HasPrefix(c, "no tagged ethe "):
			v := s.ctxVLAN(t, c)
			if !v.tagged[f[3]] {
				t.Fatalf("%q: port is not tagged in vlan %d", c, s.inVLAN)
			}
			delete(v.tagged, f[3])
		case strings.HasPrefix(c, "untagged ethe "):
			v := s.ctxVLAN(t, c)
			if v.tagged[f[2]] {
				t.Fatalf("%q: port is tagged in vlan %d", c, s.inVLAN)
			}
			for _, o := range s.vlans {
				delete(o.untagged, f[2]) // FastIron moves the untagged membership
			}
			v.untagged[f[2]] = true
		case strings.HasPrefix(c, "no untagged ethe "):
			v := s.ctxVLAN(t, c)
			if !v.untagged[f[3]] {
				t.Fatalf("%q: port is not untagged in vlan %d", c, s.inVLAN)
			}
			delete(v.untagged, f[3])
		case c == bulkCmd:
			if s.inIface == "" {
				t.Fatalf("%q outside interface context", c)
			}
			for _, id := range s.vlanIDs() {
				v := s.vlans[id]
				if id == 1 && !s.bulkTagsDefault {
					continue
				}
				if v.untagged[s.inIface] {
					if s.bulkTagsUntagged {
						delete(v.untagged, s.inIface)
						v.tagged[s.inIface] = true
					}
					continue
				}
				v.tagged[s.inIface] = true
			}
		default:
			t.Fatalf("simSwitch: unknown command %q", c)
		}
	}
}

func (s *simSwitch) ctxVLAN(t *testing.T, c string) *simVLAN {
	t.Helper()
	if s.inVLAN == 0 {
		t.Fatalf("%q outside VLAN context", c)
	}
	return s.vlans[s.inVLAN]
}

// membership reports port's membership as vlan -> "tagged"/"untagged". An
// implicit untagged membership of VLAN 1 is not modelled (FastIron does not
// print it either).
func (s *simSwitch) membership(port string) map[int]string {
	m := map[int]string{}
	for id, v := range s.vlans {
		if v.tagged[port] {
			m[id] = "tagged"
		}
		if v.untagged[port] && id != 1 {
			m[id] = "untagged"
		}
	}
	return m
}

// config renders the simulated membership as a parser.RunningConfig, which is
// what verifyBulkTagging re-reads from the switch.
func (s *simSwitch) config() *parser.RunningConfig {
	cfg := &parser.RunningConfig{}
	for _, id := range s.vlanIDs() {
		v := s.vlans[id]
		pv := parser.VLAN{ID: id}
		for p := range v.tagged {
			pv.TaggedPorts = append(pv.TaggedPorts, p)
		}
		for p := range v.untagged {
			pv.UntaggedPorts = append(pv.UntaggedPorts, p)
		}
		cfg.VLANs = append(cfg.VLANs, pv)
	}
	return cfg
}

// render writes the simulated membership back out as running-config text,
// the way FastIron prints it, for a mock's verification re-read.
func (s *simSwitch) render() string {
	var b strings.Builder
	b.WriteString("ver 08.0.95mT213\n!\n")
	for _, id := range s.vlanIDs() {
		v := s.vlans[id]
		if id == 1 {
			b.WriteString("vlan 1 name DEFAULT-VLAN by port\n")
		} else {
			fmt.Fprintf(&b, "vlan %d by port\n", id)
		}
		writePorts := func(kind string, set map[string]bool) {
			if len(set) == 0 {
				return
			}
			ports := make([]string, 0, len(set))
			for p := range set {
				ports = append(ports, p)
			}
			sort.Strings(ports)
			fmt.Fprintf(&b, " %s ethe %s\n", kind, strings.Join(ports, " ethe "))
		}
		writePorts("tagged", v.tagged)
		writePorts("untagged", v.untagged)
		b.WriteString("!\n")
	}
	return b.String()
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// For every tag_all_vlans transition the provider can generate, replaying the
// bulk stream (plus the corrections verifyBulkTagging derives from the
// resulting config) and the per-VLAN stream on the same starting device
// yields identical membership — under every reading of what the bulk command
// does to VLAN 1 and the untagged VLAN — and that membership is exactly the
// expansion tag_all_vlans stands for.
func TestTagAllVLANs_BulkAndExpansionConverge(t *testing.T) {
	// In the capture 1/1/15 is tagged in 30-70 but not 10, i.e. a drifted
	// trunk (see TestInterfaceEthernetUpdate_BulkHealsUnrecordedDrift). The
	// scenarios that start from tag_all_vlans state need a device that matches
	// that state, so tag it in 10 as well.
	fixture := strings.Replace(loadFixture(t, "7250-24"),
		" tagged ethe 1/1/19 to 1/1/20 ethe 1/2/1 ethe 1/2/3 to 1/2/8\n",
		" tagged ethe 1/1/15 ethe 1/1/19 to 1/1/20 ethe 1/2/1 ethe 1/2/3 to 1/2/8\n", 1)
	seed, err := parser.ParseRunningConfig(fixture)
	if err != nil {
		t.Fatal(err)
	}
	bare := stripDialectEvidence(fixture)

	type scenario struct {
		name        string
		port        string
		state, plan *InterfaceEthernetResourceModel
	}
	scenarios := []scenario{
		{"create trunk with untagged", "1/1/16", nil, ethModel("1/1/16", 20, nil, true)},
		{"create trunk moving untagged", "1/1/16", nil, ethModel("1/1/16", 30, nil, true)},
		// 1/1/22 is tagged in VLAN 20 and untagged nowhere: a create that
		// leaves the port without an untagged VLAN must not start from one,
		// since Create does not reconcile membership it was never told about
		// (pre-existing behaviour, unchanged here).
		{"create tagged-only trunk", "1/1/22", nil, ethModel("1/1/22", 0, nil, true)},
		{"create with untagged 1", "1/1/22", nil, ethModel("1/1/22", 1, nil, true)},
		{"explicit to tag_all", "1/1/15", ethModel("1/1/15", 20, []int64{30, 40, 50, 60, 70}, false), ethModel("1/1/15", 20, nil, true)},
		{"drifted trunk repair", "1/1/15", ethModel("1/1/15", 20, []int64{30, 40, 50}, false), ethModel("1/1/15", 20, nil, true)},
		{"tag_all change untagged", "1/1/15", ethModel("1/1/15", 20, nil, true), ethModel("1/1/15", 30, nil, true)},
		{"tag_all drop untagged", "1/1/15", ethModel("1/1/15", 20, nil, true), ethModel("1/1/15", 0, nil, true)},
	}
	semantics := []struct {
		name                      string
		tagsDefault, tagsUntagged bool
	}{
		{"skips VLAN 1 and untagged VLAN", false, false},
		{"tags VLAN 1 too", true, false},
		{"tags the untagged VLAN too", false, true},
		{"tags both", true, true},
	}

	// commandsFor returns the stream the provider sends up to and including
	// the membership change itself. Against the static mock, Create/Update's
	// verification re-read sees a device the commands never touched and may
	// append corrections for that stale picture; those are cut off here and
	// re-derived below from the simulated device with the same function
	// verifyBulkTagging uses (bulkTaggingCorrections), which is what a real
	// switch's re-read would feed it.
	commandsFor := func(sc scenario, d dialect.Dialect) []string {
		var mock *sshclient.MockClient
		if sc.state == nil {
			var resp resource.CreateResponse
			mock, resp = runCreate(t, bare, d, sc.plan)
			mustOK(t, sc.name+" create", resp.Diagnostics, resp.Diagnostics)
		} else {
			var resp resource.UpdateResponse
			mock, resp = runUpdate(t, bare, d, sc.state, sc.plan)
			mustOK(t, sc.name+" update", resp.Diagnostics, resp.Diagnostics)
		}
		cmds := cliOf(mock)
		for i, c := range cmds {
			if c == bulkCmd {
				return cmds[:i+2] // through the following "exit"
			}
		}
		return cmds
	}

	for _, sc := range scenarios {
		bulkStream := commandsFor(sc, dialect08095)
		if countCommand(bulkStream, bulkCmd) != 1 {
			t.Fatalf("%s: expected exactly one %q in %q", sc.name, bulkCmd, bulkStream)
		}
		plainStream := commandsFor(sc, dialect.Dialect{})
		if countCommand(plainStream, bulkCmd) != 0 {
			t.Fatalf("%s: unknown firmware must not use %q: %q", sc.name, bulkCmd, plainStream)
		}

		var untagged int
		if !sc.plan.UntaggedVLAN.IsNull() {
			untagged = int(sc.plan.UntaggedVLAN.ValueInt64())
		}
		want := map[int]string{}
		for _, id := range expandedTagAll(seed, untagged) {
			want[id] = "tagged"
		}
		if untagged > 1 {
			want[untagged] = "untagged"
		}

		plain := newSimSwitch(seed, false, false)
		plain.run(t, plainStream)
		if got := plain.membership(sc.port); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: expansion path membership %v, want %v", sc.name, got, want)
		}

		for _, sem := range semantics {
			bulk := newSimSwitch(seed, sem.tagsDefault, sem.tagsUntagged)
			bulk.run(t, bulkStream)
			corrections := bulkTaggingCorrections(bulk.config(), sc.port, untagged)
			bulk.run(t, corrections)
			if got := bulk.membership(sc.port); !reflect.DeepEqual(got, want) {
				t.Errorf("%s / bulk %s: membership %v (after corrections %q), want %v", sc.name, sem.name, got, corrections, want)
			}
			if !sem.tagsDefault && !sem.tagsUntagged && len(corrections) != 0 {
				t.Errorf("%s: a bulk command that already skips VLAN 1 and the untagged VLAN must need no corrections, got %q", sc.name, corrections)
			}
		}
	}
}

// expandedTagAll is the membership tag_all_vlans stands for: every VLAN in
// cfg except VLAN 1 and untagged.
func expandedTagAll(cfg *parser.RunningConfig, untagged int) []int {
	var ids []int
	for _, v := range cfg.VLANs {
		if v.ID == 1 || v.ID == untagged {
			continue
		}
		ids = append(ids, v.ID)
	}
	sort.Ints(ids)
	return ids
}

// A side effect worth knowing: when state says tag_all_vlans = true but the
// device is missing a VLAN that Read has not (yet) reported — 1/1/15 in the
// capture lacks VLAN 10 — the per-VLAN path trusts state and never adds it,
// while the bulk command tags every VLAN regardless. So any tag_all_vlans
// apply that adds something on 08.0.50+ also heals that kind of drift.
func TestInterfaceEthernetUpdate_BulkHealsUnrecordedDrift(t *testing.T) {
	fixture := loadFixture(t, "7250-24")
	seed, err := parser.ParseRunningConfig(fixture)
	if err != nil {
		t.Fatal(err)
	}
	state, plan := ethModel("1/1/15", 20, nil, true), ethModel("1/1/15", 30, nil, true)

	mock, resp := runUpdate(t, stripDialectEvidence(fixture), dialect.Dialect{}, state, plan)
	mustOK(t, "Update (expansion)", resp.Diagnostics, resp.Diagnostics)
	plain := newSimSwitch(seed, false, false)
	plain.run(t, cliOf(mock))
	if got := plain.membership("1/1/15")[10]; got != "" {
		t.Errorf("per-VLAN path should not have touched VLAN 10, got %q", got)
	}

	mock, resp = runUpdate(t, fixture, dialect08095, state, plan)
	mustOK(t, "Update (bulk)", resp.Diagnostics, resp.Diagnostics)
	cmds := cliOf(mock)
	if i := indexOf(cmds, bulkCmd); i >= 0 {
		cmds = cmds[:i+2]
	}
	bulk := newSimSwitch(seed, false, false)
	bulk.run(t, cmds)
	if got := bulk.membership("1/1/15")[10]; got != "tagged" {
		t.Errorf("bulk path should have tagged the missing VLAN 10, got %q", got)
	}
}
