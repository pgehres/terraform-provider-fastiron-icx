package resource

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// poeShow is `show inline power <port>` as FastIron 09.0.10k prints it on an
// ICX 7450.
func poeShow(port, admin string) string {
	return " Port\tAdmin \tOper    ---Power(mWatts)---  PD Type  PD Class     Pri  Fault/\n" +
		"     \tState \tState   Consumed  Allocated                             Error\n" +
		"-----------------------------------------------------------------------------\n" +
		" " + port + "\t" + admin + " \tOff            0          0  n/a      n/a            3  n/a\n"
}

func poeModel(port string, enabled bool, limit, priority int64) *PoEResourceModel {
	m := &PoEResourceModel{
		ID:         types.StringValue(port),
		Port:       types.StringValue(port),
		Enabled:    types.BoolValue(enabled),
		PowerLimit: types.Int64Null(),
		Priority:   types.Int64Null(),
	}
	if limit != 0 {
		m.PowerLimit = types.Int64Value(limit)
	}
	if priority != 0 {
		m.Priority = types.Int64Value(priority)
	}
	return m
}

func poeStateAndPlan(t *testing.T, r *PoEResource, state, plan *PoEResourceModel) (tfsdk.State, tfsdk.Plan) {
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
			t.Fatalf("state: %v", diags)
		}
	}
	p := tfsdk.Plan{Schema: schemaResp.Schema}
	if plan != nil {
		if diags := p.Set(ctx, plan); diags.HasError() {
			t.Fatalf("plan: %v", diags)
		}
	}
	return s, p
}

func poeRead(t *testing.T, mock *sshclient.MockClient, prior *PoEResourceModel) PoEResourceModel {
	t.Helper()
	r := &PoEResource{client: mock}
	state, _ := poeStateAndPlan(t, r, prior, nil)
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	var got PoEResourceModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatal(diags)
	}
	return got
}

// Read reports what the switch has, so changes made on the switch show up.
func TestPoERead(t *testing.T) {
	tests := []struct {
		name     string
		stanza   string
		admin    string
		enabled  bool
		limit    int64
		priority int64
	}{
		{"default on, no stanza", "", "On", true, 0, 0},
		{"disabled on the switch", "interface ethernet 1/1/5\n no inline power\n!\n", "Off", false, 0, 0},
		{"power limit", "interface ethernet 1/1/5\n inline power power-limit 25000\n!\n", "On", true, 25000, 0},
		{"priority", "interface ethernet 1/1/5\n inline power priority 2\n!\n", "On", true, 0, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := sshclient.NewMockClient("ver 09.0.10k_cd3T211\n!\n" + tt.stanza + "end\n")
			mock.Responses["show inline power 1/1/5"] = poeShow("1/1/5", tt.admin)
			// The prior state says the opposite of the device everywhere.
			got := poeRead(t, mock, poeModel("1/1/5", !tt.enabled, 1000, 1))
			want := poeModel("1/1/5", tt.enabled, tt.limit, tt.priority)
			if !got.Enabled.Equal(want.Enabled) || !got.PowerLimit.Equal(want.PowerLimit) || !got.Priority.Equal(want.Priority) {
				t.Errorf("read = %+v, want %+v", got, *want)
			}
		})
	}
}

func TestPoEReadNotAPoEPort(t *testing.T) {
	mock := sshclient.NewMockClient("ver 09.0.10k_cd3T211\n!\nend\n")
	mock.Responses["show inline power 1/2/1"] = "Error - Port 1/2/1 is not a PoE port\n"
	r := &PoEResource{client: mock}
	state, _ := poeStateAndPlan(t, r, poeModel("1/2/1", true, 0, 0), nil)
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not a PoE port") {
		t.Errorf("want a not-a-PoE-port error, got %v", resp.Diagnostics)
	}
}

