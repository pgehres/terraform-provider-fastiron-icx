package resource

import (
	"context"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// These tests exercise icx_interface_ethernet against the real sanitized
// fixtures: the two FastIron 08.0.30 captures (ICX 6430-C12 switch image,
// ICX 6450-C12PD router image) that use the `dual-mode` interface dialect for
// trunk-untagged membership, and the 08.0.95 ICX 7250-24 capture that must
// keep its VLAN-context-only behavior byte for byte.

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../testdata/config." + name + ".txt")
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

func newEthResource(t *testing.T, fixture string) (*InterfaceEthernetResource, *sshclient.MockClient) {
	t.Helper()
	mock := sshclient.NewMockClient(loadFixture(t, fixture))
	return &InterfaceEthernetResource{client: mock}, mock
}

// ethModel builds a resource model. tagged == nil means a null set; untagged
// == 0 means null.
func ethModel(port string, untagged int64, tagged []int64, tagAll bool) *InterfaceEthernetResourceModel {
	m := &InterfaceEthernetResourceModel{
		ID:                   types.StringValue(port),
		Port:                 types.StringValue(port),
		PortName:             types.StringNull(),
		SpanningTreePt2PtMac: types.BoolValue(false),
		OpticalMonitor:       types.BoolValue(true),
		UntaggedVLAN:         types.Int64Null(),
		TaggedVLANs:          types.SetNull(types.Int64Type),
		TagAllVLANs:          types.BoolValue(tagAll),
		RawConfig:            types.ListNull(types.StringType),
	}
	if untagged != 0 {
		m.UntaggedVLAN = types.Int64Value(untagged)
	}
	if tagged != nil {
		set, diags := types.SetValueFrom(context.Background(), types.Int64Type, tagged)
		if diags.HasError() {
			panic(diags)
		}
		m.TaggedVLANs = set
	}
	return m
}

func ethStateAndPlan(t *testing.T, r *InterfaceEthernetResource, state, plan *InterfaceEthernetResourceModel) (tfsdk.State, tfsdk.Plan) {
	t.Helper()
	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema error: %v", schemaResp.Diagnostics)
	}
	s := tfsdk.State{Schema: schemaResp.Schema}
	if state != nil {
		if diags := s.Set(ctx, state); diags.HasError() {
			t.Fatalf("failed to build state: %v", diags)
		}
	}
	p := tfsdk.Plan{Schema: schemaResp.Schema}
	if plan != nil {
		if diags := p.Set(ctx, plan); diags.HasError() {
			t.Fatalf("failed to build plan: %v", diags)
		}
	}
	return s, p
}

func readEth(t *testing.T, fixture, port string, tagAll bool) InterfaceEthernetResourceModel {
	t.Helper()
	ctx := context.Background()
	r, _ := newEthResource(t, fixture)
	state, _ := ethStateAndPlan(t, r, ethModel(port, 0, nil, tagAll), nil)

	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read error: %v", resp.Diagnostics)
	}
	var got InterfaceEthernetResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("state get: %v", diags)
	}
	return got
}

