package resource

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                = &InterfaceEthernetResource{}
	_ resource.ResourceWithImportState = &InterfaceEthernetResource{}
)

type InterfaceEthernetResource struct {
	client sshclient.CommandExecutor
}

type InterfaceEthernetResourceModel struct {
	ID                  types.String `tfsdk:"id"`
	Port                types.String `tfsdk:"port"`
	PortName            types.String `tfsdk:"port_name"`
	SpanningTreePt2PtMac types.Bool   `tfsdk:"spanning_tree_pt2pt_mac"`
	OpticalMonitor      types.Bool   `tfsdk:"optical_monitor"`
	UntaggedVLAN        types.Int64  `tfsdk:"untagged_vlan"`
	TaggedVLANs         types.Set    `tfsdk:"tagged_vlans"`
	TagAllVLANs         types.Bool   `tfsdk:"tag_all_vlans"`
	RawConfig           types.List   `tfsdk:"raw_config"`
}

func NewInterfaceEthernetResource() resource.Resource {
	return &InterfaceEthernetResource{}
}

func (r *InterfaceEthernetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_interface_ethernet"
}

func (r *InterfaceEthernetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an ethernet interface on an ICX switch, including VLAN membership.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Terraform resource ID (port identifier).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"port": schema.StringAttribute{
				Description: "Port identifier in unit/module/port format (e.g., \"1/1/15\").",
				Required:    true,
			},
			"port_name": schema.StringAttribute{
				Description: "Descriptive name for the port. Names with spaces are automatically quoted.",
				Optional:    true,
			},
			"spanning_tree_pt2pt_mac": schema.BoolAttribute{
				Description: "Enable 802.1w admin-pt2pt-mac on this interface.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"optical_monitor": schema.BoolAttribute{
				Description: "Enable optical monitoring on this interface. Set to false to disable (adds 'no optical-monitor').",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"untagged_vlan": schema.Int64Attribute{
				Description: "VLAN ID for untagged traffic on this port. The port will be set as untagged in this VLAN.",
				Optional:    true,
			},
			"tagged_vlans": schema.SetAttribute{
				Description: "Set of VLAN IDs for tagged traffic on this port.",
				Optional:    true,
				ElementType: types.Int64Type,
			},
			"tag_all_vlans": schema.BoolAttribute{
				Description: "Tag all VLANs on the switch (excluding untagged_vlan) on this port. Useful for trunk ports. Mutually exclusive with tagged_vlans.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"raw_config": schema.ListAttribute{
				Description: "Additional raw CLI commands to execute within the interface context. On destroy, each command is prefixed with 'no'.",
				Optional:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (r *InterfaceEthernetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *InterfaceEthernetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan InterfaceEthernetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolve "all" tagged VLANs.
	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	// Apply interface-level commands.
	ifCommands := r.buildInterfaceCommands(&plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(ifCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(ifCommands); err != nil {
			resp.Diagnostics.AddError("Failed to configure interface", err.Error())
			return
		}
	}

	// Apply VLAN membership commands.
	vlanCommands := r.buildVLANMembershipCommands(ctx, &plan, nil, config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(vlanCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(vlanCommands); err != nil {
			resp.Diagnostics.AddError("Failed to configure VLAN membership", err.Error())
			return
		}
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}

	plan.ID = types.StringValue(plan.Port.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *InterfaceEthernetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state InterfaceEthernetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	port := state.Port.ValueString()
	iface := config.FindEthernetInterface(port)

	// Physical interfaces always exist — if not in config, it just has defaults.
	if iface != nil {
		if iface.PortName != "" {
			state.PortName = types.StringValue(iface.PortName)
		} else {
			state.PortName = types.StringNull()
		}
		state.SpanningTreePt2PtMac = types.BoolValue(iface.SpanningTreePt2PtMac)
		state.OpticalMonitor = types.BoolValue(!iface.OpticalMonitorDisable)
	} else {
		state.PortName = types.StringNull()
		state.SpanningTreePt2PtMac = types.BoolValue(false)
		state.OpticalMonitor = types.BoolValue(true)
	}

	// Ensure tag_all_vlans has a value (can't be read from switch).
	if state.TagAllVLANs.IsNull() || state.TagAllVLANs.IsUnknown() {
		state.TagAllVLANs = types.BoolValue(false)
	}

	// Read VLAN membership from config.
	r.readVLANMembership(ctx, port, config, &state, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *InterfaceEthernetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state InterfaceEthernetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	// Interface-level changes.
	ifCommands := r.buildInterfaceUpdateCommands(&plan, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(ifCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(ifCommands); err != nil {
			resp.Diagnostics.AddError("Failed to update interface", err.Error())
			return
		}
	}

	// VLAN membership changes.
	vlanCommands := r.buildVLANMembershipCommands(ctx, &plan, &state, config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(vlanCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(vlanCommands); err != nil {
			resp.Diagnostics.AddError("Failed to update VLAN membership", err.Error())
			return
		}
	}

	if len(ifCommands) > 0 || len(vlanCommands) > 0 {
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}

	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *InterfaceEthernetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state InterfaceEthernetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	port := state.Port.ValueString()
	var commands []string

	// Reset interface to defaults.
	commands = append(commands, fmt.Sprintf("interface ethernet %s", port))
	if !state.PortName.IsNull() && state.PortName.ValueString() != "" {
		commands = append(commands, "no port-name")
	}
	if state.SpanningTreePt2PtMac.ValueBool() {
		commands = append(commands, "no spanning-tree 802-1w admin-pt2pt-mac")
	}
	if !state.OpticalMonitor.ValueBool() {
		commands = append(commands, "optical-monitor")
	}

	// Remove raw config lines.
	rawLines := listToStringSlice(ctx, state.RawConfig, &resp.Diagnostics)
	for _, line := range rawLines {
		commands = append(commands, "no "+line)
	}
	commands = append(commands, "exit")

	// Remove VLAN membership.
	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	vlanRemoveCommands := r.buildVLANRemovalCommands(ctx, port, &state, config, &resp.Diagnostics)
	commands = append(commands, vlanRemoveCommands...)

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to reset interface", err.Error())
			return
		}
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}
}

func (r *InterfaceEthernetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID is the port identifier, e.g., "1/2/7".
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("port"), req.ID)...)
}

// buildInterfaceCommands generates commands for creating an interface configuration.
func (r *InterfaceEthernetResource) buildInterfaceCommands(plan *InterfaceEthernetResourceModel, diags *diag.Diagnostics) []string {
	port := plan.Port.ValueString()
	commands := []string{fmt.Sprintf("interface ethernet %s", port)}

	if !plan.PortName.IsNull() && plan.PortName.ValueString() != "" {
		commands = append(commands, fmt.Sprintf("port-name %s", quotePortName(plan.PortName.ValueString())))
	}

	if plan.SpanningTreePt2PtMac.ValueBool() {
		commands = append(commands, "spanning-tree 802-1w admin-pt2pt-mac")
	}

	if !plan.OpticalMonitor.ValueBool() {
		commands = append(commands, "no optical-monitor")
	}

	// Raw config lines.
	rawLines := listToStringSlice(context.Background(), plan.RawConfig, diags)
	commands = append(commands, rawLines...)

	commands = append(commands, "exit")
	return commands
}

// buildInterfaceUpdateCommands generates commands for updating interface-level attributes.
func (r *InterfaceEthernetResource) buildInterfaceUpdateCommands(plan, state *InterfaceEthernetResourceModel, diags *diag.Diagnostics) []string {
	port := plan.Port.ValueString()
	var innerCommands []string

	// Port name.
	if !plan.PortName.Equal(state.PortName) {
		if plan.PortName.IsNull() || plan.PortName.ValueString() == "" {
			innerCommands = append(innerCommands, "no port-name")
		} else {
			innerCommands = append(innerCommands, fmt.Sprintf("port-name %s", quotePortName(plan.PortName.ValueString())))
		}
	}

	// STP pt2pt.
	if plan.SpanningTreePt2PtMac.ValueBool() != state.SpanningTreePt2PtMac.ValueBool() {
		if plan.SpanningTreePt2PtMac.ValueBool() {
			innerCommands = append(innerCommands, "spanning-tree 802-1w admin-pt2pt-mac")
		} else {
			innerCommands = append(innerCommands, "no spanning-tree 802-1w admin-pt2pt-mac")
		}
	}

	// Optical monitor.
	if plan.OpticalMonitor.ValueBool() != state.OpticalMonitor.ValueBool() {
		if plan.OpticalMonitor.ValueBool() {
			innerCommands = append(innerCommands, "optical-monitor")
		} else {
			innerCommands = append(innerCommands, "no optical-monitor")
		}
	}

	// Raw config.
	planRaw := listToStringSlice(context.Background(), plan.RawConfig, diags)
	stateRaw := listToStringSlice(context.Background(), state.RawConfig, diags)
	for _, line := range stateRaw {
		if !stringSliceContains(planRaw, line) {
			innerCommands = append(innerCommands, "no "+line)
		}
	}
	for _, line := range planRaw {
		if !stringSliceContains(stateRaw, line) {
			innerCommands = append(innerCommands, line)
		}
	}

	if len(innerCommands) == 0 {
		return nil
	}

	commands := []string{fmt.Sprintf("interface ethernet %s", port)}
	commands = append(commands, innerCommands...)
	commands = append(commands, "exit")
	return commands
}

// resolveTaggedVLANs resolves the tagged_vlans set. If tag_all_vlans is true,
// expands to all VLANs on the switch (excluding untagged_vlan and VLAN 1).
func (r *InterfaceEthernetResource) resolveTaggedVLANs(ctx context.Context, plan *InterfaceEthernetResourceModel, config *parser.RunningConfig, diags *diag.Diagnostics) []int {
	var untaggedVLAN int64
	if !plan.UntaggedVLAN.IsNull() {
		untaggedVLAN = plan.UntaggedVLAN.ValueInt64()
	}

	// Check for tag_all_vlans.
	if plan.TagAllVLANs.ValueBool() {
		var vlanIDs []int
		for _, v := range config.VLANs {
			if int64(v.ID) == untaggedVLAN || v.ID == 1 {
				continue
			}
			vlanIDs = append(vlanIDs, v.ID)
		}
		sort.Ints(vlanIDs)
		return vlanIDs
	}

	// Read individual VLAN IDs from the set.
	taggedInts := setToInt64Slice(ctx, plan.TaggedVLANs, diags)
	if diags.HasError() {
		return nil
	}

	var vlanIDs []int
	for _, id := range taggedInts {
		vlanIDs = append(vlanIDs, int(id))
	}
	sort.Ints(vlanIDs)
	return vlanIDs
}

// buildVLANMembershipCommands generates VLAN commands for port membership.
// If oldState is nil, this is a create operation; otherwise it's an update.
func (r *InterfaceEthernetResource) buildVLANMembershipCommands(ctx context.Context, plan *InterfaceEthernetResourceModel, oldState *InterfaceEthernetResourceModel, config *parser.RunningConfig, diags *diag.Diagnostics) []string {
	port := plan.Port.ValueString()
	var commands []string

	newTaggedVLANs := r.resolveTaggedVLANs(ctx, plan, config, diags)
	if diags.HasError() {
		return nil
	}

	var newUntaggedVLAN int
	if !plan.UntaggedVLAN.IsNull() {
		newUntaggedVLAN = int(plan.UntaggedVLAN.ValueInt64())
	}

	var oldTaggedVLANs []int
	var oldUntaggedVLAN int
	if oldState != nil {
		oldTaggedVLANs = r.resolveTaggedVLANs(ctx, oldState, config, diags)
		if !oldState.UntaggedVLAN.IsNull() {
			oldUntaggedVLAN = int(oldState.UntaggedVLAN.ValueInt64())
		}
	}

	// Remove old untagged VLAN membership.
	if oldUntaggedVLAN != 0 && oldUntaggedVLAN != newUntaggedVLAN {
		commands = append(commands,
			fmt.Sprintf("vlan %d", oldUntaggedVLAN),
			fmt.Sprintf("no untagged ethe %s", port),
			"exit",
		)
	}

	// Remove old tagged VLAN membership.
	for _, vlanID := range oldTaggedVLANs {
		if !intSliceContains(newTaggedVLANs, vlanID) {
			commands = append(commands,
				fmt.Sprintf("vlan %d", vlanID),
				fmt.Sprintf("no tagged ethe %s", port),
				"exit",
			)
		}
	}

	// Add new untagged VLAN membership.
	if newUntaggedVLAN != 0 && newUntaggedVLAN != oldUntaggedVLAN {
		commands = append(commands,
			fmt.Sprintf("vlan %d", newUntaggedVLAN),
			fmt.Sprintf("untagged ethe %s", port),
			"exit",
		)
	}

	// Add new tagged VLAN membership.
	for _, vlanID := range newTaggedVLANs {
		if !intSliceContains(oldTaggedVLANs, vlanID) {
			commands = append(commands,
				fmt.Sprintf("vlan %d", vlanID),
				fmt.Sprintf("tagged ethe %s", port),
				"exit",
			)
		}
	}

	return commands
}

// buildVLANRemovalCommands removes all VLAN membership for a port.
func (r *InterfaceEthernetResource) buildVLANRemovalCommands(ctx context.Context, port string, state *InterfaceEthernetResourceModel, config *parser.RunningConfig, diags *diag.Diagnostics) []string {
	var commands []string

	// Remove from all VLANs that have this port.
	for _, vlan := range config.VLANs {
		if vlan.ID == 1 {
			continue // Don't touch DEFAULT-VLAN.
		}
		for _, p := range vlan.TaggedPorts {
			if p == port {
				commands = append(commands,
					fmt.Sprintf("vlan %d", vlan.ID),
					fmt.Sprintf("no tagged ethe %s", port),
					"exit",
				)
				break
			}
		}
		for _, p := range vlan.UntaggedPorts {
			if p == port {
				commands = append(commands,
					fmt.Sprintf("vlan %d", vlan.ID),
					fmt.Sprintf("no untagged ethe %s", port),
					"exit",
				)
				break
			}
		}
	}

	return commands
}

// readVLANMembership reads VLAN membership for a port from the running config.
func (r *InterfaceEthernetResource) readVLANMembership(ctx context.Context, port string, config *parser.RunningConfig, state *InterfaceEthernetResourceModel, diags *diag.Diagnostics) {
	var taggedVLANs []int64
	var untaggedVLAN int

	for _, vlan := range config.VLANs {
		if vlan.ID == 1 {
			continue
		}
		for _, p := range vlan.TaggedPorts {
			if p == port {
				taggedVLANs = append(taggedVLANs, int64(vlan.ID))
				break
			}
		}
		for _, p := range vlan.UntaggedPorts {
			if p == port {
				untaggedVLAN = vlan.ID
				break
			}
		}
	}

	// If tag_all_vlans is set, don't overwrite tagged_vlans with the expanded list.
	if state.TagAllVLANs.ValueBool() {
		// Keep tag_all_vlans = true in state; don't touch tagged_vlans.
	} else if len(taggedVLANs) > 0 {
		state.TaggedVLANs = int64SliceToSet(ctx, taggedVLANs, diags)
	} else {
		state.TaggedVLANs = types.SetNull(types.Int64Type)
	}

	if untaggedVLAN != 0 {
		state.UntaggedVLAN = types.Int64Value(int64(untaggedVLAN))
	} else {
		state.UntaggedVLAN = types.Int64Null()
	}
}

func (r *InterfaceEthernetResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}

func intSliceContains(slice []int, val int) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}
