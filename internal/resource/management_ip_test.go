package resource

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// icx_management_ip against the two switch-image fixtures:
//   7150-c10zp: ip address 10.0.20.5/24, gateway 10.0.20.1, no DNS
//   6430-c12:   ip address 10.0.20.6/24, gateway 10.0.20.9, DNS 10.0.20.1

func newMgmtResource(t *testing.T, fixture string, version string, image parser.ImageType) (*ManagementIPResource, *sshclient.MockClient) {
	t.Helper()
	cfg := ""
	if fixture != "" {
		cfg = loadFixture(t, fixture)
	}
	mock := sshclient.NewMockClient(cfg)
	return &ManagementIPResource{client: mock, dialect: dialect.Detect(version, image)}, mock
}

func newC10ZPMgmt(t *testing.T) (*ManagementIPResource, *sshclient.MockClient) {
	t.Helper()
	return newMgmtResource(t, "7150-c10zp", "08.0.95rT211", parser.ImageTypeSwitch)
}

func new6430Mgmt(t *testing.T) (*ManagementIPResource, *sshclient.MockClient) {
	t.Helper()
	return newMgmtResource(t, "6430-c12", "08.0.30uT311", parser.ImageTypeSwitch)
}

func strList(items ...string) types.List {
	if items == nil {
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, len(items))
	for i, s := range items {
		elems[i] = types.StringValue(s)
	}
	return types.ListValueMust(types.StringType, elems)
}

func mgmtModel(ip string, gw types.String, dns types.List, allow bool) *ManagementIPResourceModel {
	return &ManagementIPResourceModel{
		ID:                      types.StringValue(managementIPID),
		IPAddress:               types.StringValue(ip),
		DefaultGateway:          gw,
		DNSServers:              dns,
		AllowManagementIPChange: types.BoolValue(allow),
	}
}

func mgmtStateAndPlan(t *testing.T, r *ManagementIPResource, state, plan *ManagementIPResourceModel) (tfsdk.State, tfsdk.Plan) {
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
	} else {
		s = tfsdk.State{Schema: schemaResp.Schema, Raw: nullPlan(tfsdk.Plan{Schema: schemaResp.Schema}).Raw}
	}
	p := tfsdk.Plan{Schema: schemaResp.Schema}
	if plan != nil {
		if diags := p.Set(ctx, plan); diags.HasError() {
			t.Fatalf("failed to build plan: %v", diags)
		}
	}
	return s, p
}

func hasErrorTitled(diags diag.Diagnostics, title string) bool {
	for _, d := range diags.Errors() {
		if d.Summary() == title {
			return true
		}
	}
	return false
}

func hasWarningTitled(diags diag.Diagnostics, title string) bool {
	for _, d := range diags.Warnings() {
		if d.Summary() == title {
			return true
		}
	}
	return false
}

func mgmtState(t *testing.T, s tfsdk.State) ManagementIPResourceModel {
	t.Helper()
	var m ManagementIPResourceModel
	if diags := s.Get(context.Background(), &m); diags.HasError() {
		t.Fatalf("state: %v", diags)
	}
	return m
}

// --- Read -----------------------------------------------------------------

func TestManagementIPRead_Fixtures(t *testing.T) {
	tests := []struct {
		name   string
		res    func(*testing.T) (*ManagementIPResource, *sshclient.MockClient)
		ip, gw string
		dns    []string
	}{
		{"7150-c10zp", newC10ZPMgmt, "10.0.20.5/24", "10.0.20.1", nil},
		{"6430-c12", new6430Mgmt, "10.0.20.6/24", "10.0.20.9", []string{"10.0.20.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := tt.res(t)
			state, _ := mgmtStateAndPlan(t, r, mgmtModel("0.0.0.0/0", types.StringNull(), strList(), false), nil)
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("read: %v", resp.Diagnostics)
			}
			got := mgmtState(t, resp.State)
			if got.IPAddress.ValueString() != tt.ip {
				t.Errorf("ip_address = %v, want %s", got.IPAddress, tt.ip)
			}
			if got.DefaultGateway.ValueString() != tt.gw {
				t.Errorf("default_gateway = %v, want %s", got.DefaultGateway, tt.gw)
			}
			if !got.DNSServers.Equal(strList(tt.dns...)) {
				t.Errorf("dns_servers = %v, want %v", got.DNSServers, tt.dns)
			}
			if got.AllowManagementIPChange.ValueBool() {
				t.Error("allow_management_ip_change must read back false")
			}
		})
	}
}

