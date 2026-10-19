package resource

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// stripDialectEvidence removes the `ver` line and every `dual-mode` line from
// a fixture, so the only dialect signal left is the provider's.
func stripDialectEvidence(cfg string) string {
	cfg = regexp.MustCompile(`(?m)^ver .*\n`).ReplaceAllString(cfg, "")
	return regexp.MustCompile(`(?m)^ dual-mode.*\n`).ReplaceAllString(cfg, "")
}

func createEthWithDialect(t *testing.T, cfg string, d dialect.Dialect, plan *InterfaceEthernetResourceModel) []string {
	t.Helper()
	mock := sshclient.NewMockClient(cfg)
	r := &InterfaceEthernetResource{client: mock, dialect: d}
	_, p := ethStateAndPlan(t, r, nil, plan)
	state, _ := ethStateAndPlan(t, r, nil, nil)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Create error: %v", resp.Diagnostics)
	}
	return cliOf(mock)
}

// The Configure-time dialect is authoritative: with no version or dual-mode
// line in the config, an 08.0.30 provider dialect still selects dual-mode.
func TestInterfaceEthernet_ProviderDialectSelectsDualMode(t *testing.T) {
	cfg := stripDialectEvidence(loadFixture(t, "6430-c12"))
	cmds := createEthWithDialect(t, cfg, dialect.Detect("08.0.30uT311", parser.ImageTypeSwitch),
		ethModel("1/1/7", 102, []int64{110, 101}, false))
	if indexOf(cmds, "dual-mode 102") < 0 {
		t.Errorf("expected dual-mode with an 08.0.30 provider dialect, got %q", cmds)
	}
	for _, c := range cmds {
		if strings.HasPrefix(c, "untagged ethe") {
			t.Errorf("dual-mode dialect must not emit %q", c)
		}
	}
}

// No evidence anywhere (unknown dialect): the 08.0.9x VLAN-context form.
func TestInterfaceEthernet_UnknownDialectUses0809xForm(t *testing.T) {
	cfg := stripDialectEvidence(loadFixture(t, "6430-c12"))
	cmds := createEthWithDialect(t, cfg, dialect.Dialect{},
		ethModel("1/1/7", 102, []int64{110, 101}, false))
	for _, c := range cmds {
		if strings.HasPrefix(c, "dual-mode") {
			t.Errorf("unknown dialect must never emit dual-mode, got %q", cmds)
		}
	}
	if indexOf(cmds, "untagged ethe 1/1/7") < 0 {
		t.Errorf("expected VLAN-context untagged membership, got %q", cmds)
	}
}

func systemWithDialect(version string, image parser.ImageType) (*SystemResource, *sshclient.MockClient) {
	mock := sshclient.NewMockClient("")
	return &SystemResource{client: mock, dialect: dialect.Detect(version, image)}, mock
}

// The DHCP client command is rendered per dialect on create, update, and
// delete. Each operation's full command stream is compared exactly, so a
// stray extra command (or a second DHCP spelling) fails the test.
func TestSystem_DHCPClientDialect(t *testing.T) {
	tests := []struct {
		name, version   string
		image           parser.ImageType
		disable, enable string
	}{
		{"08.0.95 router", "08.0.95qT213", parser.ImageTypeRouter, "ip dhcp-client disable", "no ip dhcp-client disable"},
		{"08.0.95 switch", "08.0.95rT211", parser.ImageTypeSwitch, "ip dhcp-client disable", "no ip dhcp-client disable"},
		{"6450 08.0.30 router", "08.0.30uT313", parser.ImageTypeRouter, "ip dhcp-client disable", "no ip dhcp-client disable"},
		{"6430 08.0.30 switch", "08.0.30uT311", parser.ImageTypeSwitch, "no ip dhcp-client enable", "ip dhcp-client enable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			on := systemModel(false, true)
			on.DHCPClientDisable = types.BoolValue(true)
			off := systemModel(false, true)

			// Create.
			r, mock := systemWithDialect(tt.version, tt.image)
			state, plan := systemStateAndPlan(t, r, off, on)
			cresp := resource.CreateResponse{State: state}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &cresp)
			if cresp.Diagnostics.HasError() {
				t.Fatalf("create: %v", cresp.Diagnostics)
			}
			assertCommands(t, "create", mock.Commands, []string{tt.disable, "write memory"})

			// Update true -> false.
			r, mock = systemWithDialect(tt.version, tt.image)
			state, plan = systemStateAndPlan(t, r, on, off)
			uresp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &uresp)
			if uresp.Diagnostics.HasError() {
				t.Fatalf("update: %v", uresp.Diagnostics)
			}
			assertCommands(t, "update", mock.Commands, []string{tt.enable, "write memory"})

			// Delete resets to enabled.
			r, mock = systemWithDialect(tt.version, tt.image)
			state, _ = systemStateAndPlan(t, r, on, nil)
			dresp := resource.DeleteResponse{State: state}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &dresp)
			if dresp.Diagnostics.HasError() {
				t.Fatalf("delete: %v", dresp.Diagnostics)
			}
			assertCommands(t, "delete", mock.Commands, []string{tt.enable, "write memory"})
		})
	}
}

// assertCommands fails unless got is exactly want, in order.
func assertCommands(t *testing.T, op string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s: commands = %q, want exactly %q", op, got, want)
	}
}

// manager_* attributes are refused at plan time on FastIron 08.0.30 and
// accepted on 08.0.9x and an unknown dialect; defaults never trip the guard.
func TestSystemModifyPlan_Manager(t *testing.T) {
	registrar := systemModel(false, true)
	registrar.ManagerRegistrar = types.BoolValue(true)
	portList := systemModel(false, true)
	portList.ManagerPortList = types.StringValue("987")

	tests := []struct {
		name, version string
		plan          *SystemResourceModel
		wantErr       bool
	}{
		{"08.0.30 registrar", "08.0.30uT311", registrar, true},
		{"08.0.30 port-list", "08.0.30uT313", portList, true},
		{"08.0.30 defaults", "08.0.30uT311", systemModel(false, true), false},
		{"08.0.95 registrar", "08.0.95qT213", registrar, false},
		{"unknown registrar", "", registrar, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, mock := systemWithDialect(tt.version, parser.ImageTypeRouter)
			_, plan := systemStateAndPlan(t, r, systemModel(false, true), tt.plan)
			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan}, &resp)
			if got := resp.Diagnostics.HasError(); got != tt.wantErr {
				t.Errorf("error = %v, want %v (%v)", got, tt.wantErr, resp.Diagnostics)
			}
			if len(mock.Commands) != 0 {
				t.Errorf("ModifyPlan must not send commands, got %v", mock.Commands)
			}

			// The apply-time backstop agrees and sends nothing.
			if tt.wantErr {
				state, plan := systemStateAndPlan(t, r, systemModel(false, true), tt.plan)
				uresp := resource.UpdateResponse{State: state}
				r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &uresp)
				if !uresp.Diagnostics.HasError() || len(mock.Commands) != 0 {
					t.Errorf("update backstop: diags %v, commands %v", uresp.Diagnostics, mock.Commands)
				}
			}
		})
	}
}
