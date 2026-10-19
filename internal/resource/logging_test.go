package resource

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// loggingModel builds a model; nil slices are null, entries 0 null, "" null.
func loggingModel(hosts []string, entries int64, levels []string, facility string) *LoggingResourceModel {
	ctx := context.Background()
	m := &LoggingResourceModel{
		ID:              types.StringValue("logging"),
		Hosts:           types.ListNull(types.StringType),
		BufferedEntries: types.Int64Null(),
		DisabledLevels:  types.SetNull(types.StringType),
		Facility:        types.StringNull(),
	}
	if hosts != nil {
		list, diags := types.ListValueFrom(ctx, types.StringType, hosts)
		if diags.HasError() {
			panic(diags)
		}
		m.Hosts = list
	}
	if entries != 0 {
		m.BufferedEntries = types.Int64Value(entries)
	}
	if levels != nil {
		set, diags := types.SetValueFrom(ctx, types.StringType, levels)
		if diags.HasError() {
			panic(diags)
		}
		m.DisabledLevels = set
	}
	if facility != "" {
		m.Facility = types.StringValue(facility)
	}
	return m
}

func loggingStateAndPlan(t *testing.T, r *LoggingResource, state, plan *LoggingResourceModel) (tfsdk.State, tfsdk.Plan) {
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

func TestLoggingCreate(t *testing.T) {
	mock := sshclient.NewMockClient("")
	r := &LoggingResource{client: mock}
	state, plan := loggingStateAndPlan(t, r, nil,
		loggingModel([]string{"10.0.0.5", "10.0.0.6"}, 1000, []string{"informational", "debugging"}, "local7"))
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{
		"logging host 10.0.0.5",
		"logging host 10.0.0.6",
		"logging buffered 1000",
		"no logging buffered debugging",
		"no logging buffered informational",
		"logging facility local7",
		"write memory",
	})

	mock = sshclient.NewMockClient("")
	r = &LoggingResource{client: mock}
	state, plan = loggingStateAndPlan(t, r, nil, loggingModel(nil, 0, nil, ""))
	resp = resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() || len(mock.Commands) != 0 {
		t.Errorf("empty create: diags %v, commands %v", resp.Diagnostics, mock.Commands)
	}
}

func TestLoggingUpdate(t *testing.T) {
	tests := []struct {
		name        string
		state, plan *LoggingResourceModel
		want        []string
	}{
		{
			"swap host, resize buffer, toggle levels, change facility",
			loggingModel([]string{"10.0.0.5"}, 1000, []string{"debugging"}, "local7"),
			loggingModel([]string{"10.0.0.7"}, 500, []string{"informational"}, "local6"),
			[]string{"no logging host 10.0.0.5", "logging host 10.0.0.7", "logging buffered 500",
				"logging buffered debugging", "no logging buffered informational", "logging facility local6", "write memory"},
		},
		{
			"back to defaults",
			loggingModel([]string{"10.0.0.5"}, 1000, []string{"debugging"}, "local7"),
			loggingModel(nil, 0, nil, ""),
			[]string{"no logging host 10.0.0.5", "no logging buffered 1000", "logging buffered debugging",
				"no logging facility", "write memory"},
		},
		{
			"no change",
			loggingModel([]string{"10.0.0.5"}, 0, []string{"debugging"}, ""),
			loggingModel([]string{"10.0.0.5"}, 0, []string{"debugging"}, ""),
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := sshclient.NewMockClient("")
			r := &LoggingResource{client: mock}
			state, plan := loggingStateAndPlan(t, r, tt.state, tt.plan)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			assertCommands(t, "update", mock.Commands, tt.want)
		})
	}
}

func TestLoggingDelete(t *testing.T) {
	mock := sshclient.NewMockClient("")
	r := &LoggingResource{client: mock}
	state, _ := loggingStateAndPlan(t, r, loggingModel([]string{"10.0.0.5"}, 200, []string{"warnings", "errors"}, "kern"), nil)
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	assertCommands(t, "delete", mock.Commands, []string{
		"no logging host 10.0.0.5", "no logging buffered 200",
		"logging buffered errors", "logging buffered warnings", "no logging facility", "write memory",
	})
}

func TestLoggingRead(t *testing.T) {
	ctx := context.Background()
	read := func(cfg string) LoggingResourceModel {
		mock := sshclient.NewMockClient(cfg)
		r := &LoggingResource{client: mock}
		s, _ := loggingStateAndPlan(t, r, loggingModel([]string{"9.9.9.9"}, 7, []string{"alerts"}, "mail"), nil)
		resp := resource.ReadResponse{State: s}
		r.Read(ctx, resource.ReadRequest{State: s}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		var got LoggingResourceModel
		resp.State.Get(ctx, &got)
		return got
	}
	got := read("logging host 10.0.0.5\nlogging host 10.0.0.6 udp-port 5514\nlogging buffered 1000\nno logging buffered informational\nno logging buffered debugging\nlogging facility local7\n")
	var diags resource.ReadResponse
	if hosts := listToStringSlice(ctx, got.Hosts, &diags.Diagnostics); !reflect.DeepEqual(hosts, []string{"10.0.0.5", "10.0.0.6"}) {
		t.Errorf("hosts = %v", hosts)
	}
	if got.BufferedEntries.ValueInt64() != 1000 || got.Facility.ValueString() != "local7" {
		t.Errorf("entries/facility = %v/%v", got.BufferedEntries, got.Facility)
	}
	if levels := setToStringSlice(ctx, got.DisabledLevels, &diags.Diagnostics); !reflect.DeepEqual(levels, []string{"debugging", "informational"}) {
		t.Errorf("disabled_levels = %v", levels)
	}
	// Every fixture: all defaults, so state is nulled and an empty resource plans clean.
	for _, f := range []string{"7250-24", "7150-24", "7150-c12", "7150-c10zp", "6430-c12", "6450-c12pd"} {
		got := read(loadFixture(t, f))
		if !got.Hosts.IsNull() || !got.BufferedEntries.IsNull() || !got.DisabledLevels.IsNull() || !got.Facility.IsNull() {
			t.Errorf("%s: got %+v, want all null", f, got)
		}
	}
}