// Stale state on a device now positively running the router image (a
// switch→router reimage) is removed, mirroring icx_interface_ve.
func TestManagementIPRead_RouterImageRemovesResource(t *testing.T) {
	r, mock := newMgmtResource(t, "7250-24", "08.0.95mT213", parser.ImageTypeRouter)
	state, _ := mgmtStateAndPlan(t, r, mgmtModel("10.0.20.5/24", types.StringNull(), strList(), false), nil)
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state")
	}
	if len(mock.Commands) != 0 {
		t.Errorf("no commands expected, got %v", mock.Commands)
	}
}

// --- Create ---------------------------------------------------------------

// Create matching the device exactly sends nothing: adopting the resource on
// an existing switch is a zero-diff, zero-command operation.
func TestManagementIPCreate_MatchingDeviceSendsNothing(t *testing.T) {
	r, mock := new6430Mgmt(t)
	plan := mgmtModel("10.0.20.6/24", types.StringValue("10.0.20.9"), strList("10.0.20.1"), false)
	state, p := mgmtStateAndPlan(t, r, nil, plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{"show running-config"})
	got := mgmtState(t, resp.State)
	if got.ID.ValueString() != "management" {
		t.Errorf("id = %v", got.ID)
	}
}

// Omitted gateway/DNS (unknown in the plan) adopt the device's values.
func TestManagementIPCreate_UnknownsAdoptDevice(t *testing.T) {
	r, mock := new6430Mgmt(t)
	plan := mgmtModel("10.0.20.6/24", types.StringUnknown(), types.ListUnknown(types.StringType), false)
	plan.AllowManagementIPChange = types.BoolUnknown()
	state, p := mgmtStateAndPlan(t, r, nil, plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{"show running-config"})
	got := mgmtState(t, resp.State)
	if got.DefaultGateway.ValueString() != "10.0.20.9" || !got.DNSServers.Equal(strList("10.0.20.1")) || got.AllowManagementIPChange.ValueBool() {
		t.Errorf("state = %+v", got)
	}
}

// Gateway and DNS differences are applied through the ordinary path and
// saved; the address, which matches, is never sent.
func TestManagementIPCreate_GatewayAndDNSOnly(t *testing.T) {
	r, mock := newC10ZPMgmt(t)
	plan := mgmtModel("10.0.20.5/24", types.StringValue("10.0.20.254"), strList("1.1.1.1", "9.9.9.9"), false)
	state, p := mgmtStateAndPlan(t, r, nil, plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{
		"show running-config",
		"ip default-gateway 10.0.20.254",
		"ip dns server-address 1.1.1.1 9.9.9.9",
		"write memory",
	})
	if len(mock.DisconnectBatches) != 0 {
		t.Errorf("no ip address batch expected, got %v", mock.DisconnectBatches)
	}
}

