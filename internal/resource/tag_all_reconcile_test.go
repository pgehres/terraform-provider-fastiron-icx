package resource

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Gotcha #8: a tag_all_vlans trunk whose membership drifted used to be
// invisible to plan forever. Read now checks the device against the expansion
// the create path uses. These tests run against the real sanitized fixtures:
// every port that is a full trunk must read back byte-identical to its state
// (zero diff for the fleet), and the ports that are genuinely missing a VLAN
// in the captures must surface it.

// readEthFull runs Read with a full starting state and returns the resulting
// model plus the diagnostics, so tests can assert on the warning.
func readEthFull(t *testing.T, fixture string, start *InterfaceEthernetResourceModel) (InterfaceEthernetResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	r, _ := newEthResource(t, fixture)
	state, _ := ethStateAndPlan(t, r, start, nil)

	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read error: %v", resp.Diagnostics)
	}
	var got InterfaceEthernetResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("state get: %v", diags)
	}
	return got, resp.Diagnostics
}

func driftWarning(diags diag.Diagnostics) diag.Diagnostic {
	for _, d := range diags {
		if d.Severity() == diag.SeverityWarning && strings.Contains(d.Summary(), "has drifted") {
			return d
		}
	}
	return nil
}

// Every full trunk in the 08.0.95 captures, declared tag_all_vlans = true with
// the untagged VLAN the device has, reads back exactly as stored: tagged_vlans
// stays null, tag_all_vlans stays true, no warning. This is the fleet's
// zero-diff guarantee after upgrading to a provider that reconciles.
func TestInterfaceEthernetRead_TagAllFullTrunkPlansClean(t *testing.T) {
	tests := []struct {
		fixture, port string
		untagged      int64
	}{
		{"7250-24", "1/2/1", 20}, // router uplink: untagged 20, tagged 10/30-70
		{"7250-24", "1/2/3", 0},  // tagged-only, every VLAN
		{"7250-24", "1/2/4", 20},
		{"7250-24", "1/2/7", 0},
		{"7150-24", "1/3/4", 0},
		{"7150-24", "1/2/1", 20},
		{"7150-c12", "1/3/1", 0},
		{"7150-c12", "1/3/2", 0},
		{"7150-c10zp", "1/3/1", 0}, // carries the ISP VLANs 900-902 too
	}
	for _, tt := range tests {
		t.Run(tt.fixture+" "+tt.port, func(t *testing.T) {
			start := ethModel(tt.port, tt.untagged, nil, true)
			start.SpanningTreePt2PtMac = types.BoolValue(false)
			got, diags := readEthFull(t, tt.fixture, start)

			// Compare only what Read derives from VLAN membership; port-level
			// attributes (port_name etc.) come from the interface stanza.
			if !got.TagAllVLANs.ValueBool() {
				t.Errorf("tag_all_vlans = %v, want true", got.TagAllVLANs)
			}
			if !got.TaggedVLANs.IsNull() {
				t.Errorf("tagged_vlans = %v, want null (state untouched)", got.TaggedVLANs)
			}
			if !got.UntaggedVLAN.Equal(start.UntaggedVLAN) {
				t.Errorf("untagged_vlan = %v, want %v", got.UntaggedVLAN, start.UntaggedVLAN)
			}
			if w := driftWarning(diags); w != nil {
				t.Errorf("unexpected drift warning: %s", w.Detail())
			}
		})
	}
}

// 1/3/2 on the c10zp is the transit trunk from the field incident: it carries
// 20/30/40/50/70 but not the ISP VLANs 900-902 that 1/3/1 carries. Declared
// tag_all_vlans = true, Read must report the device truth and name the gap.
func TestInterfaceEthernetRead_TagAllDriftSurfaces(t *testing.T) {
	got, diags := readEthFull(t, "7150-c10zp", ethModel("1/3/2", 0, nil, true))

	if got.TagAllVLANs.ValueBool() {
		t.Error("tag_all_vlans must read false when the trunk is missing VLANs")
	}
	want := []int64{20, 30, 40, 50, 70}
	if tagged := taggedOf(t, got); !reflect.DeepEqual(tagged, want) {
		t.Errorf("tagged_vlans = %v, want actual membership %v", tagged, want)
	}
	w := driftWarning(diags)
	if w == nil {
		t.Fatalf("expected a drift warning, got %v", diags)
	}
	if !strings.Contains(w.Detail(), "Missing from the port: VLAN 900, 901, 902.") {
		t.Errorf("warning must name the missing VLANs, got: %s", w.Detail())
	}
	if strings.Contains(w.Detail(), "outside the expansion") {
		t.Errorf("no extras expected, got: %s", w.Detail())
	}
}

