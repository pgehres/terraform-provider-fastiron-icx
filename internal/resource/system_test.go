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

func newSwitchImageSystemResource() (*SystemResource, *sshclient.MockClient) {
	mock := sshclient.NewMockClient("")
	return &SystemResource{
		client:          mock,
		imageType:       parser.ImageTypeSwitch,
		firmwareVersion: "08.0.95rT211",
	}, mock
}

func systemModel(globalSTP, telnet bool) *SystemResourceModel {
	return &SystemResourceModel{
		ID:                      types.StringValue("system"),
		GlobalSTP:               types.BoolValue(globalSTP),
		TelnetServer:            types.BoolValue(telnet),
		DHCPClientDisable:       types.BoolValue(false),
		OpticalMonitor:          types.BoolValue(false),
		OpticalMonitorNonRuckus: types.BoolValue(false),
		ManagerRegistrar:        types.BoolValue(false),
		ManagerDisable:          types.BoolValue(false),
		ManagerPortList:         types.StringNull(),
	}
}

func systemStateAndPlan(t *testing.T, r *SystemResource, state, plan *SystemResourceModel) (tfsdk.State, tfsdk.Plan) {
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
	if plan != nil {
		if diags := p.Set(ctx, plan); diags.HasError() {
			t.Fatalf("failed to build plan: %v", diags)
		}
	}
	return s, p
}

// Asking for global_stp=true on a switch image fails before any commands.
func TestSystemUpdate_SwitchImagePlanGlobalSTPErrors(t *testing.T) {
	r, mock := newSwitchImageSystemResource()
	state, plan := systemStateAndPlan(t, r, systemModel(false, true), systemModel(true, true))

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)

	if !hasRouterImageError(resp.Diagnostics) {
		t.Fatalf("expected 'Router image required' error, got: %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("expected no commands sent, got %v", mock.Commands)
	}
}

// global_stp=true only in (stale) state on a switch image: turning it off
// must not error, and `no global-stp` (which does not exist there) is skipped
// while other changes still apply.
func TestSystemUpdate_SwitchImageStateOnlyGlobalSTPSkips(t *testing.T) {
	r, mock := newSwitchImageSystemResource()
	state, plan := systemStateAndPlan(t, r, systemModel(true, true), systemModel(false, false))

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error, got: %v", resp.Diagnostics)
	}
	if mock.HasCommandContaining("global-stp") {
		t.Errorf("expected no global-stp command on switch image, got %v", mock.Commands)
	}
	if !mock.HasCommand("no telnet server") {
		t.Errorf("expected other changes to still apply, got %v", mock.Commands)
	}
}

// Same state-only case with nothing else changing: no commands, no write memory.
func TestSystemUpdate_SwitchImageStateOnlyGlobalSTPNoCommands(t *testing.T) {
	r, mock := newSwitchImageSystemResource()
	state, plan := systemStateAndPlan(t, r, systemModel(true, true), systemModel(false, true))

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error, got: %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("expected no commands sent, got %v", mock.Commands)
	}
}

// Deleting the singleton with stale global_stp=true on a switch image must
// succeed and skip `no global-stp`.
func TestSystemDelete_SwitchImageStateOnlyGlobalSTPSkips(t *testing.T) {
	r, mock := newSwitchImageSystemResource()
	state, _ := systemStateAndPlan(t, r, systemModel(true, true), nil)

	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error, got: %v", resp.Diagnostics)
	}
	if mock.HasCommandContaining("global-stp") {
		t.Errorf("expected no global-stp command on switch image, got %v", mock.Commands)
	}
}

// On a router image the state-only removal still sends `no global-stp`.
func TestSystemUpdate_RouterImageTurnsOffGlobalSTP(t *testing.T) {
	r, mock := newSwitchImageSystemResource()
	r.imageType = parser.ImageTypeRouter
	state, plan := systemStateAndPlan(t, r, systemModel(true, true), systemModel(false, true))

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error, got: %v", resp.Diagnostics)
	}
	if !mock.HasCommand("no global-stp") {
		t.Errorf("expected no global-stp, got %v", mock.Commands)
	}
}
