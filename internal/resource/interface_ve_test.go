package resource

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// newSwitchImageVEResource returns an InterfaceVEResource configured as if the
// provider had detected a switch-image device.
func newSwitchImageVEResource() (*InterfaceVEResource, *sshclient.MockClient) {
	mock := sshclient.NewMockClient("")
	return &InterfaceVEResource{
		client:          mock,
		imageType:       parser.ImageTypeSwitch,
		firmwareVersion: "08.0.30uT311",
	}, mock
}

func veSchema(t *testing.T, r *InterfaceVEResource) tfsdk.State {
	t.Helper()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema error: %v", schemaResp.Diagnostics)
	}
	return tfsdk.State{Schema: schemaResp.Schema}
}

func veStateWithID(t *testing.T, r *InterfaceVEResource, veID int64) tfsdk.State {
	t.Helper()
	state := veSchema(t, r)
	diags := state.Set(context.Background(), &InterfaceVEResourceModel{
		ID:        types.StringValue("10"),
		VeID:      types.Int64Value(veID),
		IPAddress: types.StringNull(),
		RawConfig: types.ListNull(types.StringType),
	})
	if diags.HasError() {
		t.Fatalf("failed to build state: %v", diags)
	}
	return state
}

// A VE in state on a device reimaged router→switch must refresh away as
// "absent" rather than erroring, or the stale state could never be cleared.
func TestInterfaceVERead_SwitchImageRemovesState(t *testing.T) {
	ctx := context.Background()
	r, mock := newSwitchImageVEResource()
	state := veStateWithID(t, r, 10)

	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error, got: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected state to be removed on switch image")
	}
	if len(mock.Commands) != 0 {
		t.Errorf("expected no commands sent, got %v", mock.Commands)
	}
}

// Delete on a switch image succeeds without sending commands, so stale state
// from a router→switch reimage can be destroyed.
func TestInterfaceVEDelete_SwitchImageNoCommands(t *testing.T) {
	ctx := context.Background()
	r, mock := newSwitchImageVEResource()
	state := veStateWithID(t, r, 10)

	resp := resource.DeleteResponse{State: state}
	r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error, got: %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("expected no commands sent, got %v", mock.Commands)
	}
	if mock.WriteMemoryCalls != 0 {
		t.Errorf("expected no write memory calls, got %d", mock.WriteMemoryCalls)
	}
}

// Create keeps the fail-fast error on a switch image, before any commands.
func TestInterfaceVECreate_SwitchImageErrors(t *testing.T) {
	ctx := context.Background()
	r, mock := newSwitchImageVEResource()
	schemaState := veSchema(t, r)

	plan := tfsdk.Plan{Schema: schemaState.Schema}
	diags := plan.Set(ctx, &InterfaceVEResourceModel{
		ID:        types.StringUnknown(),
		VeID:      types.Int64Value(10),
		IPAddress: types.StringNull(),
		RawConfig: types.ListNull(types.StringType),
	})
	if diags.HasError() {
		t.Fatalf("failed to build plan: %v", diags)
	}

	resp := resource.CreateResponse{State: schemaState}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error on switch image")
	}
	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "Router image required") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'Router image required' error, got: %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("expected no commands sent, got %v", mock.Commands)
	}
}
