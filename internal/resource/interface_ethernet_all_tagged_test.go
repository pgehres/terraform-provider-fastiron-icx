package resource

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// These tests cover the two spellings of a tag_all_vlans apply: the bulk
// `vlan-config add all-tagged` interface command (experimental, opt-in through
// the provider's native_bulk_tagging, FastIron 08.0.50+, tagged-only
// non-management trunks only) and the per-VLAN `tagged ethe` expansion
// everywhere else. They run against the real ICX 7250-24 capture (08.0.95,
// VLANs 1/10/20/30/40/50/60/70, default route via 10.0.20.1; port 1/1/16
// untagged in 20, 1/1/15 untagged in 20 and tagged in 30-70, 1/1/22 tagged in
// 20 only and untagged nowhere, 1/2/1 untagged in management VLAN 20 and
// tagged in the rest) and the ICX 6430-C12 capture (08.0.30).

const bulkCmd = "vlan-config add all-tagged"

var (
	dialect08095 = dialect.Detect("08.0.95mT213", parser.ImageTypeRouter)
	dialect08030 = dialect.Detect("08.0.30uT311", parser.ImageTypeSwitch)
)

// arpVia is router-image `show arp` output placing the 7250's management next
// hop (its default route, 10.0.20.1) behind port.
func arpVia(port string) string {
	return "Total number of ARP entries: 1\n" +
		"   1     10.0.20.1       0000.5e00.0101  Dynamic 0   " + port + "  Valid\n"
}

// applyEth runs Create (state == nil) or Update against exec and returns the
// diagnostics and the state the operation recorded (nil when none was set).
func applyEth(t *testing.T, exec sshclient.CommandExecutor, d dialect.Dialect, bulk bool, state, plan *InterfaceEthernetResourceModel) (diag.Diagnostics, *InterfaceEthernetResourceModel) {
	t.Helper()
	ctx := context.Background()
	r := &InterfaceEthernetResource{client: exec, dialect: d, nativeBulkTagging: bulk}
	var diags diag.Diagnostics
	var recorded InterfaceEthernetResourceModel
	if state == nil {
		_, p := ethStateAndPlan(t, r, nil, plan)
		empty, _ := ethStateAndPlan(t, r, nil, nil)
		resp := resource.CreateResponse{State: empty}
		r.Create(ctx, resource.CreateRequest{Plan: p}, &resp)
		diags = resp.Diagnostics
		if resp.State.Raw.IsNull() {
			return diags, nil
		}
		if dd := resp.State.Get(ctx, &recorded); dd.HasError() {
			t.Fatalf("read back state: %v", dd)
		}
		return diags, &recorded
	}
	s, p := ethStateAndPlan(t, r, state, plan)
	resp := resource.UpdateResponse{State: s}
	r.Update(ctx, resource.UpdateRequest{Plan: p, State: s}, &resp)
	if dd := resp.State.Get(ctx, &recorded); dd.HasError() {
		t.Fatalf("read back state: %v", dd)
	}
	return resp.Diagnostics, &recorded
}

