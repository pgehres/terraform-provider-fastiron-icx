package resource

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

func newSwitchImageVLANResource() (*VLANResource, *sshclient.MockClient) {
	mock := sshclient.NewMockClient("")
	return &VLANResource{
		client:          mock,
		imageType:       parser.ImageTypeSwitch,
		firmwareVersion: "08.0.95rT211",
	}, mock
}

func vlanModel(routerInterface types.Int64) *VLANResourceModel {
	return &VLANResourceModel{
		ID:               types.StringValue("20"),
		VlanID:           types.Int64Value(20),
		Name:             types.StringValue("mgmt"),
		RouterInterface:  routerInterface,
		SpanningTree:     types.BoolValue(false),
		STPPriority:      types.Int64Null(),
		MulticastPassive: types.BoolValue(false),
		MulticastVersion: types.Int64Null(),
		RawConfig:        types.ListNull(types.StringType),
	}
}

func vlanStateAndPlan(t *testing.T, r *VLANResource, state, plan *VLANResourceModel) (tfsdk.State, tfsdk.Plan) {
	t.Helper()
	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema error: %v", schemaResp.Diagnostics)
	}
	s := tfsdk.State{Schema: schemaResp.Schema}
	if diags := s.Set(ctx, state); diags.HasError() {
		t.Fatalf("failed to build state: %v", diags)
	}
	p := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := p.Set(ctx, plan); diags.HasError() {
		t.Fatalf("failed to build plan: %v", diags)
	}
	return s, p
}

// Adding a router_interface on a switch image fails before any commands.
func TestVLANUpdate_SwitchImagePlanRouterInterfaceErrors(t *testing.T) {
	r, mock := newSwitchImageVLANResource()
	state, plan := vlanStateAndPlan(t, r, vlanModel(types.Int64Null()), vlanModel(types.Int64Value(20)))

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)

	if !hasRouterImageError(resp.Diagnostics) {
		t.Fatalf("expected 'Router image required' error, got: %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("expected no commands sent, got %v", mock.Commands)
	}
}

// Create with a router_interface on a switch image fails before any commands.
func TestVLANCreate_SwitchImagePlanRouterInterfaceErrors(t *testing.T) {
	r, mock := newSwitchImageVLANResource()
	state, plan := vlanStateAndPlan(t, r, vlanModel(types.Int64Null()), vlanModel(types.Int64Value(20)))

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)

	if !hasRouterImageError(resp.Diagnostics) {
		t.Fatalf("expected 'Router image required' error, got: %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("expected no commands sent, got %v", mock.Commands)
	}
}

// A router_interface only in (stale) state on a switch image: removing it
// must not error, and `no router-interface` (which does not exist there) is
// not sent.
func TestVLANUpdate_SwitchImageStateOnlyRouterInterfaceSkips(t *testing.T) {
	r, mock := newSwitchImageVLANResource()
	state, plan := vlanStateAndPlan(t, r, vlanModel(types.Int64Value(20)), vlanModel(types.Int64Null()))

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error, got: %v", resp.Diagnostics)
	}
	if mock.HasCommandContaining("router-interface") {
		t.Errorf("expected no router-interface command on switch image, got %v", mock.Commands)
	}
}

// On a router image the same removal sends `no router-interface ve N`.
func TestVLANUpdate_RouterImageRemovesRouterInterface(t *testing.T) {
	r, mock := newSwitchImageVLANResource()
	r.imageType = parser.ImageTypeRouter
	state, plan := vlanStateAndPlan(t, r, vlanModel(types.Int64Value(20)), vlanModel(types.Int64Null()))

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error, got: %v", resp.Diagnostics)
	}
	if !mock.HasCommand("no router-interface ve 20") {
		t.Errorf("expected no router-interface ve 20, got %v", mock.Commands)
	}
}
