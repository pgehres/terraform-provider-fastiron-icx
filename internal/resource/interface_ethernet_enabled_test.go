package resource

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// Admin state (`enabled`) on icx_interface_ethernet: adoption of the device's
// state when omitted, exact command sequences when set, and the plan-time
// refusal to disable the management path.

// withDisabled returns the 6430 fixture with port 1/1/7 (an access port with
// no VLAN membership) administratively disabled.
func withDisabled(t *testing.T) string {
	t.Helper()
	cfg := loadFixture(t, "6430-c12")
	if !strings.Contains(cfg, "interface ethernet 1/1/7\n") {
		t.Fatal("fixture changed: no interface ethernet 1/1/7 stanza")
	}
	return strings.Replace(cfg, "interface ethernet 1/1/7\n", "interface ethernet 1/1/7\n disable\n", 1)
}

func ethEnabledModel(port string, enabled types.Bool) *InterfaceEthernetResourceModel {
	m := ethModel(port, 0, nil, false)
	m.Enabled = enabled
	return m
}

func newEthResourceWithConfig(cfg string) (*InterfaceEthernetResource, *sshclient.MockClient) {
	mock := sshclient.NewMockClient(cfg)
	mock.Responses["show arp"] = arpSwitch // gateway 10.0.20.9 via 1/2/1
	return &InterfaceEthernetResource{client: mock}, mock
}

func TestInterfaceEthernetRead_Enabled(t *testing.T) {
	ctx := context.Background()
	read := func(cfg, port string) InterfaceEthernetResourceModel {
		r, _ := newEthResourceWithConfig(cfg)
		state, _ := ethStateAndPlan(t, r, ethModel(port, 0, nil, false), nil)
		resp := resource.ReadResponse{State: state}
		r.Read(ctx, resource.ReadRequest{State: state}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("Read: %v", resp.Diagnostics)
		}
		var got InterfaceEthernetResourceModel
		resp.State.Get(ctx, &got)
		return got
	}
	if got := read(withDisabled(t), "1/1/7"); !boolKnown(got.Enabled) || got.Enabled.ValueBool() {
		t.Errorf("disabled port: enabled = %v, want false", got.Enabled)
	}
	if got := read(withDisabled(t), "1/1/1"); !boolKnown(got.Enabled) || !got.Enabled.ValueBool() {
		t.Errorf("configured port: enabled = %v, want true", got.Enabled)
	}
	if got := read(loadFixture(t, "7250-24"), "1/1/9"); !boolKnown(got.Enabled) || !got.Enabled.ValueBool() {
		t.Errorf("port absent from config: enabled = %v, want true", got.Enabled)
	}
}

func createEthWith(t *testing.T, cfg string, plan *InterfaceEthernetResourceModel) (InterfaceEthernetResourceModel, *sshclient.MockClient, resource.CreateResponse) {
	t.Helper()
	r, mock := newEthResourceWithConfig(cfg)
	_, p := ethStateAndPlan(t, r, nil, plan)
	state, _ := ethStateAndPlan(t, r, nil, nil)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	var got InterfaceEthernetResourceModel
	if !resp.Diagnostics.HasError() {
		resp.State.Get(context.Background(), &got)
	}
	return got, mock, resp
}

func TestInterfaceEthernetCreate_EnabledOmittedAdoptsDevice(t *testing.T) {
	// Unknown (omitted) on a disabled port: recorded as false, nothing sent
	// for it — and no `show arp`, since nothing is being disabled.
	got, mock, resp := createEthWith(t, withDisabled(t), ethEnabledModel("1/1/7", types.BoolUnknown()))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if boolKnown(got.Enabled) && got.Enabled.ValueBool() {
		t.Errorf("enabled = %v, want false (adopted)", got.Enabled)
	}
	if want := []string{"interface ethernet 1/1/7", "exit"}; !reflect.DeepEqual(cliOf(mock), want) {
		t.Errorf("commands = %q, want %q", cliOf(mock), want)
	}
	// Unknown on an enabled port: recorded true.
	got, _, _ = createEthWith(t, loadFixture(t, "7250-24"), ethEnabledModel("1/1/9", types.BoolUnknown()))
	if !boolKnown(got.Enabled) || !got.Enabled.ValueBool() {
		t.Errorf("enabled = %v, want true (adopted)", got.Enabled)
	}
}

