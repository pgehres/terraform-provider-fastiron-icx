package resource

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// icx_system.hostname: the one attribute whose command changes the CLI prompt.
// Every test here checks that `hostname` never travels through
// ExecuteInConfigMode (which would hang the session) but through the
// prompt re-detecting path, and always last, before `write memory`.

func systemWithConfig(t *testing.T, fixture string) (*SystemResource, *sshclient.MockClient) {
	t.Helper()
	cfg := ""
	if fixture != "" {
		cfg = loadFixture(t, fixture)
	}
	mock := sshclient.NewMockClient(cfg)
	return &SystemResource{client: mock, dialect: dialect.Detect("08.0.95mT213", parser.ImageTypeRouter)}, mock
}

func withHostname(m *SystemResourceModel, h types.String) *SystemResourceModel {
	m.Hostname = h
	return m
}

// Create with a hostname and another setting: the other setting goes through
// the normal path, the hostname through the prompt-changing one, then save.
func TestSystemCreate_HostnameLastViaPromptChangingPath(t *testing.T) {
	r, mock := systemWithConfig(t, "")
	plan := withHostname(systemModel(false, false), types.StringValue("lab-core"))
	state, p := systemStateAndPlan(t, r, systemModel(false, true), plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{"no telnet server", "hostname lab-core", "write memory"})
	if mock.PromptRedetects != 1 || len(mock.PromptChangingCommands) != 1 || mock.PromptChangingCommands[0] != "hostname lab-core" {
		t.Errorf("hostname must go through ExecutePromptChanging exactly once, got redetects=%d cmds=%v",
			mock.PromptRedetects, mock.PromptChangingCommands)
	}

	var got SystemResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Hostname.ValueString() != "lab-core" {
		t.Errorf("state hostname = %v", got.Hostname)
	}
}

// Only the hostname set: still saved.
func TestSystemCreate_HostnameOnlyStillWritesMemory(t *testing.T) {
	r, mock := systemWithConfig(t, "")
	plan := withHostname(systemModel(false, true), types.StringValue("edge1"))
	state, p := systemStateAndPlan(t, r, systemModel(false, true), plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{"hostname edge1", "write memory"})
}

// hostname omitted from the configuration (unknown in the plan): Create reads
// the device's name into state and sends no hostname command — so adopting
// the attribute on an existing fleet is a zero-diff operation.
func TestSystemCreate_HostnameUnknownAdoptsDeviceName(t *testing.T) {
	r, mock := systemWithConfig(t, "6430-c12")
	plan := withHostname(systemModel(false, true), types.StringUnknown())
	state, p := systemStateAndPlan(t, r, systemModel(false, true), plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{"show running-config"})
	var got SystemResourceModel
	resp.State.Get(context.Background(), &got)
	if got.Hostname.ValueString() != "lab-icx-6430" {
		t.Errorf("state hostname = %v, want the device's lab-icx-6430", got.Hostname)
	}
}

// Same, on a device running its default name: state hostname is null.
func TestSystemCreate_HostnameUnknownDeviceDefaultIsNull(t *testing.T) {
	r, mock := systemWithConfig(t, "7250-24")
	plan := withHostname(systemModel(false, true), types.StringUnknown())
	state, p := systemStateAndPlan(t, r, systemModel(false, true), plan)

	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	if mock.HasCommandContaining("hostname") || mock.WriteMemoryCalls != 0 {
		t.Errorf("no hostname command expected, got %v", mock.Commands)
	}
	var got SystemResourceModel
	resp.State.Get(context.Background(), &got)
	if !got.Hostname.IsNull() {
		t.Errorf("state hostname = %v, want null", got.Hostname)
	}
}

func TestSystemRead_Hostname(t *testing.T) {
	tests := []struct {
		fixture string
		want    types.String
	}{
		{"6430-c12", types.StringValue("lab-icx-6430")},
		{"7250-24", types.StringNull()},
		{"7150-c10zp", types.StringNull()},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			r, _ := systemWithConfig(t, tt.fixture)
			state, _ := systemStateAndPlan(t, r, systemModel(false, true), nil)
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("read: %v", resp.Diagnostics)
			}
			var got SystemResourceModel
			resp.State.Get(context.Background(), &got)
			if !got.Hostname.Equal(tt.want) {
				t.Errorf("hostname = %v, want %v", got.Hostname, tt.want)
			}
		})
	}
}