// Dual-mode firmware: 1/2/1 on the 6430 is dual-mode 102 and tagged in every
// VLAN except 103 (which has no members at all). The dual-mode VLAN is the
// untagged VLAN and so excluded from the expansion; 103 is the drift.
func TestInterfaceEthernetRead_TagAllDriftOnDualModeFirmware(t *testing.T) {
	got, diags := readEthFull(t, "6430-c12", ethModel("1/2/1", 102, nil, true))

	if got.TagAllVLANs.ValueBool() {
		t.Error("tag_all_vlans must read false")
	}
	if got.UntaggedVLAN.ValueInt64() != 102 {
		t.Errorf("untagged_vlan = %v, want 102 (from dual-mode)", got.UntaggedVLAN)
	}
	want := []int64{101, 107, 109, 110, 120, 200}
	if tagged := taggedOf(t, got); !reflect.DeepEqual(tagged, want) {
		t.Errorf("tagged_vlans = %v, want %v", tagged, want)
	}
	w := driftWarning(diags)
	if w == nil || !strings.Contains(w.Detail(), "Missing from the port: VLAN 103.") {
		t.Errorf("expected warning naming VLAN 103, got %v", diags)
	}
}

// A port with no tagged membership at all (an access port someone declared
// tag_all_vlans on, or a trunk whose tags were all stripped) reads as
// tag_all_vlans = false with a null set — never an empty set, which would be
// a second kind of diff.
func TestInterfaceEthernetRead_TagAllNoTagsReadsNullSet(t *testing.T) {
	got, diags := readEthFull(t, "7250-24", ethModel("1/1/1", 20, nil, true))
	if got.TagAllVLANs.ValueBool() {
		t.Error("tag_all_vlans must read false")
	}
	if !got.TaggedVLANs.IsNull() {
		t.Errorf("tagged_vlans = %v, want null", got.TaggedVLANs)
	}
	if w := driftWarning(diags); w == nil || !strings.Contains(w.Detail(), "tagged_vlans = null") {
		t.Errorf("expected warning reporting a null set, got %v", diags)
	}
}

// Explicit tagged_vlans lists are untouched by reconciliation: Read reports
// the device set and never emits the drift warning, even for a port that
// would fail the tag_all check.
func TestInterfaceEthernetRead_ExplicitListUnchanged(t *testing.T) {
	got, diags := readEthFull(t, "7150-c10zp", ethModel("1/3/2", 0, []int64{20, 30}, false))
	want := []int64{20, 30, 40, 50, 70}
	if tagged := taggedOf(t, got); !reflect.DeepEqual(tagged, want) {
		t.Errorf("tagged_vlans = %v, want %v", tagged, want)
	}
	if got.TagAllVLANs.ValueBool() {
		t.Error("tag_all_vlans must stay false")
	}
	if w := driftWarning(diags); w != nil {
		t.Errorf("explicit list must not warn: %s", w.Detail())
	}
}

// --- The repair ------------------------------------------------------------

