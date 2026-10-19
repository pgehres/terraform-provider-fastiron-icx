package resource

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &InterfaceEthernetResource{}
	_ resource.ResourceWithImportState = &InterfaceEthernetResource{}
)

// rePort matches FastIron port identifiers in unit/slot/port format (e.g., "1/1/15").
var rePort = regexp.MustCompile(`^\d+/\d+/\d+$`)

// rePortName matches valid FastIron port names: no control characters or double
// quotes. Spaces are allowed — quotePortName wraps them in double quotes for
// the CLI command.
var rePortName = regexp.MustCompile(`^[^"\x00-\x1f]+$`)

type InterfaceEthernetResource struct {
	client sshclient.CommandExecutor
}

type InterfaceEthernetResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Port                 types.String `tfsdk:"port"`
	PortName             types.String `tfsdk:"port_name"`
	SpanningTreePt2PtMac types.Bool   `tfsdk:"spanning_tree_pt2pt_mac"`
	OpticalMonitor       types.Bool   `tfsdk:"optical_monitor"`
	UntaggedVLAN         types.Int64  `tfsdk:"untagged_vlan"`
	TaggedVLANs          types.Set    `tfsdk:"tagged_vlans"`
	TagAllVLANs          types.Bool   `tfsdk:"tag_all_vlans"`
	RawConfig            types.List   `tfsdk:"raw_config"`
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
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						rePort,
						`port must be in unit/slot/port format (e.g. "1/1/15")`,
					),
				},
			},
			"port_name": schema.StringAttribute{
				Description: "Descriptive name for the port. Names with spaces are automatically quoted. Double quotes and control characters are not allowed.",
				Optional:    true,
				Validators: []validator.String{
					// Double quotes are rejected because quotePortName wraps the name in
					// double quotes; an embedded quote would break the CLI command.
					// Control characters (including newlines) are rejected to prevent
					// command injection.
					stringvalidator.RegexMatches(
						rePortName,
						`port_name must not contain double quotes or control characters (0x00–0x1f)`,
					),
					stringvalidator.LengthBetween(1, 64),
				},
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
				Validators: []validator.Int64{
					int64validator.Between(1, 4094),
				},
			},
			"tagged_vlans": schema.SetAttribute{
				Description: "Set of VLAN IDs for tagged traffic on this port.",
				Optional:    true,
				ElementType: types.Int64Type,
				Validators: []validator.Set{
					setvalidator.ValueInt64sAre(
						int64validator.Between(1, 4094),
					),
				},
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
				Validators: []validator.List{
					// Carriage returns and newlines would allow injecting additional
					// CLI commands through raw_config elements.
					listvalidator.ValueStringsAre(
						stringvalidator.RegexMatches(
							reNoNewlines,
							`raw_config elements must not contain carriage returns or newlines`,
						),
					),
				},
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
	r.readVLANMembership(ctx, port, iface, config, &state, &resp.Diagnostics)

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
//
// On FastIron releases that have the `dual-mode` interface command (08.0.30
// through 08.0.7x — see parser.FirmwareHasDualMode) the trunk-untagged case is
// expressed differently and is handled by buildDualModeMembershipCommands.
// On 08.0.80+ (the ICX 7000-series fleet on 08.0.95) this function's
// VLAN-context-only form is used unchanged.
func (r *InterfaceEthernetResource) buildVLANMembershipCommands(ctx context.Context, plan *InterfaceEthernetResourceModel, oldState *InterfaceEthernetResourceModel, config *parser.RunningConfig, diags *diag.Diagnostics) []string {
	if config.UsesDualMode() {
		return r.buildDualModeMembershipCommands(ctx, plan, oldState, config, diags)
	}

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

// dualModeCommand returns the interface-context command that makes port's
// untagged VLAN vlanID on dual-mode firmware. FastIron documents the bare form
// (`dual-mode`, no argument) as "untagged in the default VLAN", so VLAN 1 is
// emitted that way rather than as `dual-mode 1`.
func dualModeCommand(vlanID int) string {
	if vlanID == 1 {
		return "dual-mode"
	}
	return fmt.Sprintf("dual-mode %d", vlanID)
}

// buildDualModeMembershipCommands is the FastIron 08.0.30-era counterpart of
// buildVLANMembershipCommands.
//
// Dialect (FastIron 08.0.30–08.0.7x Layer 2 guide / command reference):
//
//   - An access port (untagged_vlan only) is an `untagged ethe` member in VLAN
//     context, exactly as on 08.0.95.
//   - A port that is tagged in some VLANs cannot also be made `untagged` in
//     VLAN context — the CLI rejects it. Instead the port is made a *tagged*
//     member of the untagged VLAN too, and `dual-mode <vlan-id>` under
//     `interface ethernet` selects which of its VLANs carries untagged frames.
//     "Only tagged ports can be configured as dual-mode ports." Bare
//     `dual-mode` means the default VLAN (1), which needs no tagged entry.
//   - `no dual-mode` returns the port to tagged-only.
//
// So on this firmware the VLAN-context tagged set for a trunk with an untagged
// VLAN is tagged_vlans ∪ {untagged_vlan}, and Read mirrors that by excluding
// the dual-mode VLAN from tagged_vlans (see readVLANMembership).
//
// Command ordering matters for a live trunk, so the sequence is:
//
//  1. `no dual-mode` when the port is leaving dual-mode (device had it, plan
//     does not need it) — before any tagged membership it depends on is removed.
//  2. Remove a VLAN-context untagged membership that is going away (a port
//     untagged in a non-default VLAN cannot be tagged elsewhere until then).
//  3. Add new tagged memberships, including the new dual-mode VLAN.
//  4. `dual-mode <id>` when the plan needs dual-mode and the device's current
//     dual-mode VLAN differs. Re-issued directly, without `no dual-mode` first:
//     the intermediate `no dual-mode` would momentarily drop the untagged VLAN
//     (on a management uplink, that severs the provider's own SSH session).
//  5. Remove tagged memberships no longer wanted (the old dual-mode VLAN
//     among them, now that dual-mode no longer points at it).
//  6. Add a new VLAN-context untagged membership (access port).
//
// The device's actual `dual-mode` line (read from the running config on every
// operation) is the source of truth for steps 1 and 4, so an interface whose
// dual-mode drifted by hand is healed on the next apply that touches it.
func (r *InterfaceEthernetResource) buildDualModeMembershipCommands(ctx context.Context, plan *InterfaceEthernetResourceModel, oldState *InterfaceEthernetResourceModel, config *parser.RunningConfig, diags *diag.Diagnostics) []string {
	port := plan.Port.ValueString()
	var commands []string

	// Desired membership.
	newTagged := r.resolveTaggedVLANs(ctx, plan, config, diags)
	if diags.HasError() {
		return nil
	}
	var newUntagged int
	if !plan.UntaggedVLAN.IsNull() {
		newUntagged = int(plan.UntaggedVLAN.ValueInt64())
	}
	newDual := newUntagged != 0 && len(newTagged) > 0
	newVLANTagged, newVLANUntagged := dualModeVLANContextSets(newTagged, newUntagged, newDual)

	// Previous membership, as recorded in state.
	var oldVLANTagged []int
	var oldVLANUntagged int
	if oldState != nil {
		oldTagged := r.resolveTaggedVLANs(ctx, oldState, config, diags)
		if diags.HasError() {
			return nil
		}
		var oldUntagged int
		if !oldState.UntaggedVLAN.IsNull() {
			oldUntagged = int(oldState.UntaggedVLAN.ValueInt64())
		}
		oldDual := oldUntagged != 0 && len(oldTagged) > 0
		oldVLANTagged, oldVLANUntagged = dualModeVLANContextSets(oldTagged, oldUntagged, oldDual)
	}

	// The device's current dual-mode VLAN, if any.
	var deviceDual *int
	if iface := config.FindEthernetInterface(port); iface != nil {
		deviceDual = iface.DualModeVLAN
	}

	// 1. Leave dual-mode.
	if !newDual && deviceDual != nil {
		commands = append(commands,
			fmt.Sprintf("interface ethernet %s", port),
			"no dual-mode",
			"exit",
		)
	}

	// 2. Remove old VLAN-context untagged membership.
	if oldVLANUntagged != 0 && oldVLANUntagged != newVLANUntagged {
		commands = append(commands,
			fmt.Sprintf("vlan %d", oldVLANUntagged),
			fmt.Sprintf("no untagged ethe %s", port),
			"exit",
		)
	}

	// 3. Add new tagged membership (including the dual-mode VLAN).
	for _, vlanID := range newVLANTagged {
		if !intSliceContains(oldVLANTagged, vlanID) {
			commands = append(commands,
				fmt.Sprintf("vlan %d", vlanID),
				fmt.Sprintf("tagged ethe %s", port),
				"exit",
			)
		}
	}

	// 4. Set dual-mode to the untagged VLAN.
	if newDual && (deviceDual == nil || *deviceDual != newUntagged) {
		commands = append(commands,
			fmt.Sprintf("interface ethernet %s", port),
			dualModeCommand(newUntagged),
			"exit",
		)
	}

	// 5. Remove old tagged membership no longer wanted.
	for _, vlanID := range oldVLANTagged {
		if !intSliceContains(newVLANTagged, vlanID) {
			commands = append(commands,
				fmt.Sprintf("vlan %d", vlanID),
				fmt.Sprintf("no tagged ethe %s", port),
				"exit",
			)
		}
	}

	// 6. Add new VLAN-context untagged membership (access port).
	if newVLANUntagged != 0 && newVLANUntagged != oldVLANUntagged {
		commands = append(commands,
			fmt.Sprintf("vlan %d", newVLANUntagged),
			fmt.Sprintf("untagged ethe %s", port),
			"exit",
		)
	}

	return commands
}

// dualModeVLANContextSets maps a resource's (tagged_vlans, untagged_vlan) to
// the VLAN-context memberships the switch must hold on dual-mode firmware: a
// dual-mode port is a tagged member of its untagged VLAN (unless that is the
// default VLAN 1, which needs no entry) and has no VLAN-context untagged
// membership; an access port is the reverse. The returned tagged list is
// sorted.
func dualModeVLANContextSets(tagged []int, untagged int, dual bool) (vlanTagged []int, vlanUntagged int) {
	vlanTagged = append(vlanTagged, tagged...)
	if dual {
		if untagged != 1 && !intSliceContains(vlanTagged, untagged) {
			vlanTagged = append(vlanTagged, untagged)
		}
		sort.Ints(vlanTagged)
		return vlanTagged, 0
	}
	sort.Ints(vlanTagged)
	return vlanTagged, untagged
}

// buildVLANRemovalCommands removes all VLAN membership for a port.
//
// On dual-mode firmware the port's `dual-mode` line is cleared first, so the
// subsequent `no tagged ethe` for the dual-mode VLAN is not rejected for a
// port that still depends on it. The check is on the device's own config line,
// so it needs no firmware gating: only firmware that has the command prints it.
func (r *InterfaceEthernetResource) buildVLANRemovalCommands(ctx context.Context, port string, state *InterfaceEthernetResourceModel, config *parser.RunningConfig, diags *diag.Diagnostics) []string {
	var commands []string

	if iface := config.FindEthernetInterface(port); iface != nil && iface.DualModeVLAN != nil {
		commands = append(commands,
			fmt.Sprintf("interface ethernet %s", port),
			"no dual-mode",
			"exit",
		)
	}

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
//
// When the interface carries a `dual-mode [vlan]` line (FastIron 08.0.30-era
// trunk-untagged dialect), that VLAN is the port's untagged VLAN regardless of
// the VLAN-context lists, and it is excluded from tagged_vlans even though the
// VLAN context lists the port as tagged in it — mirroring what
// buildDualModeMembershipCommands writes, so a dual-mode trunk plans clean.
// Interfaces without the line (every port on 08.0.95, and access ports on
// 08.0.30) are read from VLAN context exactly as before.
func (r *InterfaceEthernetResource) readVLANMembership(ctx context.Context, port string, iface *parser.EthernetInterface, config *parser.RunningConfig, state *InterfaceEthernetResourceModel, diags *diag.Diagnostics) {
	var taggedVLANs []int64
	var untaggedVLAN int

	var dualModeVLAN int
	if iface != nil && iface.DualModeVLAN != nil {
		dualModeVLAN = *iface.DualModeVLAN
		untaggedVLAN = dualModeVLAN
	}

	for _, vlan := range config.VLANs {
		if vlan.ID == 1 {
			continue
		}
		for _, p := range vlan.TaggedPorts {
			if p == port {
				if vlan.ID != dualModeVLAN {
					taggedVLANs = append(taggedVLANs, int64(vlan.ID))
				}
				break
			}
		}
		if dualModeVLAN != 0 {
			continue // dual-mode decides the untagged VLAN
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