// A differing address without the allow flag is refused before any command.
func TestManagementIPCreate_AddressMismatchRefused(t *testing.T) {
	r, mock := newC10ZPMgmt(t)
	plan := mgmtModel("10.0.20.7/24", types.StringValue("10.0.20.254"), strList(), false)
	state, p := mgmtStateAndPlan(t, r, nil, plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if !hasErrorTitled(resp.Diagnostics, titleIPChangeRefused) {
		t.Fatalf("expected %q, got %v", titleIPChangeRefused, resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{"show running-config"})
}

// With the flag: other changes first, then the address through the
// disconnect-tolerant batch (which carries the save) and nothing after it.
// The session surviving means a clean success.
func TestManagementIPCreate_AddressChangeAllowedSessionSurvives(t *testing.T) {
	r, mock := newC10ZPMgmt(t)
	plan := mgmtModel("10.0.20.7/24", types.StringValue("10.0.20.254"), strList(), true)
	state, p := mgmtStateAndPlan(t, r, nil, plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{
		"show running-config",
		"ip default-gateway 10.0.20.254",
		"ip address 10.0.20.7 255.255.255.0",
		"end",
		"write memory",
	})
	if len(mock.DisconnectBatches) != 1 || mock.DisconnectBatches[0][0] != "ip address 10.0.20.7 255.255.255.0" {
		t.Errorf("ip address must go through ExecuteExpectingDisconnect, got %v", mock.DisconnectBatches)
	}
	if mock.WriteMemoryCalls != 1 {
		t.Errorf("exactly one save expected (inside the batch), got %d", mock.WriteMemoryCalls)
	}
}

// The expected case on a real switch: the session drops. The resource is
// recorded with the new address and a warning explains what to do next.
func TestManagementIPCreate_AddressChangeSevered(t *testing.T) {
	r, mock := newC10ZPMgmt(t)
	mock.SeverOnDisconnectBatch = true
	plan := mgmtModel("10.0.20.7/24", types.StringValue("10.0.20.1"), strList(), true)
	state, p := mgmtStateAndPlan(t, r, nil, plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a severed session is expected, not an error: %v", resp.Diagnostics)
	}
	if !hasWarningTitled(resp.Diagnostics, titleIPChangeSevered) {
		t.Fatalf("expected warning %q, got %v", titleIPChangeSevered, resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{"show running-config", "ip address 10.0.20.7 255.255.255.0"})
	got := mgmtState(t, resp.State)
	if got.IPAddress.ValueString() != "10.0.20.7/24" {
		t.Errorf("state ip_address = %v", got.IPAddress)
	}
	for _, d := range resp.Diagnostics.Warnings() {
		if !strings.Contains(d.Detail(), "write memory") || !strings.Contains(d.Detail(), "host") {
			t.Errorf("warning must carry the after-care instructions, got %q", d.Detail())
		}
	}
}

// Create on a known router image fails with the switch-image guard.
func TestManagementIPCreate_RouterImageRefused(t *testing.T) {
	r, mock := newMgmtResource(t, "7250-24", "08.0.95mT213", parser.ImageTypeRouter)
	plan := mgmtModel("10.0.20.5/24", types.StringNull(), strList(), false)
	state, p := mgmtStateAndPlan(t, r, nil, plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if !hasErrorTitled(resp.Diagnostics, "Switch image required") {
		t.Fatalf("expected 'Switch image required', got %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("no commands expected, got %v", mock.Commands)
	}
}

// --- Update ---------------------------------------------------------------

func TestManagementIPUpdate_GatewayAndDNS(t *testing.T) {
	r, mock := new6430Mgmt(t)
	st := mgmtModel("10.0.20.6/24", types.StringValue("10.0.20.9"), strList("10.0.20.1"), false)
	pl := mgmtModel("10.0.20.6/24", types.StringValue("10.0.20.1"), strList("10.0.20.1", "1.1.1.1"), false)
	state, plan := mgmtStateAndPlan(t, r, st, pl)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	assertCommands(t, "update", mock.Commands, []string{
		"ip default-gateway 10.0.20.1",
		"no ip dns server-address 10.0.20.1",
		"ip dns server-address 10.0.20.1 1.1.1.1",
		"write memory",
	})
}

// Removing the DNS servers and the gateway explicitly (null in plan, set in
// state) sends the `no` forms.
func TestManagementIPUpdate_RemoveGatewayAndDNS(t *testing.T) {
	r, mock := new6430Mgmt(t)
	st := mgmtModel("10.0.20.6/24", types.StringValue("10.0.20.9"), strList("10.0.20.1"), false)
	pl := mgmtModel("10.0.20.6/24", types.StringNull(), strList(), false)
	state, plan := mgmtStateAndPlan(t, r, st, pl)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	assertCommands(t, "update", mock.Commands, []string{
		"no ip default-gateway 10.0.20.9",
		"no ip dns server-address 10.0.20.1",
		"write memory",
	})
}

// Unknown (omitted) gateway/DNS in an update keep state and send nothing.
func TestManagementIPUpdate_UnknownsKeepState(t *testing.T) {
	r, mock := new6430Mgmt(t)
	st := mgmtModel("10.0.20.6/24", types.StringValue("10.0.20.9"), strList("10.0.20.1"), false)
	pl := mgmtModel("10.0.20.6/24", types.StringUnknown(), types.ListUnknown(types.StringType), false)
	state, plan := mgmtStateAndPlan(t, r, st, pl)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("expected no commands, got %v", mock.Commands)
	}
	got := mgmtState(t, resp.State)
	if got.DefaultGateway.ValueString() != "10.0.20.9" || !got.DNSServers.Equal(strList("10.0.20.1")) {
		t.Errorf("state = %+v", got)
	}
}

func TestManagementIPUpdate_AddressChangeRefusedWithoutFlag(t *testing.T) {
	r, mock := newC10ZPMgmt(t)
	st := mgmtModel("10.0.20.5/24", types.StringValue("10.0.20.1"), strList(), false)
	pl := mgmtModel("10.0.20.7/24", types.StringValue("10.0.20.1"), strList(), false)
	state, plan := mgmtStateAndPlan(t, r, st, pl)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if !hasErrorTitled(resp.Diagnostics, titleIPChangeRefused) {
		t.Fatalf("expected %q, got %v", titleIPChangeRefused, resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("no commands expected, got %v", mock.Commands)
	}
}

// Address change plus a DNS change: DNS first, address last in the batch.
func TestManagementIPUpdate_AddressChangeLastSevered(t *testing.T) {
	r, mock := newC10ZPMgmt(t)
	mock.SeverOnDisconnectBatch = true
	st := mgmtModel("10.0.20.5/24", types.StringValue("10.0.20.1"), strList(), false)
	pl := mgmtModel("10.0.20.7/24", types.StringValue("10.0.20.1"), strList("1.1.1.1"), true)
	state, plan := mgmtStateAndPlan(t, r, st, pl)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if !hasWarningTitled(resp.Diagnostics, titleIPChangeSevered) {
		t.Fatalf("expected warning %q, got %v", titleIPChangeSevered, resp.Diagnostics)
	}
	assertCommands(t, "update", mock.Commands, []string{
		"ip dns server-address 1.1.1.1",
		"ip address 10.0.20.7 255.255.255.0",
	})
	if mock.WriteMemoryCalls != 0 {
		t.Errorf("no separate write memory possible once severed, got %v", mock.Commands)
	}
	got := mgmtState(t, resp.State)
	if got.IPAddress.ValueString() != "10.0.20.7/24" {
		t.Errorf("state ip_address = %v", got.IPAddress)
	}
}

// --- Delete / Import ------------------------------------------------------

func TestManagementIPDelete_SendsNothing(t *testing.T) {
	r, mock := new6430Mgmt(t)
	st := mgmtModel("10.0.20.6/24", types.StringValue("10.0.20.9"), strList("10.0.20.1"), true)
	state, _ := mgmtStateAndPlan(t, r, st, nil)

	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 {
		t.Errorf("delete must send nothing, got %v", mock.Commands)
	}
}

func TestManagementIPImport(t *testing.T) {
	r, _ := new6430Mgmt(t)
	state, _ := mgmtStateAndPlan(t, r, nil, nil)

	resp := resource.ImportStateResponse{State: state}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "management"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import: %v", resp.Diagnostics)
	}
	var id string
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id != "management" {
		t.Errorf("id = %q", id)
	}

	bad := resource.ImportStateResponse{State: state}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "mgmt"}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Error("expected an error for a wrong import ID")
	}

	rr, _ := newMgmtResource(t, "7250-24", "08.0.95mT213", parser.ImageTypeRouter)
	router := resource.ImportStateResponse{State: state}
	rr.ImportState(context.Background(), resource.ImportStateRequest{ID: "management"}, &router)
	if !hasErrorTitled(router.Diagnostics, "Switch image required") {
		t.Errorf("import on a router image must fail with the guard, got %v", router.Diagnostics)
	}
}

// --- ModifyPlan -----------------------------------------------------------

// The image gate at plan time: only a positively detected router image blocks.
func TestManagementIPModifyPlan_ImageTypes(t *testing.T) {
	tests := []struct {
		name    string
		image   parser.ImageType
		wantErr bool
	}{
		{"switch", parser.ImageTypeSwitch, false},
		{"router", parser.ImageTypeRouter, true},
		{"unknown", parser.ImageTypeUnknown, false},
		{"unset", parser.ImageType(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, mock := newMgmtResource(t, "7150-c10zp", "08.0.95rT211", tt.image)
			_, plan := mgmtStateAndPlan(t, r, nil, mgmtModel("10.0.20.5/24", types.StringNull(), strList(), false))
			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: nullState(t, r)}, &resp)
			if got := hasErrorTitled(resp.Diagnostics, "Switch image required"); got != tt.wantErr {
				t.Errorf("Switch image error = %v, want %v (%v)", got, tt.wantErr, resp.Diagnostics)
			}
			if !tt.wantErr && resp.Diagnostics.HasError() {
				t.Errorf("unexpected errors: %v", resp.Diagnostics)
			}
			if tt.wantErr && len(mock.Commands) != 0 {
				t.Errorf("a refused plan must not touch the device, got %v", mock.Commands)
			}
		})
	}
}