func mockApply(t *testing.T, cfg string, d dialect.Dialect, bulk bool, state, plan *InterfaceEthernetResourceModel) (*sshclient.MockClient, diag.Diagnostics) {
	t.Helper()
	mock := sshclient.NewMockClient(cfg)
	mock.Responses["show arp"] = arpVia("1/2/1")
	diags, _ := applyEth(t, mock, d, bulk, state, plan)
	return mock, diags
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

func without(cmds []string, drop string) []string {
	var out []string
	for _, c := range cmds {
		if c != drop {
			out = append(out, c)
		}
	}
	return out
}

// fixture15 is the 7250 capture with 1/1/15 also tagged in VLAN 10, so a
// state saying tag_all_vlans = true for it matches the device.
func fixture15(t *testing.T) string {
	t.Helper()
	return strings.Replace(loadFixture(t, "7250-24"),
		" tagged ethe 1/1/19 to 1/1/20 ethe 1/2/1 ethe 1/2/3 to 1/2/8\n",
		" tagged ethe 1/1/15 ethe 1/1/19 to 1/1/20 ethe 1/2/1 ethe 1/2/3 to 1/2/8\n", 1)
}

// --- default off: byte-identical to main -----------------------------------

// mainCommandStreams is the exact mock command stream (config reads and
// write memory included) that origin/main — the per-VLAN expansion, before
// vlan-config bulk tagging existed — produces for each tag_all_vlans scenario
// on the 08.0.95 7250 capture. Captured by running these scenarios on main.
var mainCommandStreams = map[string][]string{
	"create trunk with untagged":   []string{"show running-config", "interface ethernet 1/1/16", "exit", "vlan 20", "untagged ethe 1/1/16", "exit", "vlan 10", "tagged ethe 1/1/16", "exit", "vlan 30", "tagged ethe 1/1/16", "exit", "vlan 40", "tagged ethe 1/1/16", "exit", "vlan 50", "tagged ethe 1/1/16", "exit", "vlan 60", "tagged ethe 1/1/16", "exit", "vlan 70", "tagged ethe 1/1/16", "exit", "write memory"},
	"create trunk no untagged":     []string{"show running-config", "interface ethernet 1/1/16", "exit", "vlan 10", "tagged ethe 1/1/16", "exit", "vlan 20", "tagged ethe 1/1/16", "exit", "vlan 30", "tagged ethe 1/1/16", "exit", "vlan 40", "tagged ethe 1/1/16", "exit", "vlan 50", "tagged ethe 1/1/16", "exit", "vlan 60", "tagged ethe 1/1/16", "exit", "vlan 70", "tagged ethe 1/1/16", "exit", "write memory"},
	"create trunk moving untagged": []string{"show running-config", "interface ethernet 1/1/16", "exit", "vlan 30", "untagged ethe 1/1/16", "exit", "vlan 10", "tagged ethe 1/1/16", "exit", "vlan 20", "tagged ethe 1/1/16", "exit", "vlan 40", "tagged ethe 1/1/16", "exit", "vlan 50", "tagged ethe 1/1/16", "exit", "vlan 60", "tagged ethe 1/1/16", "exit", "vlan 70", "tagged ethe 1/1/16", "exit", "write memory"},
	"create tagged-only trunk":     []string{"show running-config", "interface ethernet 1/1/22", "exit", "vlan 10", "tagged ethe 1/1/22", "exit", "vlan 20", "tagged ethe 1/1/22", "exit", "vlan 30", "tagged ethe 1/1/22", "exit", "vlan 40", "tagged ethe 1/1/22", "exit", "vlan 50", "tagged ethe 1/1/22", "exit", "vlan 60", "tagged ethe 1/1/22", "exit", "vlan 70", "tagged ethe 1/1/22", "exit", "write memory"},
	"create with untagged 1":       []string{"show running-config", "interface ethernet 1/1/22", "exit", "vlan 1", "untagged ethe 1/1/22", "exit", "vlan 10", "tagged ethe 1/1/22", "exit", "vlan 20", "tagged ethe 1/1/22", "exit", "vlan 30", "tagged ethe 1/1/22", "exit", "vlan 40", "tagged ethe 1/1/22", "exit", "vlan 50", "tagged ethe 1/1/22", "exit", "vlan 60", "tagged ethe 1/1/22", "exit", "vlan 70", "tagged ethe 1/1/22", "exit", "write memory"},
	"explicit to tag_all":          []string{"show running-config", "vlan 10", "tagged ethe 1/1/15", "exit", "write memory"},
	"drifted trunk repair":         []string{"show running-config", "vlan 10", "tagged ethe 1/1/15", "exit", "vlan 60", "tagged ethe 1/1/15", "exit", "vlan 70", "tagged ethe 1/1/15", "exit", "write memory"},
	"tag_all change untagged":      []string{"show running-config", "vlan 20", "no untagged ethe 1/1/15", "exit", "vlan 30", "no tagged ethe 1/1/15", "exit", "vlan 30", "untagged ethe 1/1/15", "exit", "vlan 20", "tagged ethe 1/1/15", "exit", "write memory"},
	"tag_all drop untagged":        []string{"show running-config", "vlan 20", "no untagged ethe 1/1/15", "exit", "vlan 20", "tagged ethe 1/1/15", "exit", "write memory"},
	"tag_all no change":            []string{"show running-config"},
}

type tagAllScenario struct {
	name        string
	fixture     func(*testing.T) string
	state, plan *InterfaceEthernetResourceModel
}

func tagAllScenarios() []tagAllScenario {
	fx := func(t *testing.T) string { return loadFixture(t, "7250-24") }
	return []tagAllScenario{
		{"create trunk with untagged", fx, nil, ethModel("1/1/16", 20, nil, true)},
		{"create trunk no untagged", fx, nil, ethModel("1/1/16", 0, nil, true)},
		{"create trunk moving untagged", fixture15, nil, ethModel("1/1/16", 30, nil, true)},
		{"create tagged-only trunk", fixture15, nil, ethModel("1/1/22", 0, nil, true)},
		{"create with untagged 1", fixture15, nil, ethModel("1/1/22", 1, nil, true)},
		{"explicit to tag_all", fx, ethModel("1/1/15", 20, []int64{30, 40, 50, 60, 70}, false), ethModel("1/1/15", 20, nil, true)},
		{"drifted trunk repair", fixture15, ethModel("1/1/15", 20, []int64{30, 40, 50}, false), ethModel("1/1/15", 20, nil, true)},
		{"tag_all change untagged", fx, ethModel("1/1/15", 20, nil, true), ethModel("1/1/15", 30, nil, true)},
		{"tag_all drop untagged", fixture15, ethModel("1/1/15", 20, nil, true), ethModel("1/1/15", 0, nil, true)},
		{"tag_all no change", fx, ethModel("1/1/15", 20, nil, true), ethModel("1/1/15", 20, nil, true)},
	}
}

// With native_bulk_tagging unset (false), every tag_all_vlans scenario sends
// exactly what main sends — same commands, same order, same config reads, no
// `show arp`, same write memory — even on firmware that has the command and
// with a resolvable management path.
func TestTagAll_DefaultOffIsByteIdenticalToMain(t *testing.T) {
	for _, sc := range tagAllScenarios() {
		want, ok := mainCommandStreams[sc.name]
		if !ok {
			t.Fatalf("no main baseline for %q", sc.name)
		}
		mock, diags := mockApply(t, sc.fixture(t), dialect08095, false, sc.state, sc.plan)
		if diags.HasError() {
			t.Fatalf("%s: %v", sc.name, diags)
		}
		if !reflect.DeepEqual(mock.Commands, want) {
			t.Errorf("%s: flag off diverges from main:\n got %q\nwant %q", sc.name, mock.Commands, want)
		}
	}
}

// Configure threads the provider's opt-in through to the resource.
func TestTagAll_ConfigureThreadsFlag(t *testing.T) {
	r := &InterfaceEthernetResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: providerDataWith(true)}, &resp)
	if resp.Diagnostics.HasError() || !r.nativeBulkTagging {
		t.Fatalf("Configure must thread NativeBulkTagging through, got %v (%v)", r.nativeBulkTagging, resp.Diagnostics)
	}
	r2 := &InterfaceEthernetResource{}
	r2.Configure(context.Background(), resource.ConfigureRequest{ProviderData: providerDataWith(false)}, &resp)
	if r2.nativeBulkTagging {
		t.Fatal("default provider data must leave bulk tagging off")
	}
}