func TestInterfaceEthernetCreate_EnabledExplicit(t *testing.T) {
	// false on a non-management port: disable, after the show arp check.
	_, mock, resp := createEthWith(t, withDisabled(t), ethEnabledModel("1/1/3", types.BoolValue(false)))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	want := []string{"show arp", "interface ethernet 1/1/3", "disable", "exit"}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	// true on a disabled port: enable.
	_, mock, _ = createEthWith(t, withDisabled(t), ethEnabledModel("1/1/7", types.BoolValue(true)))
	want = []string{"interface ethernet 1/1/7", "enable", "exit"}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	// true on an already-enabled port: nothing extra.
	_, mock, _ = createEthWith(t, withDisabled(t), ethEnabledModel("1/1/3", types.BoolValue(true)))
	want = []string{"interface ethernet 1/1/3", "exit"}
	if got := cliOf(mock); !reflect.DeepEqual(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
}

// Disabling the uplink the gateway is reached through (1/2/1 per show arp) is
// refused before any configuration command, at create and at plan time.
func TestInterfaceEthernet_RefusesManagementPathDisable(t *testing.T) {
	_, mock, resp := createEthWith(t, withDisabled(t), ethEnabledModel("1/2/1", types.BoolValue(false)))
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected refusal")
	}
	if got := cliOf(mock); !reflect.DeepEqual(got, []string{"show arp"}) {
		t.Errorf("commands = %q, want only show arp", got)
	}
	if mock.WriteMemoryCalls != 0 {
		t.Error("write memory must not run")
	}

	r, mock := newEthResourceWithConfig(withDisabled(t))
	state, plan := ethStateAndPlan(t, r, ethEnabledModel("1/2/1", types.BoolValue(true)), ethEnabledModel("1/2/1", types.BoolValue(false)))
	mresp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &mresp)
	if !mresp.Diagnostics.HasError() {
		t.Fatalf("ModifyPlan should refuse, got %v", mresp.Diagnostics)
	}
	for _, c := range mock.Commands {
		if c != "show running-config" && c != "show arp" {
			t.Errorf("ModifyPlan sent %q", c)
		}
	}

	// Plans that do not disable anything never read the device.
	for _, tt := range []struct {
		name        string
		state, plan *InterfaceEthernetResourceModel
	}{
		{"already disabled", ethEnabledModel("1/2/1", types.BoolValue(false)), ethEnabledModel("1/2/1", types.BoolValue(false))},
		{"enabling", ethEnabledModel("1/2/1", types.BoolValue(false)), ethEnabledModel("1/2/1", types.BoolValue(true))},
		{"unknown", ethEnabledModel("1/2/1", types.BoolValue(true)), ethEnabledModel("1/2/1", types.BoolUnknown())},
	} {
		r, mock := newEthResourceWithConfig(withDisabled(t))
		state, plan := ethStateAndPlan(t, r, tt.state, tt.plan)
		mresp := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &mresp)
		if len(mresp.Diagnostics) != 0 || len(mock.Commands) != 0 {
			t.Errorf("%s: diags %v, commands %v", tt.name, mresp.Diagnostics, mock.Commands)
		}
	}
	// Destroy plan.
	r, mock = newEthResourceWithConfig(withDisabled(t))
	state, plan = ethStateAndPlan(t, r, ethEnabledModel("1/2/1", types.BoolValue(true)), nil)
	mresp = resource.ModifyPlanResponse{Plan: nullPlan(plan)}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: nullPlan(plan), State: state}, &mresp)
	if len(mresp.Diagnostics) != 0 || len(mock.Commands) != 0 {
		t.Errorf("destroy: diags %v, commands %v", mresp.Diagnostics, mock.Commands)
	}
}

