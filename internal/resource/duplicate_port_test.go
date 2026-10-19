package resource

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
)

// Gotcha #10: two icx_interface_ethernet resources on one port clobber each
// other on every apply. ModifyPlan claims the port in the provider instance's
// registry and errors on a second, different claim. The registry must never
// fire on Terraform's own re-plans of one resource, so the tests cover the
// shapes those take as well as the real duplicate.

// planEth runs ModifyPlan for one resource instance sharing registry reg, with
// the given configuration (unknown values allowed) and optional prior state.
func planEth(t *testing.T, reg *providerdata.PortRegistry, cfg, state *InterfaceEthernetResourceModel, destroy bool) resource.ModifyPlanResponse {
	t.Helper()
	r, _ := newEthResource(t, "7250-24")
	r.ports = reg
	s, p := ethStateAndPlan(t, r, state, cfg)
	config := tfsdk.Config{Schema: p.Schema, Raw: p.Raw}
	if destroy {
		p = nullPlan(p)
	}
	resp := resource.ModifyPlanResponse{Plan: p}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Config: config, Plan: p, State: s}, &resp)
	return resp
}

func dupError(resp resource.ModifyPlanResponse) string {
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "managed by more than one") {
			return d.Detail()
		}
	}
	return ""
}

func TestInterfaceEthernetModifyPlan_DuplicatePortRejected(t *testing.T) {
	reg := providerdata.NewPortRegistry()
	// An explicit-trunk declaration and an access declaration for 1/2/1 —
	// the exact pair from the gotcha.
	first := planEth(t, reg, ethModel("1/2/1", 0, []int64{10, 20}, false), nil, false)
	if first.Diagnostics.HasError() {
		t.Fatalf("first claim must pass: %v", first.Diagnostics)
	}
	second := planEth(t, reg, ethModel("1/2/1", 30, nil, false), nil, false)
	detail := dupError(second)
	if detail == "" {
		t.Fatalf("second claim on 1/2/1 must error, got %v", second.Diagnostics)
	}
	for _, want := range []string{"port 1/2/1", "tagged_vlans, untagged_vlan", "terraform state rm"} {
		if !strings.Contains(detail, want) {
			t.Errorf("error must mention %q, got: %s", want, detail)
		}
	}
}

func TestInterfaceEthernetModifyPlan_DifferentPortsCoexist(t *testing.T) {
	reg := providerdata.NewPortRegistry()
	for _, port := range []string{"1/1/1", "1/1/2", "1/2/1"} {
		if resp := planEth(t, reg, ethModel(port, 20, nil, false), nil, false); resp.Diagnostics.HasError() {
			t.Errorf("port %s: unexpected error %v", port, resp.Diagnostics)
		}
	}
}

// Terraform plans a replacement twice with the same configuration (prior
// state, then null prior state). Identical claims are the same resource.
func TestInterfaceEthernetModifyPlan_SameResourceReplannedPasses(t *testing.T) {
	reg := providerdata.NewPortRegistry()
	cfg := ethModel("1/2/1", 20, []int64{10, 30}, false)
	if resp := planEth(t, reg, cfg, cfg, false); resp.Diagnostics.HasError() {
		t.Fatalf("plan against prior state: %v", resp.Diagnostics)
	}
	if resp := planEth(t, reg, cfg, nil, false); resp.Diagnostics.HasError() {
		t.Fatalf("plan against null prior state: %v", resp.Diagnostics)
	}
	if resp := planEth(t, reg, cfg, cfg, false); resp.Diagnostics.HasError() {
		t.Fatalf("third identical plan: %v", resp.Diagnostics)
	}
}

