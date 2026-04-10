package resource

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &VLANResource{}
	_ resource.ResourceWithImportState = &VLANResource{}
)

type VLANResource struct {
	client sshclient.CommandExecutor
}

type VLANResourceModel struct {
	ID               types.String `tfsdk:"id"`
	VlanID           types.Int64  `tfsdk:"vlan_id"`
	Name             types.String `tfsdk:"name"`
	RouterInterface  types.Int64  `tfsdk:"router_interface"`
	SpanningTree     types.Bool   `tfsdk:"spanning_tree"`
	STPPriority      types.Int64  `tfsdk:"stp_priority"`
	MulticastPassive types.Bool   `tfsdk:"multicast_passive"`
	MulticastVersion types.Int64  `tfsdk:"multicast_version"`
	RawConfig        types.List   `tfsdk:"raw_config"`
}

func NewVLANResource() resource.Resource {
	return &VLANResource{}
}

func (r *VLANResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vlan"
}

func (r *VLANResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VLAN on an ICX switch.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Terraform resource ID (VLAN ID as string).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vlan_id": schema.Int64Attribute{
				Description: "VLAN ID (1-4094).",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "VLAN name.",
				Optional:    true,
			},
			"router_interface": schema.Int64Attribute{
				Description: "VE interface number to associate with this VLAN (creates router-interface ve N).",
				Optional:    true,
			},
			"spanning_tree": schema.BoolAttribute{
				Description: "Enable 802.1w spanning tree on this VLAN.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"stp_priority": schema.Int64Attribute{
				Description: "Spanning tree priority for this VLAN (e.g., 4096).",
				Optional:    true,
			},
			"multicast_passive": schema.BoolAttribute{
				Description: "Enable multicast passive on this VLAN.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"multicast_version": schema.Int64Attribute{
				Description: "IGMP multicast version (2 or 3).",
				Optional:    true,
			},
			"raw_config": schema.ListAttribute{
				Description: "Additional raw CLI commands to execute within the VLAN context. On destroy, each command is prefixed with 'no'.",
				Optional:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (r *VLANResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *VLANResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VLANResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := r.buildCreateCommands(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		resp.Diagnostics.AddError("Failed to create VLAN", err.Error())
		return
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}

	plan.ID = types.StringValue(strconv.FormatInt(plan.VlanID.ValueInt64(), 10))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VLANResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VLANResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	vlanID := int(state.VlanID.ValueInt64())
	vlan := config.FindVLAN(vlanID)
	if vlan == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	r.mapVLANToState(ctx, vlan, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *VLANResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state VLANResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := r.buildUpdateCommands(ctx, &plan, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to update VLAN", err.Error())
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

func (r *VLANResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VLANResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vlanID := state.VlanID.ValueInt64()

	// VLAN 1 cannot be deleted — reset to defaults instead.
	if vlanID == 1 {
		resp.Diagnostics.AddWarning("VLAN 1 cannot be deleted", "VLAN 1 (DEFAULT-VLAN) is permanent. It has been reset to defaults.")
		commands := []string{
			"vlan 1",
			"no spanning-tree 802-1w",
			"exit",
		}
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to reset VLAN 1", err.Error())
			return
		}
	} else {
		commands := []string{
			fmt.Sprintf("no vlan %d", vlanID),
		}
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to delete VLAN", err.Error())
			return
		}
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}
}

func (r *VLANResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vlanID, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid VLAN ID", fmt.Sprintf("Expected numeric VLAN ID, got %q", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vlan_id"), vlanID)...)
}

// buildCreateCommands generates the CLI commands to create a VLAN.
func (r *VLANResource) buildCreateCommands(ctx context.Context, plan *VLANResourceModel, diags *diag.Diagnostics) []string {
	vlanID := plan.VlanID.ValueInt64()

	// VLAN header.
	header := fmt.Sprintf("vlan %d", vlanID)
	if !plan.Name.IsNull() && plan.Name.ValueString() != "" {
		header += fmt.Sprintf(" name %s", plan.Name.ValueString())
	}
	header += " by port"

	commands := []string{header}

	// Router interface.
	if !plan.RouterInterface.IsNull() {
		commands = append(commands, fmt.Sprintf("router-interface ve %d", plan.RouterInterface.ValueInt64()))
	}

	// Spanning tree.
	if plan.SpanningTree.ValueBool() {
		commands = append(commands, "spanning-tree 802-1w")
		if !plan.STPPriority.IsNull() {
			commands = append(commands, fmt.Sprintf("spanning-tree 802-1w priority %d", plan.STPPriority.ValueInt64()))
		}
	}

	// Multicast.
	if plan.MulticastPassive.ValueBool() {
		commands = append(commands, "multicast passive")
	}
	if !plan.MulticastVersion.IsNull() {
		commands = append(commands, fmt.Sprintf("multicast version %d", plan.MulticastVersion.ValueInt64()))
	}

	// Raw config lines.
	rawLines := listToStringSlice(ctx, plan.RawConfig, diags)
	commands = append(commands, rawLines...)

	commands = append(commands, "exit")
	return commands
}