// With no next hop resolvable, disabling is allowed with a warning.
func TestInterfaceEthernetModifyPlan_WarnsWhenUnverifiable(t *testing.T) {
	r, mock := newEthResourceWithConfig(withDisabled(t))
	mock.Responses["show arp"] = "Total Arp Entries : 0\n"
	state, plan := ethStateAndPlan(t, r, ethEnabledModel("1/1/3", types.BoolValue(true)), ethEnabledModel("1/1/3", types.BoolValue(false)))
	mresp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: state}, &mresp)
	if mresp.Diagnostics.HasError() || mresp.Diagnostics.WarningsCount() != 1 {
		t.Errorf("want one warning and no error, got %v", mresp.Diagnostics)
	}
}

func updateEthEnabled(t *testing.T, cfg string, state, plan *InterfaceEthernetResourceModel) (InterfaceEthernetResourceModel, []string, resource.UpdateResponse) {
	t.Helper()
	r, mock := newEthResourceWithConfig(cfg)
	s, p := ethStateAndPlan(t, r, state, plan)
	resp := resource.UpdateResponse{State: s}
	r.Update(context.Background(), resource.UpdateRequest{Plan: p, State: s}, &resp)
	var got InterfaceEthernetResourceModel
	if !resp.Diagnostics.HasError() {
		resp.State.Get(context.Background(), &got)
	}
	return got, cliOf(mock), resp
}

func TestInterfaceEthernetUpdate_Enabled(t *testing.T) {
	cfg := withDisabled(t)
	// true -> false: show arp check, then disable.
	_, cmds, resp := updateEthEnabled(t, cfg, ethEnabledModel("1/1/3", types.BoolValue(true)), ethEnabledModel("1/1/3", types.BoolValue(false)))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if want := []string{"show arp", "interface ethernet 1/1/3", "disable", "exit"}; !reflect.DeepEqual(cmds, want) {
		t.Errorf("disable: %q, want %q", cmds, want)
	}
	// false -> true: enable, no arp check.
	_, cmds, _ = updateEthEnabled(t, cfg, ethEnabledModel("1/1/7", types.BoolValue(false)), ethEnabledModel("1/1/7", types.BoolValue(true)))
	if want := []string{"interface ethernet 1/1/7", "enable", "exit"}; !reflect.DeepEqual(cmds, want) {
		t.Errorf("enable: %q, want %q", cmds, want)
	}
	// unknown: keeps state's value, sends nothing.
	got, cmds, _ := updateEthEnabled(t, cfg, ethEnabledModel("1/1/7", types.BoolValue(false)), ethEnabledModel("1/1/7", types.BoolUnknown()))
	if len(cmds) != 0 {
		t.Errorf("unknown: sent %q", cmds)
	}
	if !boolKnown(got.Enabled) || got.Enabled.ValueBool() {
		t.Errorf("unknown: enabled = %v, want false kept from state", got.Enabled)
	}
	// Refused on the management path, before any command.
	_, cmds, resp = updateEthEnabled(t, cfg, ethEnabledModel("1/2/1", types.BoolValue(true)), ethEnabledModel("1/2/1", types.BoolValue(false)))
	if !resp.Diagnostics.HasError() || !reflect.DeepEqual(cmds, []string{"show arp"}) {
		t.Errorf("mgmt path: diags %v, commands %q", resp.Diagnostics, cmds)
	}
}

func TestInterfaceEthernetDelete_ReenablesPort(t *testing.T) {
	cfg := withDisabled(t)
	for _, tt := range []struct {
		name    string
		enabled types.Bool
		want    []string
	}{
		{"disabled", types.BoolValue(false), []string{"interface ethernet 1/1/7", "enable", "exit"}},
		{"enabled", types.BoolValue(true), []string{"interface ethernet 1/1/7", "exit"}},
		{"unrecorded", types.BoolNull(), []string{"interface ethernet 1/1/7", "exit"}},
	} {
		r, mock := newEthResourceWithConfig(cfg)
		state, _ := ethStateAndPlan(t, r, ethEnabledModel("1/1/7", tt.enabled), nil)
		resp := resource.DeleteResponse{State: state}
		r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", tt.name, resp.Diagnostics)
		}
		if got := cliOf(mock); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}
