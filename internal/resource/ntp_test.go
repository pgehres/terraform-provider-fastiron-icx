package resource

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

func newNTPResource(cfg string, d dialect.Dialect) (*NTPResource, *sshclient.MockClient) {
	mock := sshclient.NewMockClient(cfg)
	return &NTPResource{client: mock, dialect: d}, mock
}

// ntpModel builds a model; servers == nil is a null list, tz == "" null.
func ntpModel(servers []string, serve bool, tz string, summer bool) *NTPResourceModel {
	m := &NTPResourceModel{
		ID:         types.StringValue("ntp"),
		Servers:    types.ListNull(types.StringType),
		Serve:      types.BoolValue(serve),
		Timezone:   types.StringNull(),
		SummerTime: types.BoolValue(summer),
	}
	if servers != nil {
		list, diags := types.ListValueFrom(context.Background(), types.StringType, servers)
		if diags.HasError() {
			panic(diags)
		}
		m.Servers = list
	}
	if tz != "" {
		m.Timezone = types.StringValue(tz)
	}
	return m
}

func ntpStateAndPlan(t *testing.T, r *NTPResource, state, plan *NTPResourceModel) (tfsdk.State, tfsdk.Plan) {
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

func TestNTPRead_6430Fixture(t *testing.T) {
	ctx := context.Background()
	r, _ := newNTPResource(loadFixture(t, "6430-c12"), dialect.Dialect{})
	// Configured spelling "us pacific" is kept when it matches the device's "us Pacific".
	state, _ := ntpStateAndPlan(t, r, ntpModel(nil, true, "us pacific", false), nil)
	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got NTPResourceModel
	resp.State.Get(ctx, &got)
	servers := listToStringSlice(ctx, got.Servers, &resp.Diagnostics)
	if want := []string{"216.239.35.0", "216.239.35.4"}; !reflect.DeepEqual(servers, want) {
		t.Errorf("servers = %v, want %v", servers, want)
	}
	if got.Serve.ValueBool() {
		t.Error("serve should be false (disable serve)")
	}
	if got.Timezone.ValueString() != "us pacific" {
		t.Errorf("timezone = %q, want configured spelling kept", got.Timezone.ValueString())
	}
	if !got.SummerTime.ValueBool() {
		t.Error("summer_time should be true")
	}

	// Without a configured value the device's spelling is reported.
	state, _ = ntpStateAndPlan(t, r, ntpModel(nil, true, "", false), nil)
	resp = resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)
	resp.State.Get(ctx, &got)
	if got.Timezone.ValueString() != "us Pacific" {
		t.Errorf("timezone = %q, want device spelling", got.Timezone.ValueString())
	}
}

// A device with nothing configured reads back as the schema defaults, so an
// empty icx_ntp plans clean against it.
func TestNTPRead_Defaults(t *testing.T) {
	ctx := context.Background()
	r, _ := newNTPResource(loadFixture(t, "7250-24"), dialect.Dialect{})
	state, _ := ntpStateAndPlan(t, r, ntpModel([]string{"1.2.3.4"}, false, "gmt gmt+01", true), nil)
	resp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &resp)
	var got NTPResourceModel
	resp.State.Get(ctx, &got)
	if !got.Servers.IsNull() || !got.Serve.ValueBool() || !got.Timezone.IsNull() || got.SummerTime.ValueBool() {
		t.Errorf("got %+v, want defaults", got)
	}
}

func TestNTPCreate(t *testing.T) {
	r, mock := newNTPResource("", dialect.Detect("08.0.95mT213", parser.ImageTypeRouter))
	plan := ntpModel([]string{"216.239.35.0", "216.239.35.4"}, false, "us Pacific", true)
	state, p := ntpStateAndPlan(t, r, nil, plan)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{
		"clock timezone us pacific",
		"clock summer-time",
		"ntp",
		"server 216.239.35.0",
		"server 216.239.35.4",
		"disable serve",
		"exit",
		"write memory",
	})

	// Nothing set: nothing sent, no save.
	r, mock = newNTPResource("", dialect.Dialect{})
	state, p = ntpStateAndPlan(t, r, nil, ntpModel(nil, true, "", false))
	resp = resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() || len(mock.Commands) != 0 {
		t.Errorf("empty create: diags %v, commands %v", resp.Diagnostics, mock.Commands)
	}
}

