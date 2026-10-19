package resource

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &SystemResource{}
	_ resource.ResourceWithImportState = &SystemResource{}
	_ resource.ResourceWithModifyPlan  = &SystemResource{}
)

// reManagerPortList matches valid FastIron manager port-list values: digits,
// slashes, commas, spaces, and the letters in "to" (for range syntax like
// "1/1/1 to 1/1/24"). A literal space (not \s) is used so newlines and other
// control characters are rejected at plan time, preventing command injection
// via the `manager port-list %s` interpolation.
var reManagerPortList = regexp.MustCompile(`^[0-9to/, ]+$`)

// reHostname is the charset accepted for `hostname <name>`: letters, digits,
// hyphen, underscore and dot, starting with a letter or digit. No whitespace,
// quotes or control characters — the name is interpolated into a CLI command
// and, more importantly, becomes part of the CLI prompt the SSH client has to
// recognise ("SSH@<hostname> Router#"). The device enforces its own length
// limit on top of the 64 characters allowed here.
var reHostname = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type SystemResource struct {
	client  sshclient.CommandExecutor
	dialect dialect.Dialect
}

// systemCapabilityGuards maps each firmware- or image-dependent icx_system
// attribute to the dialect capability it needs. A guard fires only when the
// attribute is set to a value that would send its command (true / non-null),
// so defaults never block. To gate another attribute, add a row here and the
// capability in internal/dialect.
var systemCapabilityGuards = []struct {
	capability dialect.Capability
	feature    string
	set        func(m *SystemResourceModel) bool
}{
	{dialect.CapGlobalSTP, "The global_stp attribute of icx_system",
		func(m *SystemResourceModel) bool { return m.GlobalSTP.ValueBool() }},
	{dialect.CapManager, "The manager_registrar attribute of icx_system",
		func(m *SystemResourceModel) bool { return m.ManagerRegistrar.ValueBool() }},
	{dialect.CapManager, "The manager_disable attribute of icx_system",
		func(m *SystemResourceModel) bool { return m.ManagerDisable.ValueBool() }},
	{dialect.CapManager, "The manager_port_list attribute of icx_system",
		func(m *SystemResourceModel) bool {
			return !m.ManagerPortList.IsNull() && !m.ManagerPortList.IsUnknown()
		}},
	{dialect.CapOpticalMonitor, "The optical_monitor attribute of icx_system",
		func(m *SystemResourceModel) bool { return m.OpticalMonitor.ValueBool() }},
	{dialect.CapOpticalMonitor, "The optical_monitor_non_ruckus attribute of icx_system",
		func(m *SystemResourceModel) bool { return m.OpticalMonitorNonRuckus.ValueBool() }},
}

// checkCapabilities records a diagnostic for every planned attribute the
// device's dialect cannot accept and reports whether all passed. Only the
// plan side is checked: a value present solely in state (e.g. left behind by
// a router→switch reimage) is necessarily absent on the device, so removing
// it must not error.
func (r *SystemResource) checkCapabilities(plan *SystemResourceModel, diags *diag.Diagnostics) bool {
	ok := true
	for _, g := range systemCapabilityGuards {
		if g.set(plan) && !requireCapability(r.dialect, g.capability, g.feature, diags) {
			ok = false
		}
	}
	return ok
}

type SystemResourceModel struct {
	ID                      types.String `tfsdk:"id"`
	Hostname                types.String `tfsdk:"hostname"`
	GlobalSTP               types.Bool   `tfsdk:"global_stp"`
	TelnetServer            types.Bool   `tfsdk:"telnet_server"`
	DHCPClientDisable       types.Bool   `tfsdk:"dhcp_client_disable"`
	OpticalMonitor          types.Bool   `tfsdk:"optical_monitor"`
	OpticalMonitorNonRuckus types.Bool   `tfsdk:"optical_monitor_non_ruckus"`
	ManagerRegistrar        types.Bool   `tfsdk:"manager_registrar"`
	ManagerDisable          types.Bool   `tfsdk:"manager_disable"`
	ManagerPortList         types.String `tfsdk:"manager_port_list"`
}