// Update renaming the device: other changes first, hostname last via the
// prompt-changing path, then save.
func TestSystemUpdate_HostnameChangeOrdering(t *testing.T) {
	r, mock := systemWithConfig(t, "")
	st := withHostname(systemModel(false, true), types.StringValue("old-name"))
	pl := withHostname(systemModel(false, false), types.StringValue("new-name"))
	state, plan := systemStateAndPlan(t, r, st, pl)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	assertCommands(t, "update", mock.Commands, []string{"no telnet server", "hostname new-name", "write memory"})
	if mock.PromptRedetects != 1 {
		t.Errorf("expected one prompt re-detection, got %d", mock.PromptRedetects)
	}
}

// An unchanged hostname sends nothing (and does not re-detect the prompt).
func TestSystemUpdate_HostnameUnchangedNoCommands(t *testing.T) {
	r, mock := systemWithConfig(t, "")
	st := withHostname(systemModel(false, true), types.StringValue("same"))
	pl := withHostname(systemModel(false, true), types.StringValue("same"))
	state, plan := systemStateAndPlan(t, r, st, pl)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if len(mock.Commands) != 0 || mock.PromptRedetects != 0 {
		t.Errorf("expected no commands, got %v (redetects %d)", mock.Commands, mock.PromptRedetects)
	}
}

// hostname unknown in an update plan (omitted from config, device on its
// default name so nothing in state): keep it that way, send nothing for it.
func TestSystemUpdate_HostnameUnknownKeepsState(t *testing.T) {
	r, mock := systemWithConfig(t, "")
	st := withHostname(systemModel(false, true), types.StringNull())
	pl := withHostname(systemModel(false, false), types.StringUnknown())
	state, plan := systemStateAndPlan(t, r, st, pl)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	assertCommands(t, "update", mock.Commands, []string{"no telnet server", "write memory"})
	var got SystemResourceModel
	resp.State.Get(context.Background(), &got)
	if !got.Hostname.IsNull() {
		t.Errorf("state hostname = %v, want null", got.Hostname)
	}
}

// The switch rejecting the hostname fails the apply before `write memory`.
func TestSystemUpdate_HostnameCLIErrorStopsBeforeSave(t *testing.T) {
	r, mock := systemWithConfig(t, "")
	mock.ErrorOnCommand["hostname rejected"] = errors.New("Invalid input -> rejected")
	st := withHostname(systemModel(false, true), types.StringValue("ok"))
	pl := withHostname(systemModel(false, true), types.StringValue("rejected"))
	state, plan := systemStateAndPlan(t, r, st, pl)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if mock.WriteMemoryCalls != 0 {
		t.Errorf("write memory must not run after a failed hostname change, got %v", mock.Commands)
	}
}

// Delete never resets the hostname.
func TestSystemDelete_HostnameUntouched(t *testing.T) {
	r, mock := systemWithConfig(t, "")
	st := withHostname(systemModel(false, false), types.StringValue("keep-me"))
	state, _ := systemStateAndPlan(t, r, st, nil)

	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	assertCommands(t, "delete", mock.Commands, []string{"telnet server", "write memory"})
	if mock.HasCommandContaining("hostname") {
		t.Errorf("delete must not touch the hostname, got %v", mock.Commands)
	}
}

// Schema validation keeps anything that could break the command or the
// prompt — whitespace, quotes, control characters, a leading hyphen — out.
func TestSystemSchema_HostnameValidation(t *testing.T) {
	r, _ := systemWithConfig(t, "")
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	validators := schemaResp.Schema.Attributes["hostname"].(interface {
		StringValidators() []validator.String
	}).StringValidators()

	tests := []struct {
		in string
		ok bool
	}{
		{"lab-core", true},
		{"ICX7250-24", true},
		{"sw1.lab.example", true},
		{"a", true},
		{"core_1", true},
		{strings.Repeat("x", 64), true},
		{strings.Repeat("x", 65), false},
		{"", false},
		{"bad name", false},
		{`"quoted"`, false},
		{"-leading", false},
		{".leading", false},
		{"nl\nhostname", false},
		{"tab\tname", false},
		{"ctl\x01", false},
		{"hash#", false},
		{"gt>", false},
	}
	for _, tt := range tests {
		req := validator.StringRequest{ConfigValue: types.StringValue(tt.in)}
		var resp validator.StringResponse
		for _, v := range validators {
			v.ValidateString(context.Background(), req, &resp)
		}
		if got := !resp.Diagnostics.HasError(); got != tt.ok {
			t.Errorf("hostname %q: valid = %v, want %v (%v)", tt.in, got, tt.ok, resp.Diagnostics)
		}
	}
}