func providerDataWith(bulk bool) *providerdata.ProviderData {
	return &providerdata.ProviderData{Client: sshclient.NewMockClient(""), NativeBulkTagging: bulk}
}

// --- opted in: when bulk is and is not used --------------------------------

// Opted in, on a tagged-only, non-management trunk on 08.0.95: the
// management path is checked (one `show arp`), the bulk command is sent in
// interface context, and the config is re-read once to verify it.
func TestTagAll_OptInBulkOnTaggedOnlyTrunk(t *testing.T) {
	mock, diags := mockApply(t, loadFixture(t, "7250-24"), dialect08095, true, nil, ethModel("1/1/22", 0, nil, true))
	if diags.HasError() {
		t.Fatal(diags)
	}
	want := []string{
		"show running-config",
		"interface ethernet 1/1/22", "exit",
		"show arp",
		"interface ethernet 1/1/22", bulkCmd, "exit",
		"show running-config",
		"write memory",
	}
	if !reflect.DeepEqual(mock.Commands, want) {
		t.Errorf("commands:\n got %q\nwant %q", mock.Commands, want)
	}
}

// The provider's dialect may be unknown while the config's own `ver` line is
// parseable: that is evidence enough.
func TestTagAll_OptInBulkFromConfigVersion(t *testing.T) {
	mock, diags := mockApply(t, loadFixture(t, "7250-24"), dialect.Dialect{}, true, nil, ethModel("1/1/22", 0, nil, true))
	if diags.HasError() {
		t.Fatal(diags)
	}
	if countCommand(mock.Commands, bulkCmd) != 1 {
		t.Errorf("expected %q with an 08.0.95 ver line, got %q", bulkCmd, mock.Commands)
	}
}

// The explicit-list -> tag_all_vlans transition (the shape a drift repair
// takes) on a tagged-only trunk adds with the bulk command and removes nothing.
func TestTagAll_OptInUpdateExplicitToTagAllUsesBulk(t *testing.T) {
	mock, diags := mockApply(t, loadFixture(t, "7250-24"), dialect08095, true,
		ethModel("1/1/22", 0, []int64{20}, false), ethModel("1/1/22", 0, nil, true))
	if diags.HasError() {
		t.Fatal(diags)
	}
	if got, want := cliOf(mock), []string{"show arp", "interface ethernet 1/1/22", bulkCmd, "exit"}; !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if mock.WriteMemoryCalls != 1 {
		t.Errorf("write memory calls = %d, want 1", mock.WriteMemoryCalls)
	}
}