func NewSystemResource() resource.Resource {
	return &SystemResource{}
}

func (r *SystemResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system"
}

func (r *SystemResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages global system settings on an ICX switch. This is a singleton resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"hostname": schema.StringAttribute{
				Description: "System hostname (`hostname <name>`), which also forms the CLI prompt. " +
					"Letters, digits, '.', '-' and '_' only, 1-64 characters. When omitted the device's " +
					"current hostname is kept and recorded in state; the provider never resets a hostname " +
					"to the factory default.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 64),
					stringvalidator.RegexMatches(reHostname,
						"hostname may only contain letters, digits, '.', '-' and '_', and must start with a letter or digit"),
				},
			},
			"global_stp": schema.BoolAttribute{
				Description: "Enable global spanning tree.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"telnet_server": schema.BoolAttribute{
				Description: "Enable the telnet server. Set to false to disable (adds 'no telnet server').",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"dhcp_client_disable": schema.BoolAttribute{
				Description: "Disable the DHCP client.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"optical_monitor": schema.BoolAttribute{
				Description: "Enable optical monitoring globally.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"optical_monitor_non_ruckus": schema.BoolAttribute{
				Description: "Enable optical monitoring for non-Ruckus optics.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"manager_registrar": schema.BoolAttribute{
				Description: "Enable the manager registrar.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"manager_disable": schema.BoolAttribute{
				Description: "Disable the manager.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"manager_port_list": schema.StringAttribute{
				Description: "Manager port list (e.g., \"987\" or \"1/1/1 to 1/1/24\"). Only digits, slashes, commas, spaces, and the letters in \"to\" are allowed.",
				Optional:    true,
				Validators: []validator.String{
					// Interpolated into `manager port-list %s` and `no manager port-list %s`.
					// Reject newlines and any characters outside the expected charset to
					// prevent command injection.
					stringvalidator.RegexMatches(
						reManagerPortList,
						`manager_port_list may only contain digits, slashes, commas, spaces, and "to" (for ranges)`,
					),
				},
			},
		},
	}
}

func (r *SystemResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerdata.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *providerdata.ProviderData, got %T", req.ProviderData))
		return
	}
	r.client = data.Client
	r.dialect = data.Dialect
}

// ModifyPlan rejects, at plan time, any attribute the device's dialect cannot
// accept (e.g. global_stp = true on the switch image, manager_* on FastIron
// 08.0.30) — see systemCapabilityGuards. Destroy plans and null, unknown, or
// false values pass; the apply-time guard in Create/Update still backstops a
// value that only becomes known during apply.
func (r *SystemResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan SystemResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.checkCapabilities(&plan, &resp.Diagnostics)
}

func (r *SystemResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SystemResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !r.checkCapabilities(&plan, &resp.Diagnostics) {
		return
	}

	commands := r.buildCommands(&plan)

	// hostname omitted from config: adopt whatever the device has (and send
	// nothing for it), so an existing fleet picks up the attribute with a
	// zero-diff plan.
	setHostname := !plan.Hostname.IsNull() && !plan.Hostname.IsUnknown()
	if plan.Hostname.IsUnknown() {
		config, err := r.getRunningConfig()
		if err != nil {
			resp.Diagnostics.AddError("Failed to read running config", err.Error())
			return
		}
		plan.Hostname = hostnameValue(config.Global.Hostname)
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to configure system", err.Error())
			return
		}
	}
	// The hostname goes last, through the prompt re-detecting path: once it is
	// sent the CLI prompt changes, and nothing else would be readable until the
	// client has learned the new one.
	sent := len(commands) > 0
	if setHostname {
		if err := r.client.ExecutePromptChanging("hostname " + plan.Hostname.ValueString()); err != nil {
			resp.Diagnostics.AddError("Failed to set hostname", err.Error())
			return
		}
		sent = true
	}
	if sent {
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}

	plan.ID = types.StringValue("system")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// hostnameValue maps the parser's hostname ("" = device default) to the
