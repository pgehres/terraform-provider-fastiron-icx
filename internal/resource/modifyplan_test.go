package resource

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

// Plan-time image checks: ModifyPlan must reject router-image-only config on a
// detected switch image so the error surfaces in `terraform plan`, never block
// a router/unknown/unset image, and always let destroy plans through.

var allImageTypes = []struct {
	name      string
	imageType parser.ImageType
	wantErr   bool
}{
	{"switch", parser.ImageTypeSwitch, true},
	{"router", parser.ImageTypeRouter, false},
	{"unknown", parser.ImageTypeUnknown, false},
	{"unset", parser.ImageType(""), false},
}

// nullPlan builds the plan the framework passes to ModifyPlan for a destroy.
func nullPlan(plan tfsdk.Plan) tfsdk.Plan {
	return tfsdk.Plan{
		Schema: plan.Schema,
		Raw:    tftypes.NewValue(plan.Schema.Type().TerraformType(context.Background()), nil),
	}
}

func checkModifyPlanDiags(t *testing.T, resp resource.ModifyPlanResponse, wantErr bool) {
	t.Helper()
	if got := hasRouterImageError(resp.Diagnostics); got != wantErr {
		t.Errorf("Router image error = %v, want %v (diags: %v)", got, wantErr, resp.Diagnostics)
	}
	if !wantErr && resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got: %v", resp.Diagnostics)
	}
}

func vePlan(t *testing.T, r *InterfaceVEResource) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: veSchema(t, r).Schema}
	if diags := p.Set(context.Background(), &InterfaceVEResourceModel{
		ID:        types.StringUnknown(),
		VeID:      types.Int64Value(10),
		IPAddress: types.StringValue("10.0.0.1/24"),
		RawConfig: types.ListNull(types.StringType),
	}); diags.HasError() {
		t.Fatalf("failed to build plan: %v", diags)
	}
	return p
}

func TestInterfaceVEModifyPlan_ImageTypes(t *testing.T) {
	for _, tt := range allImageTypes {
		t.Run(tt.name, func(t *testing.T) {
			r, mock := newSwitchImageVEResource()
			r.dialect = dialect.Detect(r.dialect.Version.Raw, tt.imageType)
			plan := vePlan(t, r)

			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan}, &resp)

			checkModifyPlanDiags(t, resp, tt.wantErr)
			if len(mock.Commands) != 0 {
				t.Errorf("ModifyPlan must not send commands, got %v", mock.Commands)
			}
		})
	}
}

// Delete deliberately succeeds on a switch image, so the destroy plan must not
// be rejected — otherwise stale VE state could never be cleared.
func TestInterfaceVEModifyPlan_SwitchImageDestroyAllowed(t *testing.T) {
	r, _ := newSwitchImageVEResource()
	state := veStateWithID(t, r, 10)
	plan := nullPlan(vePlan(t, r))

	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)

	checkModifyPlanDiags(t, resp, false)
}

func TestVLANModifyPlan_RouterInterface(t *testing.T) {
	for _, tt := range allImageTypes {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newSwitchImageVLANResource()
			r.dialect = dialect.Detect(r.dialect.Version.Raw, tt.imageType)
			_, plan := vlanStateAndPlan(t, r, vlanModel(types.Int64Null()), vlanModel(types.Int64Value(20)))

			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan}, &resp)

			checkModifyPlanDiags(t, resp, tt.wantErr)
		})
	}
}

// A VLAN without router_interface, a value not yet known at plan time, and a
// destroy plan all pass on a switch image.
func TestVLANModifyPlan_SwitchImagePasses(t *testing.T) {
	tests := []struct {
		name    string
		ri      types.Int64
		destroy bool
	}{
		{"null router_interface", types.Int64Null(), false},
		{"unknown router_interface", types.Int64Unknown(), false},
		{"destroy", types.Int64Value(20), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newSwitchImageVLANResource()
			state, plan := vlanStateAndPlan(t, r, vlanModel(types.Int64Value(20)), vlanModel(tt.ri))
			if tt.destroy {
				plan = nullPlan(plan)
			}

			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)

			checkModifyPlanDiags(t, resp, false)
		})
	}
}

func TestSystemModifyPlan_GlobalSTP(t *testing.T) {
	for _, tt := range allImageTypes {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newSwitchImageSystemResource()
			r.dialect = dialect.Detect(r.dialect.Version.Raw, tt.imageType)
			_, plan := systemStateAndPlan(t, r, systemModel(false, false), systemModel(true, false))

			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan}, &resp)

			checkModifyPlanDiags(t, resp, tt.wantErr)
		})
	}
}

// global_stp = false (even when state still says true, e.g. after a reimage)
// and a destroy plan both pass on a switch image.
func TestSystemModifyPlan_SwitchImagePasses(t *testing.T) {
	t.Run("global_stp false", func(t *testing.T) {
		r, _ := newSwitchImageSystemResource()
		state, plan := systemStateAndPlan(t, r, systemModel(true, false), systemModel(false, true))

		resp := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)

		checkModifyPlanDiags(t, resp, false)
	})
	t.Run("destroy", func(t *testing.T) {
		r, _ := newSwitchImageSystemResource()
		state, plan := systemStateAndPlan(t, r, systemModel(true, false), systemModel(true, false))
		plan = nullPlan(plan)

		resp := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)

		checkModifyPlanDiags(t, resp, false)
	})
}