// Even opted in, the bulk command is never used — and the stream is exactly
// the flag-off stream, apart from a `show arp` where the management path had
// to be checked — when the port has an untagged VLAN (in the plan or on the
// device), is the management path or the path cannot be determined, the
// firmware lacks the command or is unknown, or the membership is an explicit
// list.
func TestTagAll_OptInNeverBulk(t *testing.T) {
	fx := loadFixture(t, "7250-24")
	tests := []struct {
		name    string
		cfg     string
		d       dialect.Dialect
		arp     string // "" = no `show arp` answer
		state   *InterfaceEthernetResourceModel
		plan    *InterfaceEthernetResourceModel
		wantArp bool
	}{
		{name: "plan untagged_vlan", cfg: fx, d: dialect08095, arp: arpVia("1/2/1"), plan: ethModel("1/1/16", 20, nil, true)},
		{name: "plan untagged_vlan 1", cfg: fx, d: dialect08095, arp: arpVia("1/2/1"), plan: ethModel("1/1/22", 1, nil, true)},
		{name: "plan untagged_vlan on update", cfg: fixture15(t), d: dialect08095, arp: arpVia("1/2/1"),
			state: ethModel("1/1/15", 20, nil, true), plan: ethModel("1/1/15", 30, nil, true)},
		// The plan omits untagged_vlan, but the device has 1/1/16 untagged in 20.
		{name: "device untagged", cfg: fx, d: dialect08095, arp: arpVia("1/2/1"), plan: ethModel("1/1/16", 0, nil, true)},
		// The 7150-24 uplink shape: untagged in management VLAN 20, tagged in the rest.
		{name: "management uplink 1/2/1", cfg: fx, d: dialect08095, arp: arpVia("1/2/1"),
			state: ethModel("1/2/1", 20, []int64{10, 30, 40}, false), plan: ethModel("1/2/1", 20, nil, true)},
		{name: "tagged-only port is the management path", cfg: fx, d: dialect08095, arp: arpVia("1/1/22"),
			plan: ethModel("1/1/22", 0, nil, true), wantArp: true},
		{name: "management path undeterminable", cfg: fx, d: dialect08095, arp: "",
			plan: ethModel("1/1/22", 0, nil, true), wantArp: true},
		{name: "unknown firmware", cfg: stripDialectEvidence(fx), d: dialect.Dialect{}, arp: arpVia("1/2/1"),
			plan: ethModel("1/1/22", 0, nil, true)},
		{name: "08.0.30 dual-mode dialect", cfg: loadFixture(t, "6430-c12"), d: dialect08030, arp: arpVia("1/2/1"),
			plan: ethModel("1/1/7", 0, nil, true)},
		{name: "explicit tagged_vlans", cfg: fx, d: dialect08095, arp: arpVia("1/2/1"),
			plan: ethModel("1/1/22", 0, []int64{10, 30}, false)},
	}
	for _, tt := range tests {
		run := func(bulk bool) *sshclient.MockClient {
			mock := sshclient.NewMockClient(tt.cfg)
			if tt.arp != "" {
				mock.Responses["show arp"] = tt.arp
			}
			diags, _ := applyEth(t, mock, tt.d, bulk, tt.state, tt.plan)
			if diags.HasError() {
				t.Fatalf("%s (bulk=%v): %v", tt.name, bulk, diags)
			}
			return mock
		}
		on, off := run(true), run(false)
		if countCommand(on.Commands, bulkCmd) != 0 {
			t.Errorf("%s: bulk command sent: %q", tt.name, on.Commands)
		}
		if got := countCommand(on.Commands, "show arp"); (got == 1) != tt.wantArp || got > 1 {
			t.Errorf("%s: show arp sent %d times, want it only when every other check allowed bulk (%v)", tt.name, got, tt.wantArp)
		}
		if got, want := without(on.Commands, "show arp"), off.Commands; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: opted-in stream differs from the per-VLAN stream:\n got %q\nwant %q", tt.name, got, want)
		}
	}
}