// attribute value (null = device default).
func hostnameValue(hostname string) types.String {
	if hostname == "" {
		return types.StringNull()
	}
	return types.StringValue(hostname)
}

func (r *SystemResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SystemResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	state.Hostname = hostnameValue(config.Global.Hostname)
	state.GlobalSTP = types.BoolValue(config.Global.GlobalSTP)

	// Telnet: if explicitly set in config, use that; otherwise default to true (enabled).
	if config.Global.TelnetServerSet {
		state.TelnetServer = types.BoolValue(config.Global.TelnetServer)
	} else {
		state.TelnetServer = types.BoolValue(true) // Default is enabled.
	}

	state.DHCPClientDisable = types.BoolValue(config.Global.DHCPClientDisable)
	state.OpticalMonitor = types.BoolValue(config.Global.OpticalMonitor)
	state.OpticalMonitorNonRuckus = types.BoolValue(config.Global.OpticalMonitorNonRuckus)
	state.ManagerRegistrar = types.BoolValue(config.Global.ManagerRegistrar)
	state.ManagerDisable = types.BoolValue(config.Global.ManagerDisable)

	if config.Global.ManagerPortList != "" {
		state.ManagerPortList = types.StringValue(config.Global.ManagerPortList)
	} else {
		state.ManagerPortList = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SystemResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SystemResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Guard only the plan side: global_stp=true solely in state (left behind
	// by a router→switch reimage) is necessarily absent on a switch image, so
	// turning it off must not error — the `no global-stp` is skipped instead.
	if !r.checkCapabilities(&plan, &resp.Diagnostics) {
		return
	}

	var commands []string

	if plan.GlobalSTP.ValueBool() != state.GlobalSTP.ValueBool() {
		if plan.GlobalSTP.ValueBool() {
			commands = append(commands, "global-stp")
		} else if r.dialect.Has(dialect.CapGlobalSTP) {
			commands = append(commands, "no global-stp")
		}
	}

	if plan.TelnetServer.ValueBool() != state.TelnetServer.ValueBool() {
		if plan.TelnetServer.ValueBool() {
			commands = append(commands, "telnet server")
		} else {
			commands = append(commands, "no telnet server")
		}
	}

	if plan.DHCPClientDisable.ValueBool() != state.DHCPClientDisable.ValueBool() {
		commands = append(commands, r.dialect.DHCPClientDisable(plan.DHCPClientDisable.ValueBool()))
	}

	if plan.OpticalMonitor.ValueBool() != state.OpticalMonitor.ValueBool() {
		if plan.OpticalMonitor.ValueBool() {
			commands = append(commands, "optical-monitor")
		} else {
			commands = append(commands, "no optical-monitor")
		}
	}

	if plan.OpticalMonitorNonRuckus.ValueBool() != state.OpticalMonitorNonRuckus.ValueBool() {
		if plan.OpticalMonitorNonRuckus.ValueBool() {
			commands = append(commands, "optical-monitor non-ruckus-optic-enable")
		} else {
			commands = append(commands, "no optical-monitor non-ruckus-optic-enable")
		}
	}

	if plan.ManagerRegistrar.ValueBool() != state.ManagerRegistrar.ValueBool() {
		if plan.ManagerRegistrar.ValueBool() {
			commands = append(commands, "manager registrar")
		} else {
			commands = append(commands, "no manager registrar")
		}
	}

	if plan.ManagerDisable.ValueBool() != state.ManagerDisable.ValueBool() {
		if plan.ManagerDisable.ValueBool() {
			commands = append(commands, "manager disable")
		} else {
			commands = append(commands, "no manager disable")
		}
	}

	if !plan.ManagerPortList.Equal(state.ManagerPortList) {
		if plan.ManagerPortList.IsNull() && !state.ManagerPortList.IsNull() {
			commands = append(commands, fmt.Sprintf("no manager port-list %s", state.ManagerPortList.ValueString()))
		} else if !plan.ManagerPortList.IsNull() {
			commands = append(commands, fmt.Sprintf("manager port-list %s", plan.ManagerPortList.ValueString()))
		}
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to update system", err.Error())
			return
		}
	}

	// hostname: unknown means "not in config and nothing in state to keep"
	// (the device runs its default name) — leave it alone. Null in plan cannot
	// happen for an Optional+Computed attribute. A changed value goes last,
	// through the prompt re-detecting path (see Create).
	sent := len(commands) > 0
	if plan.Hostname.IsUnknown() {
		plan.Hostname = state.Hostname
	}
	if !plan.Hostname.IsNull() && !plan.Hostname.Equal(state.Hostname) {
		if err := r.client.ExecutePromptChanging("hostname " + plan.Hostname.ValueString()); err != nil {
			resp.Diagnostics.AddError("Failed to set hostname", err.Error())
			return
		}
		sent = true
	}
	if sent {
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}

	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SystemResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), "system")...)
}