func nullState(t *testing.T, r *ManagementIPResource) tfsdk.State {
	t.Helper()
	s, _ := mgmtStateAndPlan(t, r, nil, nil)
	return s
}

// Destroy plans always pass, even on a router image.
func TestManagementIPModifyPlan_DestroyAllowed(t *testing.T) {
	r, _ := newMgmtResource(t, "7250-24", "08.0.95mT213", parser.ImageTypeRouter)
	state, plan := mgmtStateAndPlan(t, r, mgmtModel("10.0.20.5/24", types.StringNull(), strList(), false), nil)
	plan = nullPlan(tfsdk.Plan{Schema: state.Schema})
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("destroy plan must pass, got %v", resp.Diagnostics)
	}
}

// On create the device is consulted so an address mismatch is caught in plan.
func TestManagementIPModifyPlan_CreateMismatchRefused(t *testing.T) {
	r, mock := newC10ZPMgmt(t)
	_, plan := mgmtStateAndPlan(t, r, nil, mgmtModel("10.0.20.7/24", types.StringNull(), strList(), false))
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: nullState(t, r)}, &resp)
	if !hasErrorTitled(resp.Diagnostics, titleIPChangeRefused) {
		t.Fatalf("expected %q, got %v", titleIPChangeRefused, resp.Diagnostics)
	}
	assertCommands(t, "plan", mock.Commands, []string{"show running-config"})

	// Matching address: clean plan.
	r, _ = newC10ZPMgmt(t)
	_, plan = mgmtStateAndPlan(t, r, nil, mgmtModel("10.0.20.5/24", types.StringNull(), strList(), false))
	resp = resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: nullState(t, r)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("matching address must plan cleanly, got %v", resp.Diagnostics)
	}
}