// A tag_all_vlans trunk with nothing to add sends nothing — not even `show arp`.
func TestTagAll_OptInNoChangeSendsNothing(t *testing.T) {
	mock, diags := mockApply(t, loadFixture(t, "7250-24"), dialect08095, true,
		ethModel("1/1/20", 0, nil, true), ethModel("1/1/20", 0, nil, true))
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !reflect.DeepEqual(mock.Commands, []string{"show running-config"}) {
		t.Errorf("expected a single config read, got %q", mock.Commands)
	}
}

// --- verification and failure handling -------------------------------------

// If the re-read shows the bulk command tagged VLAN 1 — undocumented, and
// still unverified on hardware — that tag is removed again, best-effort.
func TestTagAll_OptInBulkCorrectsDefaultVLAN(t *testing.T) {
	fx := loadFixture(t, "7250-24")
	mock := sshclient.NewMockClient(fx)
	mock.Responses["show arp"] = arpVia("1/2/1")
	mock.RunningConfigQueue = []string{fx, strings.Replace(fx,
		"vlan 1 name DEFAULT-VLAN by port\n", "vlan 1 name DEFAULT-VLAN by port\n tagged ethe 1/1/22\n", 1)}
	diags, _ := applyEth(t, mock, dialect08095, true, nil, ethModel("1/1/22", 0, nil, true))
	if diags.HasError() {
		t.Fatal(diags)
	}
	want := []string{
		"interface ethernet 1/1/22", "exit",
		"show arp",
		"interface ethernet 1/1/22", bulkCmd, "exit",
		"vlan 1", "no tagged ethe 1/1/22", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if mock.WriteMemoryCalls != 1 {
		t.Errorf("write memory calls = %d, want 1", mock.WriteMemoryCalls)
	}
}

func TestBulkTaggingCorrections(t *testing.T) {
	fx := loadFixture(t, "7250-24")
	sideEffects := strings.Replace(fx, "vlan 1 name DEFAULT-VLAN by port\n", "vlan 1 name DEFAULT-VLAN by port\n tagged ethe 1/1/16\n", 1)
	sideEffects = strings.Replace(sideEffects, " untagged ethe 1/1/1 to 1/1/18 ethe 1/1/24 ethe 1/2/1 ethe 1/2/4 to 1/2/6\n",
		" tagged ethe 1/1/16\n untagged ethe 1/1/1 to 1/1/15 ethe 1/1/17 to 1/1/18 ethe 1/1/24 ethe 1/2/1 ethe 1/2/4 to 1/2/6\n", 1)
	cfg, err := parser.ParseRunningConfig(sideEffects)
	if err != nil {
		t.Fatal(err)
	}
	if got := bulkTaggingCorrections(cfg, "1/1/16", 0); !reflect.DeepEqual(got, []string{"vlan 1", "no tagged ethe 1/1/16", "exit"}) {
		t.Errorf("no untagged VLAN: got %q", got)
	}
	want := []string{"vlan 1", "no tagged ethe 1/1/16", "exit", "vlan 20", "no tagged ethe 1/1/16", "untagged ethe 1/1/16", "exit"}
	if got := bulkTaggingCorrections(cfg, "1/1/16", 20); !reflect.DeepEqual(got, want) {
		t.Errorf("untagged converted: got %q, want %q", got, want)
	}
	healthy, err := parser.ParseRunningConfig(fx)
	if err != nil {
		t.Fatal(err)
	}
	if got := bulkTaggingCorrections(healthy, "1/1/22", 0); len(got) != 0 {
		t.Errorf("healthy port: got %q, want none", got)
	}
}

const bulkRefusal = "error - IP routing, vrf, ip policy, route-only, rpf-mode, ip-mac or ip tcp mss was configured on ports  1/1/22"

// A bulk command that errors (the switch's lower-case `error - ...`) may have
// changed some membership already. Create still verifies, saves what
// succeeded, and reports the error — but records no state, because an errored
// Create with state is tainted and the replace that follows would strip the
// trunk (gotcha #9).
func TestTagAll_OptInBulkErrorOnCreateSavesAndRecordsNoState(t *testing.T) {
	mock := sshclient.NewMockClient(loadFixture(t, "7250-24"))
	mock.Responses["show arp"] = arpVia("1/2/1")
	mock.ErrorOnCommand[bulkCmd] = errors.New(bulkRefusal)
	diags, recorded := applyEth(t, mock, dialect08095, true, nil, ethModel("1/1/22", 0, nil, true))
	if !diags.HasError() {
		t.Fatal("expected Create to report the bulk error")
	}
	if !strings.Contains(fmt.Sprint(diags), "write memory") {
		t.Errorf("error must say the partial change was saved: %v", diags)
	}
	if recorded != nil {
		t.Errorf("an errored Create must not record state (it would be tainted), got %+v", recorded)
	}
	if got := mock.Commands[len(mock.Commands)-2:]; !reflect.DeepEqual(got, []string{"show running-config", "write memory"}) {
		t.Errorf("after the error the config must be re-read and saved, got %q", mock.Commands)
	}
}

// --- convergence against a simulated switch --------------------------------

// simSwitch is a minimal model of FastIron VLAN membership that implements
// sshclient.CommandExecutor, so the resource runs against it end to end: its
// verification re-read sees the effect of the commands it sent. It rejects
// sequences FastIron rejects (a port tagged and untagged in one VLAN,
// removing a membership that is not there).
//
// VLAN 1 is modelled honestly: a port that is not an explicit untagged member
// of any VLAN is an implicit untagged member of DEFAULT-VLAN 1 (which the
// running config does not print), and tagging it in VLAN 1 takes that native
// membership away until the tag is removed. The bulk* fields parameterise
// what `vlan-config add all-tagged` is not documented to do: also tag VLAN 1,
// also convert the port's untagged VLAN to tagged, or fail part-way (tag the
// first failAfter VLANs, then print `error - ...`).
type simSwitch struct {
	vlans            map[int]*simVLAN
	inVLAN           int
	inIface          string
	version          string
	arp              string
	bulkTagsDefault  bool
	bulkTagsUntagged bool
	failAfter        int // < 0: never fails
	commands         []string
	saved            string // rendered config at the last write memory
	t                *testing.T
}

var _ sshclient.CommandExecutor = (*simSwitch)(nil)

type simVLAN struct{ tagged, untagged map[string]bool }

func newSimSwitch(t *testing.T, cfg *parser.RunningConfig) *simSwitch {
	s := &simSwitch{vlans: map[int]*simVLAN{}, version: "08.0.95mT213", arp: arpVia("1/2/1"), failAfter: -1, t: t}
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
	s.saved = s.render()
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

func (s *simSwitch) explicitlyUntagged(port string) bool {
	for _, v := range s.vlans {
		if v.untagged[port] {
			return true
		}
	}
	return false
}

func (s *simSwitch) Execute(c string) (string, error) {
	s.commands = append(s.commands, c)
	if c == "show arp" {
		return s.arp, nil
	}
	return "", nil
}

func (s *simSwitch) ExecuteInConfigMode(cmds []string) error {
	t := s.t
	t.Helper()
	for _, c := range cmds {
		s.commands = append(s.commands, c)
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
			v := s.ctxVLAN(c)
			if v.untagged[f[2]] {
				t.Fatalf("%q: port is untagged in vlan %d", c, s.inVLAN)
			}
			v.tagged[f[2]] = true
		case strings.HasPrefix(c, "no tagged ethe "):
			v := s.ctxVLAN(c)
			if !v.tagged[f[3]] {
				t.Fatalf("%q: port is not tagged in vlan %d", c, s.inVLAN)
			}
			delete(v.tagged, f[3])
		case strings.HasPrefix(c, "untagged ethe "):
			v := s.ctxVLAN(c)
			if v.tagged[f[2]] {
				t.Fatalf("%q: port is tagged in vlan %d", c, s.inVLAN)
			}
			for _, o := range s.vlans {
				delete(o.untagged, f[2]) // FastIron moves the untagged membership
			}
			v.untagged[f[2]] = true
		case strings.HasPrefix(c, "no untagged ethe "):
			v := s.ctxVLAN(c)
			if !v.untagged[f[3]] {
				t.Fatalf("%q: port is not untagged in vlan %d", c, s.inVLAN)
			}
			delete(v.untagged, f[3])
		case c == bulkCmd:
			if s.inIface == "" {
				t.Fatalf("%q outside interface context", c)
			}
			done := 0
			for _, id := range s.vlanIDs() {
				v := s.vlans[id]
				if id == 1 && !s.bulkTagsDefault {
					continue
				}
				if v.tagged[s.inIface] {
					continue
				}
				if s.failAfter >= 0 && done == s.failAfter {
					return fmt.Errorf("command %q: %s", c, bulkRefusal)
				}
				if v.untagged[s.inIface] {
					if !s.bulkTagsUntagged {
						continue
					}
					delete(v.untagged, s.inIface)
				}
				v.tagged[s.inIface] = true
				done++
			}
		default:
			t.Fatalf("simSwitch: unknown command %q", c)
		}
	}
	return nil
}

func (s *simSwitch) ctxVLAN(c string) *simVLAN {
	s.t.Helper()
	if s.inVLAN == 0 {
		s.t.Fatalf("%q outside VLAN context", c)
	}
	return s.vlans[s.inVLAN]
}

func (s *simSwitch) ExecutePromptChanging(c string) error {
	s.t.Fatalf("unexpected prompt-changing command %q", c)
	return nil
}

func (s *simSwitch) ExecuteExpectingDisconnect(cmds []string) error {
	s.t.Fatalf("unexpected disconnecting commands %q", cmds)
	return nil
}

func (s *simSwitch) WriteMemory() error {
	s.commands = append(s.commands, "write memory")
	s.saved = s.render()
	return nil
}

func (s *simSwitch) GetRunningConfig() (string, error) {
	s.commands = append(s.commands, "show running-config")
	return s.render(), nil
}

func (s *simSwitch) Close() error { return nil }

// membership reports port's membership as vlan -> "tagged"/"untagged",
// including the implicit untagged membership of DEFAULT-VLAN 1.
func (s *simSwitch) membership(port string) map[int]string {
	m := map[int]string{}
	for id, v := range s.vlans {
		if v.tagged[port] {
			m[id] = "tagged"
		}
		if v.untagged[port] {
			m[id] = "untagged"
		}
	}
	if !s.explicitlyUntagged(port) && m[1] == "" {
		m[1] = "untagged" // implicit DEFAULT-VLAN membership
	}
	return m
}

// render writes the simulated device out as running-config text the way
// FastIron prints it (implicit VLAN 1 membership is not printed).
func (s *simSwitch) render() string {
	var b strings.Builder
	b.WriteString("Current configuration:\n!\n")
	if s.version != "" {
		fmt.Fprintf(&b, "ver %s\n!\n", s.version)
	}
	for _, id := range s.vlanIDs() {
		v := s.vlans[id]
		if id == 1 {
			b.WriteString("vlan 1 name DEFAULT-VLAN by port\n")
		} else {
			fmt.Fprintf(&b, "vlan %d by port\n", id)
		}
		writePorts := func(kind string, set map[string]bool) {
			if len(set) == 0 || (id == 1 && kind == "untagged") {
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
	b.WriteString("ip route 0.0.0.0/0 10.0.20.1\n!\nend\n")
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

func (s *simSwitch) cli() []string {
	var out []string
	for _, c := range s.commands {
		if c != "show running-config" && c != "write memory" && c != "show arp" {
			out = append(out, c)
		}
	}
	return out
}

// For every tag_all_vlans transition the provider generates, running the
// resource against the simulated switch with bulk tagging off and opted in —
// under every reading of what the bulk command does to VLAN 1 and the untagged
// VLAN — leaves identical membership, exactly the expansion tag_all_vlans
// stands for (plus the port's native VLAN). Where bulk is not allowed the
// opted-in stream is the per-VLAN stream. Every apply leaves the saved config
// equal to the running config.
//
// The erroring reading (the bulk command tags a couple of VLANs, then prints
// `error - ...`) does not claim equality after the failing apply. It claims
// instead that the partial change is saved, that an Update records the device
// truth in state, and that the follow-up apply (with native_bulk_tagging off,
// the documented remedy) converges on the same membership as everything else.
func TestTagAllVLANs_BulkAndExpansionConverge(t *testing.T) {
	seed, err := parser.ParseRunningConfig(fixture15(t))
	if err != nil {
		t.Fatal(err)
	}
	type scenario struct {
		name, port  string
		state, plan *InterfaceEthernetResourceModel
		bulk        bool // whether the opted-in apply is expected to use the bulk command
	}
	scenarios := []scenario{
		{"create trunk with untagged", "1/1/16", nil, ethModel("1/1/16", 20, nil, true), false},
		{"create trunk moving untagged", "1/1/16", nil, ethModel("1/1/16", 30, nil, true), false},
		// "create trunk no untagged" on 1/1/16 is left out: the port is
		// untagged in 20 on the device and Create does not reconcile
		// membership it was never told about, so main's stream tags it in 20
		// on top of that, which FastIron refuses (pre-existing behaviour).
		{"create tagged-only trunk", "1/1/22", nil, ethModel("1/1/22", 0, nil, true), true},
		{"create with untagged 1", "1/1/22", nil, ethModel("1/1/22", 1, nil, true), false},
		{"explicit to tag_all", "1/1/15", ethModel("1/1/15", 20, []int64{30, 40, 50, 60, 70}, false), ethModel("1/1/15", 20, nil, true), false},
		{"drifted trunk repair", "1/1/15", ethModel("1/1/15", 20, []int64{30, 40, 50}, false), ethModel("1/1/15", 20, nil, true), false},
		{"tagged-only trunk repair", "1/1/22", ethModel("1/1/22", 0, []int64{20}, false), ethModel("1/1/22", 0, nil, true), true},
		{"management uplink repair", "1/2/1", ethModel("1/2/1", 20, []int64{10, 30, 40}, false), ethModel("1/2/1", 20, nil, true), false},
		{"tag_all change untagged", "1/1/15", ethModel("1/1/15", 20, nil, true), ethModel("1/1/15", 30, nil, true), false},
		{"tag_all drop untagged", "1/1/15", ethModel("1/1/15", 20, nil, true), ethModel("1/1/15", 0, nil, true), false},
	}
	readings := []struct {
		name                      string
		tagsDefault, tagsUntagged bool
		failAfter                 int
	}{
		{"skips VLAN 1 and untagged VLAN", false, false, -1},
		{"tags VLAN 1 too", true, false, -1},
		{"tags the untagged VLAN too", false, true, -1},
		{"tags both", true, true, -1},
		{"errors after two VLANs", false, false, 2},
		{"tags VLAN 1 then errors", true, false, 2},
	}

	for _, sc := range scenarios {
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
		} else {
			want[1] = "untagged" // explicit (untagged_vlan = 1) or native DEFAULT-VLAN
		}

		off := newSimSwitch(t, seed)
		if diags, _ := applyEth(t, off, dialect08095, false, sc.state, sc.plan); diags.HasError() {
			t.Fatalf("%s (off): %v", sc.name, diags)
		}
		if countCommand(off.commands, bulkCmd) != 0 || countCommand(off.commands, "show arp") != 0 {
			t.Fatalf("%s: flag off must send neither the bulk command nor show arp: %q", sc.name, off.commands)
		}
		if got := off.membership(sc.port); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: per-VLAN membership %v, want %v", sc.name, got, want)
		}
		if off.saved != off.render() {
			t.Errorf("%s: per-VLAN apply left running and saved configs divergent", sc.name)
		}

		for _, rd := range readings {
			label := sc.name + " / bulk " + rd.name
			sim := newSimSwitch(t, seed)
			sim.bulkTagsDefault, sim.bulkTagsUntagged, sim.failAfter = rd.tagsDefault, rd.tagsUntagged, rd.failAfter
			diags, recorded := applyEth(t, sim, dialect08095, true, sc.state, sc.plan)

			if got := countCommand(sim.commands, bulkCmd); (got == 1) != sc.bulk {
				t.Fatalf("%s: bulk command sent %d times, want bulk=%v: %q", label, got, sc.bulk, sim.commands)
			}
			if !sc.bulk {
				if diags.HasError() {
					t.Fatalf("%s: %v", label, diags)
				}
				if !reflect.DeepEqual(sim.cli(), off.cli()) {
					t.Errorf("%s: opted-in stream differs from the per-VLAN stream:\n got %q\nwant %q", label, sim.cli(), off.cli())
				}
			}
			if sim.saved != sim.render() {
				t.Errorf("%s: running and saved configs diverge after the apply", label)
			}

			if sc.bulk && rd.failAfter >= 0 {
				if !diags.HasError() {
					t.Fatalf("%s: expected the bulk error to be reported", label)
				}
				// The follow-up apply: what Terraform does next with the
				// recorded state, with native_bulk_tagging turned off.
				next := sc.state
				if sc.state != nil {
					if recorded == nil || recorded.TagAllVLANs.ValueBool() {
						t.Fatalf("%s: an errored Update must record the device membership with tag_all_vlans = false, got %+v", label, recorded)
					}
					got := map[int]bool{}
					for _, id := range setToInt64Slice(context.Background(), recorded.TaggedVLANs, &diags) {
						got[int(id)] = true
					}
					for id, kind := range sim.membership(sc.port) {
						if kind == "tagged" && id != 1 && !got[id] {
							t.Errorf("%s: recorded tagged_vlans %v misses device VLAN %d", label, got, id)
						}
					}
					next = recorded
				} else if recorded != nil {
					t.Fatalf("%s: an errored Create must not record (tainted) state", label)
				}
				if d2, _ := applyEth(t, sim, dialect08095, false, next, sc.plan); d2.HasError() {
					t.Fatalf("%s: follow-up apply: %v", label, d2)
				}
				if sim.saved != sim.render() {
					t.Errorf("%s: running and saved configs diverge after the follow-up apply", label)
				}
			} else if diags.HasError() {
				t.Fatalf("%s: %v", label, diags)
			}

			if got := sim.membership(sc.port); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: membership %v, want %v (commands %q)", label, got, want, sim.cli())
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