// After Read reported the c10zp 1/3/2 drift, the plan is "back to
// tag_all_vlans = true". Update must add exactly the missing VLANs and remove
// nothing: the old side resolves from the explicit set Read stored, the new
// side from the expansion.
func TestInterfaceEthernetUpdate_TagAllRepairIsAdditive(t *testing.T) {
	mock := updateEth(t, "7150-c10zp",
		ethModel("1/3/2", 0, []int64{20, 30, 40, 50, 70}, false),
		ethModel("1/3/2", 0, nil, true))
	want := []string{
		"vlan 900", "tagged ethe 1/3/2", "exit",
		"vlan 901", "tagged ethe 1/3/2", "exit",
		"vlan 902", "tagged ethe 1/3/2", "exit",
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if mock.HasCommandContaining("no tagged") || mock.HasCommandContaining("no untagged") {
		t.Errorf("repair must remove nothing: %q", mock.Commands)
	}
	if mock.WriteMemoryCalls != 1 {
		t.Errorf("write memory calls = %d, want 1", mock.WriteMemoryCalls)
	}
}

// Same repair on dual-mode firmware: 6430 1/2/1 gains VLAN 103 and nothing
// else — dual-mode 102 is already on the device, so it is not re-issued.
func TestInterfaceEthernetUpdate_TagAllRepairOnDualModeFirmware(t *testing.T) {
	mock := updateEth(t, "6430-c12",
		ethModel("1/2/1", 102, []int64{101, 107, 109, 110, 120, 200}, false),
		ethModel("1/2/1", 102, nil, true))
	want := []string{"vlan 103", "tagged ethe 1/2/1", "exit"}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

// A healthy tag_all_vlans trunk re-applied with no change sends nothing and
// does not write memory (the no-perpetual-diff half of gotcha #8).
func TestInterfaceEthernetUpdate_TagAllHealthyNoCommands(t *testing.T) {
	mock := updateEth(t, "7250-24",
		ethModel("1/2/1", 20, nil, true),
		ethModel("1/2/1", 20, nil, true))
	if got := cliOf(mock); len(got) != 0 {
		t.Errorf("expected no commands, got %q", got)
	}
	if mock.WriteMemoryCalls != 0 {
		t.Errorf("write memory calls = %d, want 0", mock.WriteMemoryCalls)
	}
}

// --- Fixture-driven cross-checks -------------------------------------------

// The trunk-all detection icx_vlan.Create uses and the expansion Read checks
// against must agree on the captures: every port findTrunkAllPorts reports
// must read clean as tag_all_vlans with its device untagged VLAN.
func TestFindTrunkAllPorts_Fixtures(t *testing.T) {
	tests := []struct {
		fixture string
		want    []string
	}{
		{"7250-24", []string{"1/1/19", "1/1/20", "1/2/1", "1/2/3", "1/2/4", "1/2/5", "1/2/6", "1/2/7", "1/2/8"}},
		{"7150-24", []string{"1/1/15", "1/1/19", "1/1/20", "1/1/22", "1/1/23", "1/1/24", "1/2/1", "1/2/2", "1/3/1", "1/3/2", "1/3/4"}},
		{"7150-c12", []string{"1/3/1", "1/3/2"}},
		{"7150-c10zp", []string{"1/3/1"}}, // 1/3/2 lacks the ISP VLANs
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			r, _ := newEthResource(t, tt.fixture)
			config, err := r.getRunningConfig()
			if err != nil {
				t.Fatal(err)
			}
			got := findTrunkAllPorts(config, 0)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("findTrunkAllPorts = %v, want %v", got, tt.want)
			}
			for _, port := range got {
				start := readEth(t, tt.fixture, port, false) // device untagged VLAN
				start.TagAllVLANs = types.BoolValue(true)
				start.TaggedVLANs = types.SetNull(types.Int64Type)
				after, diags := readEthFull(t, tt.fixture, &start)
				if !after.TagAllVLANs.ValueBool() || !after.TaggedVLANs.IsNull() || driftWarning(diags) != nil {
					t.Errorf("port %s: trunk-all port must read clean, got tag_all=%v tagged=%v diags=%v",
						port, after.TagAllVLANs, after.TaggedVLANs, diags)
				}
			}
		})
	}
}

func TestExpandAllVLANs(t *testing.T) {
	r, _ := newEthResource(t, "7150-c10zp")
	config, err := r.getRunningConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := expandAllVLANs(config, 20), []int{30, 40, 50, 70, 900, 901, 902}; !reflect.DeepEqual(got, want) {
		t.Errorf("expandAllVLANs(untagged 20) = %v, want %v", got, want)
	}
	if got, want := expandAllVLANs(config, 0), []int{20, 30, 40, 50, 70, 900, 901, 902}; !reflect.DeepEqual(got, want) {
		t.Errorf("expandAllVLANs(no untagged) = %v, want %v", got, want)
	}
}