func taggedOf(t *testing.T, m InterfaceEthernetResourceModel) []int64 {
	t.Helper()
	if m.TaggedVLANs.IsNull() {
		return nil
	}
	var out []int64
	if diags := m.TaggedVLANs.ElementsAs(context.Background(), &out, false); diags.HasError() {
		t.Fatalf("tagged elements: %v", diags)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Commands recorded by the mock, minus the running-config reads and the final
// write memory, so tests can assert on the exact CLI sequence.
func cliOf(mock *sshclient.MockClient) []string {
	var out []string
	for _, c := range mock.Commands {
		if c == "show running-config" || c == "write memory" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func indexOf(cmds []string, want string) int {
	for i, c := range cmds {
		if c == want {
			return i
		}
	}
	return -1
}

// --- Read -----------------------------------------------------------------

func TestInterfaceEthernetRead_DualModeTrunk(t *testing.T) {
	tests := []struct {
		name, fixture, port string
		wantUntagged        int64
		wantTagged          []int64
	}{
		{
			// 6430: `dual-mode  102` under 1/2/1; VLAN 102 lists it tagged only.
			name: "6430 uplink 1/2/1", fixture: "6430-c12", port: "1/2/1",
			wantUntagged: 102, wantTagged: []int64{101, 107, 109, 110, 120, 200},
		},
		{
			name: "6430 1/2/2 dual-mode 109", fixture: "6430-c12", port: "1/2/2",
			wantUntagged: 109, wantTagged: []int64{101, 107, 110, 120, 200},
		},
		{
			// 6450: AP port tagged in 7 VLANs with dual-mode 102.
			name: "6450 ap-1 1/1/1", fixture: "6450-c12pd", port: "1/1/1",
			wantUntagged: 102, wantTagged: []int64{101, 107, 109, 110, 120, 200},
		},
		{
			name: "6450 1/1/12", fixture: "6450-c12pd", port: "1/1/12",
			wantUntagged: 102, wantTagged: []int64{101, 103, 107, 109, 110, 120, 200},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readEth(t, tt.fixture, tt.port, false)
			if got.UntaggedVLAN.IsNull() || got.UntaggedVLAN.ValueInt64() != tt.wantUntagged {
				t.Errorf("untagged_vlan = %v, want %d", got.UntaggedVLAN, tt.wantUntagged)
			}
			if tagged := taggedOf(t, got); !reflect.DeepEqual(tagged, tt.wantTagged) {
				t.Errorf("tagged_vlans = %v, want %v (dual-mode VLAN must be excluded)", tagged, tt.wantTagged)
			}
		})
	}
}

// Ports without a dual-mode line are read from VLAN context as before: access
// ports on 08.0.30, a tagged-only trunk on 08.0.30, and everything on 08.0.95.
func TestInterfaceEthernetRead_NonDualModePortsUnchanged(t *testing.T) {
	tests := []struct {
		name, fixture, port string
		wantUntagged        int64 // 0 = null
		wantTagged          []int64
	}{
		{name: "6450 access camera-1 1/1/3", fixture: "6450-c12pd", port: "1/1/3", wantUntagged: 102},
		{name: "6430 access 1/1/1", fixture: "6430-c12", port: "1/1/1", wantUntagged: 109},
		{name: "6430 unconfigured 1/1/7", fixture: "6430-c12", port: "1/1/7"},
		{
			name: "6450 tagged-only trunk-1 1/1/5", fixture: "6450-c12pd", port: "1/1/5",
			wantTagged: []int64{101, 102, 107, 109, 110, 120, 200},
		},
		{
			// 08.0.95: untagged comes from VLAN context, no dual-mode anywhere.
			name: "7250 trunk 1/2/1", fixture: "7250-24", port: "1/2/1",
			wantUntagged: 20, wantTagged: []int64{10, 30, 40, 50, 60, 70},
		},
		{name: "7250 access 1/1/1", fixture: "7250-24", port: "1/1/1", wantUntagged: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readEth(t, tt.fixture, tt.port, false)
			if tt.wantUntagged == 0 {
				if !got.UntaggedVLAN.IsNull() {
					t.Errorf("untagged_vlan = %v, want null", got.UntaggedVLAN)
				}
			} else if got.UntaggedVLAN.IsNull() || got.UntaggedVLAN.ValueInt64() != tt.wantUntagged {
				t.Errorf("untagged_vlan = %v, want %d", got.UntaggedVLAN, tt.wantUntagged)
			}
			if tagged := taggedOf(t, got); !reflect.DeepEqual(tagged, tt.wantTagged) {
				t.Errorf("tagged_vlans = %v, want %v", tagged, tt.wantTagged)
			}
		})
	}
}

// A tag_all_vlans trunk on dual-mode firmware: the dual-mode VLAN is the
// untagged VLAN, excluded from the expansion, so a full trunk reads clean —
// tagged_vlans untouched and tag_all_vlans intact.
func TestInterfaceEthernetRead_DualModeWithTagAll(t *testing.T) {
	// 6450 1/1/12: dual-mode 102, tagged in every other VLAN.
	got := readEth(t, "6450-c12pd", "1/1/12", true)
	if got.UntaggedVLAN.ValueInt64() != 102 {
		t.Errorf("untagged_vlan = %v, want 102", got.UntaggedVLAN)
	}
	if !got.TaggedVLANs.IsNull() {
		t.Errorf("tagged_vlans must be left alone on a full tag_all_vlans trunk, got %v", got.TaggedVLANs)
	}
	if !got.TagAllVLANs.ValueBool() {
		t.Error("tag_all_vlans must survive Read on a full trunk")
	}
}

// --- Create ---------------------------------------------------------------

func createEth(t *testing.T, fixture string, plan *InterfaceEthernetResourceModel) *sshclient.MockClient {
	t.Helper()
	r, mock := newEthResource(t, fixture)
	_, p := ethStateAndPlan(t, r, nil, plan)
	state, _ := ethStateAndPlan(t, r, nil, nil)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create error: %v", resp.Diagnostics)
	}
	return mock
}

func TestInterfaceEthernetCreate_DualModeTrunk(t *testing.T) {
	// Fresh port on the 6430, untagged 102 + tagged 101/110: tagged in all
	// three (102 included), then dual-mode 102 — and never `untagged ethe`.
	mock := createEth(t, "6430-c12", ethModel("1/1/7", 102, []int64{110, 101}, false))
	want := []string{
		"interface ethernet 1/1/7", "exit",
		"vlan 101", "tagged ethe 1/1/7", "exit",
		"vlan 102", "tagged ethe 1/1/7", "exit",
		"vlan 110", "tagged ethe 1/1/7", "exit",
		"interface ethernet 1/1/7", "dual-mode 102", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if mock.WriteMemoryCalls != 1 {
		t.Errorf("write memory calls = %d", mock.WriteMemoryCalls)
	}
}

func TestInterfaceEthernetCreate_DualModeDefaultVLANIsBare(t *testing.T) {
	// untagged_vlan = 1 on a trunk: bare `dual-mode`, no `vlan 1 / tagged`.
	mock := createEth(t, "6450-c12pd", ethModel("1/1/9", 1, []int64{101}, false))
	want := []string{
		"interface ethernet 1/1/9", "exit",
		"vlan 101", "tagged ethe 1/1/9", "exit",
		"interface ethernet 1/1/9", "dual-mode", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

func TestInterfaceEthernetCreate_DualModeAccessPortUsesUntagged(t *testing.T) {
	// Access port (untagged only) on 08.0.30 uses plain VLAN-context untagged.
	mock := createEth(t, "6430-c12", ethModel("1/1/7", 103, nil, false))
	want := []string{
		"interface ethernet 1/1/7", "exit",
		"vlan 103", "untagged ethe 1/1/7", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if mock.HasCommandContaining("dual-mode") {
		t.Errorf("access port must not get dual-mode: %v", mock.Commands)
	}
}

func TestInterfaceEthernetCreate_DualModeTaggedOnlyTrunk(t *testing.T) {
	mock := createEth(t, "6430-c12", ethModel("1/1/7", 0, []int64{101, 110}, false))
	if mock.HasCommandContaining("dual-mode") || mock.HasCommandContaining("untagged") {
		t.Errorf("tagged-only trunk must not get dual-mode or untagged: %v", mock.Commands)
	}
	if !mock.HasCommand("vlan 101") || !mock.HasCommand("vlan 110") || !mock.HasCommand("tagged ethe 1/1/7") {
		t.Errorf("expected tagged membership, got %v", mock.Commands)
	}
}

func TestInterfaceEthernetCreate_DualModeTagAllIncludesUntaggedVLAN(t *testing.T) {
	// tag_all_vlans excludes the untagged VLAN from the expansion; on dual-mode
	// firmware the untagged VLAN must still be tagged in VLAN context.
	mock := createEth(t, "6430-c12", ethModel("1/1/7", 102, nil, true))
	cmds := cliOf(mock)
	var taggedIn []string
	for i, c := range cmds {
		if c == "tagged ethe 1/1/7" {
			taggedIn = append(taggedIn, cmds[i-1])
		}
	}
	want := []string{"vlan 101", "vlan 102", "vlan 103", "vlan 107", "vlan 109", "vlan 110", "vlan 120", "vlan 200"}
	if !reflect.DeepEqual(taggedIn, want) {
		t.Errorf("tagged in %v, want %v", taggedIn, want)
	}
	if cmds[len(cmds)-2] != "dual-mode 102" {
		t.Errorf("expected trailing dual-mode 102, got %q", cmds)
	}
}

// 08.0.95 must be untouched: untagged goes through VLAN context and the word
// dual-mode never appears.
func TestInterfaceEthernetCreate_LegacyDialectNotEmittedOn08095(t *testing.T) {
	mock := createEth(t, "7250-24", ethModel("1/1/16", 20, []int64{30, 10}, false))
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

// --- Update ---------------------------------------------------------------

func updateEth(t *testing.T, fixture string, state, plan *InterfaceEthernetResourceModel) *sshclient.MockClient {
	t.Helper()
	r, mock := newEthResource(t, fixture)
	s, p := ethStateAndPlan(t, r, state, plan)
	resp := resource.UpdateResponse{State: s}
	r.Update(context.Background(), resource.UpdateRequest{Plan: p, State: s}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Update error: %v", resp.Diagnostics)
	}
	return mock
}

// 1/2/2 on the 6430 is dual-mode 109, tagged 101/107/110/120/200. Moving the
// untagged VLAN to 101: re-issue dual-mode directly (no `no dual-mode`
// window), then drop the now-unwanted tagged 109.
func TestInterfaceEthernetUpdate_DualModeChangeUntaggedVLAN(t *testing.T) {
	trunk := []int64{101, 107, 110, 120, 200}
	mock := updateEth(t, "6430-c12",
		ethModel("1/2/2", 109, trunk, false),
		ethModel("1/2/2", 101, trunk, false))
	want := []string{
		"interface ethernet 1/2/2", "dual-mode 101", "exit",
		"vlan 109", "no tagged ethe 1/2/2", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// Same port, only the tagged list changes: dual-mode is left alone and the
// dual-mode VLAN's tagged membership is never removed.
func TestInterfaceEthernetUpdate_DualModeTaggedOnlyChange(t *testing.T) {
	mock := updateEth(t, "6430-c12",
		ethModel("1/2/2", 109, []int64{101, 107, 110, 120, 200}, false),
		ethModel("1/2/2", 109, []int64{101, 107, 110, 120, 200, 103}, false))
	want := []string{"vlan 103", "tagged ethe 1/2/2", "exit"}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// Trunk → access: leave dual-mode first, strip all tagged membership
// (including the old dual-mode VLAN), then add the VLAN-context untagged.
func TestInterfaceEthernetUpdate_DualModeTrunkToAccess(t *testing.T) {
	mock := updateEth(t, "6430-c12",
		ethModel("1/2/2", 109, []int64{101, 107, 110, 120, 200}, false),
		ethModel("1/2/2", 109, nil, false))
	cmds := cliOf(mock)
	if cmds[0] != "interface ethernet 1/2/2" || cmds[1] != "no dual-mode" {
		t.Fatalf("expected leading no dual-mode, got %q", cmds)
	}
	noTagged := indexOf(cmds, "no tagged ethe 1/2/2")
	untagged := indexOf(cmds, "untagged ethe 1/2/2")
	if noTagged < 0 || untagged < 0 || untagged < noTagged {
		t.Errorf("untagged must come after tagged removals: %q", cmds)
	}
	if cmds[untagged-1] != "vlan 109" {
		t.Errorf("untagged must be in vlan 109: %q", cmds)
	}
	// Old dual-mode VLAN 109's tagged entry is removed.
	for i, c := range cmds {
		if c == "vlan 109" && i+1 < len(cmds) && cmds[i+1] == "no tagged ethe 1/2/2" {
			return
		}
	}
	t.Errorf("expected `vlan 109 / no tagged ethe 1/2/2`: %q", cmds)
}

// Trunk → tagged-only (untagged_vlan removed): no dual-mode, then drop 109.
func TestInterfaceEthernetUpdate_DualModeRemoveUntagged(t *testing.T) {
	trunk := []int64{101, 107, 110, 120, 200}
	mock := updateEth(t, "6430-c12",
		ethModel("1/2/2", 109, trunk, false),
		ethModel("1/2/2", 0, trunk, false))
	want := []string{
		"interface ethernet 1/2/2", "no dual-mode", "exit",
		"vlan 109", "no tagged ethe 1/2/2", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// Access → trunk (1/1/1 on the 6430 is untagged 109 in VLAN context): remove
// the VLAN-context untagged first, tag (109 included), then dual-mode 109.
func TestInterfaceEthernetUpdate_DualModeAccessToTrunk(t *testing.T) {
	mock := updateEth(t, "6430-c12",
		ethModel("1/1/1", 109, nil, false),
		ethModel("1/1/1", 109, []int64{101}, false))
	want := []string{
		"vlan 109", "no untagged ethe 1/1/1", "exit",
		"vlan 101", "tagged ethe 1/1/1", "exit",
		"vlan 109", "tagged ethe 1/1/1", "exit",
		"interface ethernet 1/1/1", "dual-mode 109", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// Access → access on 08.0.30 is plain VLAN-context movement.
func TestInterfaceEthernetUpdate_DualModeFirmwareAccessToAccess(t *testing.T) {
	mock := updateEth(t, "6430-c12",
		ethModel("1/1/1", 109, nil, false),
		ethModel("1/1/1", 200, nil, false))
	want := []string{
		"vlan 109", "no untagged ethe 1/1/1", "exit",
		"vlan 200", "untagged ethe 1/1/1", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// State says dual-mode 102 but the device has drifted (hand-run `no
// dual-mode`): an apply that touches the port re-asserts dual-mode.
func TestInterfaceEthernetUpdate_DualModeHealsDeviceDrift(t *testing.T) {
	// 1/1/5 on the 6450 is tagged in 7 VLANs with NO dual-mode line.
	trunk := []int64{101, 107, 109, 110, 120, 200}
	mock := updateEth(t, "6450-c12pd",
		ethModel("1/1/5", 102, trunk, false),
		ethModel("1/1/5", 102, append(trunk, 103), false))
	want := []string{
		"vlan 103", "tagged ethe 1/1/5", "exit",
		"interface ethernet 1/1/5", "dual-mode 102", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// No-op update on a dual-mode trunk sends nothing and does not write memory.
func TestInterfaceEthernetUpdate_DualModeNoChangeNoCommands(t *testing.T) {
	trunk := []int64{101, 107, 109, 110, 120, 200}
	mock := updateEth(t, "6430-c12",
		ethModel("1/2/1", 102, trunk, false),
		ethModel("1/2/1", 102, trunk, false))
	if got := cliOf(mock); len(got) != 0 {
		t.Errorf("expected no commands, got %q", got)
	}
	if mock.WriteMemoryCalls != 0 {
		t.Errorf("write memory calls = %d, want 0", mock.WriteMemoryCalls)
	}
}

// 08.0.95 update path is unchanged (remove-then-add through VLAN context).
func TestInterfaceEthernetUpdate_LegacyDialectNotEmittedOn08095(t *testing.T) {
	mock := updateEth(t, "7250-24",
		ethModel("1/2/1", 20, []int64{10, 30, 40, 50, 60, 70}, false),
		ethModel("1/2/1", 30, []int64{10, 20, 40, 50, 60, 70}, false))
	want := []string{
		"vlan 20", "no untagged ethe 1/2/1", "exit",
		"vlan 30", "no tagged ethe 1/2/1", "exit",
		"vlan 30", "untagged ethe 1/2/1", "exit",
		"vlan 20", "tagged ethe 1/2/1", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// --- Delete ---------------------------------------------------------------

func deleteEth(t *testing.T, fixture string, state *InterfaceEthernetResourceModel) *sshclient.MockClient {
	t.Helper()
	r, mock := newEthResource(t, fixture)
	s, _ := ethStateAndPlan(t, r, state, nil)
	resp := resource.DeleteResponse{State: s}
	r.Delete(context.Background(), resource.DeleteRequest{State: s}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete error: %v", resp.Diagnostics)
	}
	return mock
}

func TestInterfaceEthernetDelete_DualModeClearedBeforeTagged(t *testing.T) {
	mock := deleteEth(t, "6430-c12", ethModel("1/2/1", 102, []int64{101, 107, 109, 110, 120, 200}, false))
	cmds := cliOf(mock)
	noDual := indexOf(cmds, "no dual-mode")
	noTagged := indexOf(cmds, "no tagged ethe 1/2/1")
	if noDual < 0 || noTagged < 0 || noDual > noTagged {
		t.Errorf("no dual-mode must precede tagged removal: %q", cmds)
	}
	// Every VLAN the port is tagged in (102 included) is removed.
	for _, v := range []string{"vlan 101", "vlan 102", "vlan 107", "vlan 109", "vlan 110", "vlan 120", "vlan 200"} {
		if !mock.HasCommand(v) {
			t.Errorf("expected removal from %s: %q", v, cmds)
		}
	}
}

func TestInterfaceEthernetDelete_NoDualModeOn08095(t *testing.T) {
	mock := deleteEth(t, "7250-24", ethModel("1/2/1", 20, []int64{10, 30, 40, 50, 60, 70}, false))
	if mock.HasCommandContaining("dual-mode") {
		t.Errorf("08.0.95 delete must not mention dual-mode: %v", mock.Commands)
	}
	if !mock.HasCommand("no untagged ethe 1/2/1") {
		t.Errorf("expected VLAN-context untagged removal: %v", mock.Commands)
	}
}