// On update: refused without the flag, warned with it, silent when unchanged.
func TestManagementIPModifyPlan_UpdateAddressChange(t *testing.T) {
	r, _ := newC10ZPMgmt(t)
	st := mgmtModel("10.0.20.5/24", types.StringValue("10.0.20.1"), strList(), false)

	state, plan := mgmtStateAndPlan(t, r, st, mgmtModel("10.0.20.7/24", types.StringValue("10.0.20.1"), strList(), false))
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)
	if !hasErrorTitled(resp.Diagnostics, titleIPChangeRefused) {
		t.Errorf("expected %q, got %v", titleIPChangeRefused, resp.Diagnostics)
	}

	state, plan = mgmtStateAndPlan(t, r, st, mgmtModel("10.0.20.7/24", types.StringValue("10.0.20.1"), strList(), true))
	resp = resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() || len(resp.Diagnostics.Warnings()) != 1 {
		t.Errorf("expected exactly one warning and no error, got %v", resp.Diagnostics)
	}

	state, plan = mgmtStateAndPlan(t, r, st, mgmtModel("10.0.20.5/24", types.StringValue("10.0.20.254"), strList(), false))
	resp = resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &resp)
	if len(resp.Diagnostics) != 0 {
		t.Errorf("gateway-only change must plan silently, got %v", resp.Diagnostics)
	}
}

// --- helpers --------------------------------------------------------------

func TestDNSServerCommands(t *testing.T) {
	tests := []struct {
		name     string
		from, to []string
		want     []string
	}{
		{"equal", []string{"a"}, []string{"a"}, nil},
		{"both empty", nil, nil, nil},
		{"add first", nil, []string{"1.1.1.1", "9.9.9.9"}, []string{"ip dns server-address 1.1.1.1 9.9.9.9"}},
		{"remove all", []string{"1.1.1.1", "9.9.9.9"}, nil, []string{"no ip dns server-address 1.1.1.1", "no ip dns server-address 9.9.9.9"}},
		{"reorder", []string{"a", "b"}, []string{"b", "a"}, []string{"no ip dns server-address a", "no ip dns server-address b", "ip dns server-address b a"}},
	}
	for _, tt := range tests {
		if got := dnsServerCommands(tt.from, tt.to); !equalStrings(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestGatewayCommands(t *testing.T) {
	v := types.StringValue
	tests := []struct {
		name     string
		from, to types.String
		want     []string
	}{
		{"equal", v("10.0.0.1"), v("10.0.0.1"), nil},
		{"both null", types.StringNull(), types.StringNull(), nil},
		{"set", types.StringNull(), v("10.0.0.1"), []string{"ip default-gateway 10.0.0.1"}},
		{"replace", v("10.0.0.1"), v("10.0.0.2"), []string{"ip default-gateway 10.0.0.2"}},
		{"remove", v("10.0.0.1"), types.StringNull(), []string{"no ip default-gateway 10.0.0.1"}},
	}
	for _, tt := range tests {
		if got := gatewayCommands(tt.from, tt.to); !equalStrings(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestGlobalIPToCIDR(t *testing.T) {
	tests := []struct{ in, want string }{
		{"10.0.20.5 255.255.255.0", "10.0.20.5/24"},
		{"10.0.20.5 255.255.255.0 dynamic", "10.0.20.5/24"},
		{"192.168.1.10 255.255.0.0", "192.168.1.10/16"},
		{"", ""},
		{"10.0.20.5", ""},
		{"nonsense here", ""},
	}
	for _, tt := range tests {
		if got := globalIPToCIDR(tt.in); got != tt.want {
			t.Errorf("globalIPToCIDR(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
