package resource

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &SystemResource{}
	_ resource.ResourceWithImportState = &SystemResource{}
)

type SystemResource struct {
	client sshclient.CommandExecutor
}

type SystemResourceModel struct {
	ID                      types.String `tfsdk:"id"`
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
				Description: "Manager port list (e.g., \"987\").",
				Optional:    true,
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
}

func (r *SystemResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SystemResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := r.buildCommands(&plan)

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to configure system", err.Error())
			return
		}
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}

	plan.ID = types.StringValue("system")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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

	var commands []string

	if plan.GlobalSTP.ValueBool() != state.GlobalSTP.ValueBool() {
		if plan.GlobalSTP.ValueBool() {
			commands = append(commands, "global-stp")
		} else {
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
		if plan.DHCPClientDisable.ValueBool() {
			commands = append(commands, "ip dhcp-client disable")
		} else {
			commands = append(commands, "no ip dhcp-client disable")
		}
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

	// Reset all settings to defaults.
	var commands []string
	if state.GlobalSTP.ValueBool() {
		commands = append(commands, "no global-stp")
	}
	if !state.TelnetServer.ValueBool() {
		commands = append(commands, "telnet server")
	}
	if state.DHCPClientDisable.ValueBool() {
		commands = append(commands, "no ip dhcp-client disable")
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
		commands = append(commands, "ip dhcp-client disable")
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