func (r *SystemResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SystemResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Reset all settings to defaults. On a switch image global-stp cannot be
	// configured (and `no global-stp` does not exist), so a true value in
	// stale state is skipped rather than blocking the singleton's delete.
	//
	// The hostname is deliberately NOT reset: state holds the device's name
	// whether the user set it or the provider merely recorded it, so `no
	// hostname` here would rename devices nobody asked to rename (and bounce
	// the CLI prompt mid-destroy).
	var commands []string
	if state.GlobalSTP.ValueBool() && r.dialect.Has(dialect.CapGlobalSTP) {
		commands = append(commands, "no global-stp")
	}
	if !state.TelnetServer.ValueBool() {
		commands = append(commands, "telnet server")
	}
	if state.DHCPClientDisable.ValueBool() {
		commands = append(commands, r.dialect.DHCPClientDisable(false))
	}
	if state.OpticalMonitor.ValueBool() {
		commands = append(commands, "no optical-monitor")
	}
	if state.OpticalMonitorNonRuckus.ValueBool() {
		commands = append(commands, "no optical-monitor non-ruckus-optic-enable")
	}
	if state.ManagerRegistrar.ValueBool() {
		commands = append(commands, "no manager registrar")
	}
	if state.ManagerDisable.ValueBool() {
		commands = append(commands, "no manager disable")
	}
	if !state.ManagerPortList.IsNull() {
		commands = append(commands, fmt.Sprintf("no manager port-list %s", state.ManagerPortList.ValueString()))
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to reset system", err.Error())
			return
		}
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}
}

func (r *SystemResource) buildCommands(plan *SystemResourceModel) []string {
	var commands []string

	if plan.GlobalSTP.ValueBool() {
		commands = append(commands, "global-stp")
	}
	if !plan.TelnetServer.ValueBool() {
		commands = append(commands, "no telnet server")
	}
	if plan.DHCPClientDisable.ValueBool() {
		commands = append(commands, r.dialect.DHCPClientDisable(true))
	}
	if plan.OpticalMonitor.ValueBool() {
		commands = append(commands, "optical-monitor")
	}
	if plan.OpticalMonitorNonRuckus.ValueBool() {
		commands = append(commands, "optical-monitor non-ruckus-optic-enable")
	}
	if plan.ManagerRegistrar.ValueBool() {
		commands = append(commands, "manager registrar")
	}
	if plan.ManagerDisable.ValueBool() {
		commands = append(commands, "manager disable")
	}
	if !plan.ManagerPortList.IsNull() {
		commands = append(commands, fmt.Sprintf("manager port-list %s", plan.ManagerPortList.ValueString()))
	}

	return commands
}

func (r *SystemResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}