func TestNTPUpdate(t *testing.T) {
	tests := []struct {
		name        string
		state, plan *NTPResourceModel
		want        []string
	}{
		{
			"swap a server and re-enable serving",
			ntpModel([]string{"10.0.0.1", "10.0.0.2"}, false, "us pacific", true),
			ntpModel([]string{"10.0.0.1", "10.0.0.3"}, true, "us pacific", true),
			[]string{"ntp", "no server 10.0.0.2", "server 10.0.0.3", "no disable serve", "exit", "write memory"},
		},
		{
			"timezone case only is not a change",
			ntpModel(nil, true, "us Pacific", false),
			ntpModel(nil, true, "us pacific", false),
			nil,
		},
		{
			"change timezone, drop summer time",
			ntpModel(nil, true, "us pacific", true),
			ntpModel(nil, true, "gmt gmt+01", false),
			[]string{"clock timezone gmt gmt+01", "no clock summer-time", "write memory"},
		},
		{
			"remove timezone and all servers",
			ntpModel([]string{"10.0.0.1"}, true, "us pacific", false),
			ntpModel(nil, true, "", false),
			[]string{"no clock timezone us pacific", "ntp", "no server 10.0.0.1", "exit", "write memory"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, mock := newNTPResource("", dialect.Dialect{})
			state, plan := ntpStateAndPlan(t, r, tt.state, tt.plan)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			assertCommands(t, "update", mock.Commands, tt.want)
		})
	}
}

func TestNTPDelete(t *testing.T) {
	r, mock := newNTPResource("", dialect.Dialect{})
	state, _ := ntpStateAndPlan(t, r, ntpModel([]string{"10.0.0.1", "10.0.0.2"}, false, "us Pacific", true), nil)
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	assertCommands(t, "delete", mock.Commands, []string{
		"no clock summer-time",
		"no clock timezone us pacific",
		"ntp", "no server 10.0.0.1", "no server 10.0.0.2", "no disable serve", "exit",
		"write memory",
	})
}

// europe/australia zones are refused at plan time on FastIron < 08.0.50 and
// accepted elsewhere; gmt/us zones are never gated.
func TestNTPModifyPlan_TimezoneRegions(t *testing.T) {
	tests := []struct {
		name, version, tz string
		wantErr           bool
	}{
		{"08.0.30 europe", "08.0.30uT311", "europe cet", true},
		{"08.0.30 australia", "08.0.30uT313", "Australia est", true},
		{"08.0.30 us", "08.0.30uT311", "us pacific", false},
		{"08.0.30 gmt", "08.0.30uT311", "gmt gmt+01", false},
		{"08.0.95 europe", "08.0.95mT213", "europe cet", false},
		{"unknown europe", "", "europe cet", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, mock := newNTPResource("", dialect.Detect(tt.version, parser.ImageTypeUnknown))
			_, plan := ntpStateAndPlan(t, r, nil, ntpModel(nil, true, tt.tz, false))
			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan}, &resp)
			if got := resp.Diagnostics.HasError(); got != tt.wantErr {
				t.Errorf("error = %v, want %v (%v)", got, tt.wantErr, resp.Diagnostics)
			}
			if tt.wantErr && !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "08.0.50") {
				t.Errorf("detail should name 08.0.50: %s", resp.Diagnostics.Errors()[0].Detail())
			}
			if len(mock.Commands) != 0 {
				t.Errorf("ModifyPlan sent %v", mock.Commands)
			}
			if tt.wantErr {
				// Apply-time backstop agrees and sends nothing.
				state, plan := ntpStateAndPlan(t, r, nil, ntpModel(nil, true, tt.tz, false))
				cresp := resource.CreateResponse{State: state}
				r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &cresp)
				if !cresp.Diagnostics.HasError() || len(mock.Commands) != 0 {
					t.Errorf("create backstop: diags %v, commands %v", cresp.Diagnostics, mock.Commands)
				}
			}
		})
	}
}