// One command sets the whole state; `no inline power <option>` is never sent
// (on FastIron 09.0.10 it turns PoE off).
func TestPoECreateAndUpdateCommands(t *testing.T) {
	tests := []struct {
		name  string
		state *PoEResourceModel
		plan  *PoEResourceModel
		want  []string
	}{
		{"create default", nil, poeModel("1/1/1", true, 0, 0), []string{"interface ethernet 1/1/1", "inline power", "exit"}},
		{"create disabled", nil, poeModel("1/1/1", false, 0, 0), []string{"interface ethernet 1/1/1", "no inline power", "exit"}},
		{"create limit", nil, poeModel("1/1/1", true, 60000, 0), []string{"interface ethernet 1/1/1", "inline power power-limit 60000", "exit"}},
		{"create priority", nil, poeModel("1/1/1", true, 0, 1), []string{"interface ethernet 1/1/1", "inline power priority 1", "exit"}},
		{"remove limit", poeModel("1/1/1", true, 60000, 0), poeModel("1/1/1", true, 0, 0), []string{"interface ethernet 1/1/1", "inline power", "exit"}},
		{"limit to priority", poeModel("1/1/1", true, 60000, 0), poeModel("1/1/1", true, 0, 2), []string{"interface ethernet 1/1/1", "inline power priority 2", "exit"}},
		{"disable", poeModel("1/1/1", true, 0, 2), poeModel("1/1/1", false, 0, 0), []string{"interface ethernet 1/1/1", "no inline power", "exit"}},
		{"no change", poeModel("1/1/1", true, 0, 2), poeModel("1/1/1", true, 0, 2), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := sshclient.NewMockClient("")
			r := &PoEResource{client: mock}
			state, plan := poeStateAndPlan(t, r, tt.state, tt.plan)
			if tt.state == nil {
				resp := resource.CreateResponse{State: state}
				r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
				if resp.Diagnostics.HasError() {
					t.Fatal(resp.Diagnostics)
				}
			} else {
				resp := resource.UpdateResponse{State: state}
				r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
				if resp.Diagnostics.HasError() {
					t.Fatal(resp.Diagnostics)
				}
			}
			// The mock records the save with the commands; it follows every
			// change and only a change.
			want := tt.want
			if want != nil {
				want = append(want, "write memory")
			}
			assertCommands(t, tt.name, mock.Commands, want)
		})
	}
}

func TestPoEDeleteResetsToDefault(t *testing.T) {
	for _, prior := range []*PoEResourceModel{
		poeModel("1/1/7", true, 60000, 0),
		poeModel("1/1/7", true, 0, 2),
		poeModel("1/1/7", false, 0, 0),
	} {
		mock := sshclient.NewMockClient("")
		r := &PoEResource{client: mock}
		state, _ := poeStateAndPlan(t, r, prior, nil)
		resp := resource.DeleteResponse{State: state}
		r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		assertCommands(t, "delete", mock.Commands, []string{"interface ethernet 1/1/7", "inline power", "exit", "write memory"})
	}
}

func TestPoEConfigValidators(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *PoEResourceModel
		wantErr string
	}{
		{"limit only", poeModel("1/1/1", true, 60000, 0), ""},
		{"priority only", poeModel("1/1/1", true, 0, 2), ""},
		{"both", poeModel("1/1/1", true, 60000, 2), "These attributes cannot be configured together"},
		{"limit on disabled port", poeModel("1/1/1", false, 60000, 0), "PoE option on a disabled port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &PoEResource{}
			_, plan := poeStateAndPlan(t, r, nil, tt.cfg)
			cfg := tfsdk.Config(plan)
			var errs []string
			for _, v := range r.ConfigValidators(context.Background()) {
				resp := resource.ValidateConfigResponse{}
				v.ValidateResource(context.Background(), resource.ValidateConfigRequest{Config: cfg}, &resp)
				for _, d := range resp.Diagnostics.Errors() {
					errs = append(errs, d.Summary()+": "+d.Detail())
				}
			}
			joined := strings.Join(errs, "\n")
			if tt.wantErr == "" && joined != "" {
				t.Errorf("unexpected errors: %s", joined)
			}
			if tt.wantErr != "" && !strings.Contains(joined, tt.wantErr) {
				t.Errorf("errors = %q, want one containing %q", joined, tt.wantErr)
			}
		})
	}
}