// Values that are unknown at plan time (untagged_vlan = icx_vlan.x.vlan_id for
// a VLAN created in the same run) are resolved later. The resource planned
// with the unknown and then with the known value is one resource, and the
// registry keeps the known value for later comparison.
func TestInterfaceEthernetModifyPlan_UnknownValuesMatchAnything(t *testing.T) {
	reg := providerdata.NewPortRegistry()
	unknown := ethModel("1/2/1", 0, nil, false)
	unknown.UntaggedVLAN = types.Int64Unknown()
	unknown.TaggedVLANs = types.SetUnknown(types.Int64Type)
	if resp := planEth(t, reg, unknown, nil, false); resp.Diagnostics.HasError() {
		t.Fatalf("unknown claim: %v", resp.Diagnostics)
	}
	if resp := planEth(t, reg, ethModel("1/2/1", 20, []int64{10}, false), nil, false); resp.Diagnostics.HasError() {
		t.Fatalf("known claim after unknown: %v", resp.Diagnostics)
	}
	// Now that 20/[10] is recorded, a genuinely different third claim fails.
	if resp := planEth(t, reg, ethModel("1/2/1", 30, []int64{10}, false), nil, false); dupError(resp) == "" {
		t.Fatalf("different claim after resolution must error, got %v", resp.Diagnostics)
	}
	// The reverse order (known first, then unknown) is equally fine.
	reg2 := providerdata.NewPortRegistry()
	planEth(t, reg2, ethModel("1/2/1", 20, []int64{10}, false), nil, false)
	if resp := planEth(t, reg2, unknown, nil, false); resp.Diagnostics.HasError() {
		t.Fatalf("unknown claim after known: %v", resp.Diagnostics)
	}
}

// An unknown port cannot be compared and is not claimed.
func TestInterfaceEthernetModifyPlan_UnknownPortSkipped(t *testing.T) {
	reg := providerdata.NewPortRegistry()
	cfg := ethModel("1/2/1", 20, nil, false)
	cfg.Port = types.StringUnknown()
	if resp := planEth(t, reg, cfg, nil, false); resp.Diagnostics.HasError() {
		t.Fatalf("unknown port: %v", resp.Diagnostics)
	}
	if resp := planEth(t, reg, ethModel("1/2/1", 30, nil, false), nil, false); resp.Diagnostics.HasError() {
		t.Fatalf("first known claim must pass: %v", resp.Diagnostics)
	}
}

// Destroy plans are not claims: removing one of two duplicates (after the
// documented `terraform state rm`, or not) must not be blocked by the
// survivor, and a full destroy must not trip over itself.
func TestInterfaceEthernetModifyPlan_DestroyIsNotAClaim(t *testing.T) {
	reg := providerdata.NewPortRegistry()
	live := ethModel("1/2/1", 20, []int64{10}, false)
	if resp := planEth(t, reg, live, live, false); resp.Diagnostics.HasError() {
		t.Fatalf("live claim: %v", resp.Diagnostics)
	}
	gone := ethModel("1/2/1", 30, nil, false)
	if resp := planEth(t, reg, gone, gone, true); resp.Diagnostics.HasError() {
		t.Fatalf("destroy plan must pass: %v", resp.Diagnostics)
	}
	// Destroy first, then live: still no claim recorded by the destroy.
	reg2 := providerdata.NewPortRegistry()
	planEth(t, reg2, gone, gone, true)
	if resp := planEth(t, reg2, live, live, false); resp.Diagnostics.HasError() {
		t.Fatalf("live after destroy: %v", resp.Diagnostics)
	}
}

// Each provider instance (alias = switch) has its own registry: the same
// port number on two switches is not a duplicate.
func TestInterfaceEthernetModifyPlan_PerProviderInstance(t *testing.T) {
	sw1, sw2 := providerdata.NewPortRegistry(), providerdata.NewPortRegistry()
	if resp := planEth(t, sw1, ethModel("1/2/1", 20, nil, false), nil, false); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if resp := planEth(t, sw2, ethModel("1/2/1", 30, []int64{10}, false), nil, false); resp.Diagnostics.HasError() {
		t.Fatalf("other switch: %v", resp.Diagnostics)
	}
}

// Without a registry (resource never configured) detection is simply off.
func TestInterfaceEthernetModifyPlan_NoRegistry(t *testing.T) {
	planEth(t, nil, ethModel("1/2/1", 20, nil, false), nil, false)
	if resp := planEth(t, nil, ethModel("1/2/1", 30, nil, false), nil, false); resp.Diagnostics.HasError() {
		t.Fatalf("nil registry must not error: %v", resp.Diagnostics)
	}
}

// Configure wires the registry from provider data.
func TestInterfaceEthernetConfigure_WiresPortRegistry(t *testing.T) {
	r, mock := newEthResource(t, "7250-24")
	reg := providerdata.NewPortRegistry()
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: &providerdata.ProviderData{Client: mock, Ports: reg}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if r.ports != reg {
		t.Error("Configure must adopt the provider's port registry")
	}
}