// buildUpdateCommands generates the CLI commands to update a VLAN.
func (r *VLANResource) buildUpdateCommands(ctx context.Context, plan, state *VLANResourceModel, diags *diag.Diagnostics) []string {
	vlanID := plan.VlanID.ValueInt64()
	var commands []string

	// Enter VLAN context. If name changed, the header updates it.
	header := fmt.Sprintf("vlan %d", vlanID)
	if !plan.Name.IsNull() && plan.Name.ValueString() != "" {
		header += fmt.Sprintf(" name %s", plan.Name.ValueString())
	}
	header += " by port"
	commands = append(commands, header)

	// Router interface changes.
	if !plan.RouterInterface.Equal(state.RouterInterface) {
		if !state.RouterInterface.IsNull() {
			commands = append(commands, fmt.Sprintf("no router-interface ve %d", state.RouterInterface.ValueInt64()))
		}
		if !plan.RouterInterface.IsNull() {
			commands = append(commands, fmt.Sprintf("router-interface ve %d", plan.RouterInterface.ValueInt64()))
		}
	}

	// Spanning tree changes.
	if plan.SpanningTree.ValueBool() != state.SpanningTree.ValueBool() {
		if plan.SpanningTree.ValueBool() {
			commands = append(commands, "spanning-tree 802-1w")
		} else {
			commands = append(commands, "no spanning-tree 802-1w")
		}
	}
	if plan.SpanningTree.ValueBool() && !plan.STPPriority.Equal(state.STPPriority) {
		if !plan.STPPriority.IsNull() {
			commands = append(commands, fmt.Sprintf("spanning-tree 802-1w priority %d", plan.STPPriority.ValueInt64()))
		}
	}

	// Multicast changes.
	if plan.MulticastPassive.ValueBool() != state.MulticastPassive.ValueBool() {
		if plan.MulticastPassive.ValueBool() {
			commands = append(commands, "multicast passive")
		} else {
			commands = append(commands, "no multicast passive")
		}
	}
	if !plan.MulticastVersion.Equal(state.MulticastVersion) {
		if plan.MulticastVersion.IsNull() && !state.MulticastVersion.IsNull() {
			commands = append(commands, fmt.Sprintf("no multicast version %d", state.MulticastVersion.ValueInt64()))
		} else if !plan.MulticastVersion.IsNull() {
			commands = append(commands, fmt.Sprintf("multicast version %d", plan.MulticastVersion.ValueInt64()))
		}
	}

	// Handle raw_config changes.
	planRaw := listToStringSlice(ctx, plan.RawConfig, diags)
	stateRaw := listToStringSlice(ctx, state.RawConfig, diags)

	// Remove old raw lines.
	for _, line := range stateRaw {
		if !stringSliceContains(planRaw, line) {
			commands = append(commands, "no "+line)
		}
	}
	// Add new raw lines.
	for _, line := range planRaw {
		if !stringSliceContains(stateRaw, line) {
			commands = append(commands, line)
		}
	}

	commands = append(commands, "exit")
	return commands
}

func (r *VLANResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}

func (r *VLANResource) mapVLANToState(ctx context.Context, vlan *parser.VLAN, state *VLANResourceModel, diags *diag.Diagnostics) {
	state.VlanID = types.Int64Value(int64(vlan.ID))
	state.ID = types.StringValue(strconv.Itoa(vlan.ID))

	if vlan.Name != "" {
		state.Name = types.StringValue(vlan.Name)
	} else {
		state.Name = types.StringNull()
	}

	if vlan.RouterInterface != nil {
		state.RouterInterface = types.Int64Value(int64(*vlan.RouterInterface))
	} else {
		state.RouterInterface = types.Int64Null()
	}

	state.SpanningTree = types.BoolValue(vlan.SpanningTree)

	if vlan.STPPriority != nil {
		state.STPPriority = types.Int64Value(int64(*vlan.STPPriority))
	} else {
		state.STPPriority = types.Int64Null()
	}

	state.MulticastPassive = types.BoolValue(vlan.MulticastPassive)

	if vlan.MulticastVersion != nil {
		state.MulticastVersion = types.Int64Value(int64(*vlan.MulticastVersion))
	} else {
		state.MulticastVersion = types.Int64Null()
	}

	// raw_config is not read from the switch — preserve state.
}
