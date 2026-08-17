package resource

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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
	_ resource.Resource                = &VLANResource{}
	_ resource.ResourceWithImportState = &VLANResource{}
)

// reVLANName matches valid FastIron VLAN names: no spaces, double quotes, or
// single quotes. Names are interpolated bare into the VLAN header command
// (`vlan N name <name> by port`), so any whitespace or quoting character
// would corrupt the command or the parser.
var reVLANName = regexp.MustCompile(`^[^\s"']+$`)

// reNoNewlines matches strings that contain no carriage returns or newlines.
// Used to reject raw_config elements that would inject additional CLI commands.
var reNoNewlines = regexp.MustCompile(`^[^\r\n]*$`)

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
				Validators: []validator.Int64{
					int64validator.Between(1, 4094),
				},
			},
			"name": schema.StringAttribute{
				Description: "VLAN name (1-31 chars). Must not contain spaces, double quotes, or single quotes; FastIron interpolates the name bare into the VLAN header command.",
				Optional:    true,
				Validators: []validator.String{
					// Spaces are rejected because the name is interpolated bare into
					// `vlan N name <name> by port`; quotes are rejected because they
					// would break the CLI command or the running-config parser.
					stringvalidator.RegexMatches(
						reVLANName,
						`VLAN name must not contain spaces, double quotes, or single quotes`,
					),
					stringvalidator.LengthBetween(1, 31),
				},
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

	// Reconcile "tag all VLANs" trunk ports. Interfaces configured with
	// tag_all_vlans = true have that option expanded to an explicit tagged-VLAN
	// list only when the icx_interface_ethernet resource itself is applied.
	// Adding a new VLAN produces no diff on those interface resources, so
	// Terraform never re-applies them and the new VLAN would silently be missing
	// from every trunk. Detect those ports from the running config and tag the
	// new VLAN onto them here. See findTrunkAllPorts for the detection rule.
	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}
	newVLANID := plan.VlanID.ValueInt64()
	for _, port := range findTrunkAllPorts(config, int(newVLANID)) {
		commands = append(commands,
			fmt.Sprintf("vlan %d", newVLANID),
			fmt.Sprintf("tagged ethe %s", port),
			"exit",
		)
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

	// FastIron 08.0.95 has no `no vlan-name` command: entering the VLAN context
	// without a name argument does NOT remove an existing name — it silently
	// leaves the old name in place (permanent invisible drift). If the operator
	// has cleared the name attribute, surface a clear error so they know a
	// destroy-and-recreate (or keeping the name) is required.
	stateHasName := !state.Name.IsNull() && state.Name.ValueString() != ""
	planHasName := !plan.Name.IsNull() && plan.Name.ValueString() != ""
	if stateHasName && !planHasName {
		diags.AddError(
			"Cannot remove VLAN name in place",
			fmt.Sprintf("FastIron does not support removing a VLAN name via CLI on firmware 08.0.95. "+
				"VLAN %d currently has name %q. To remove the name, destroy and recreate the VLAN, "+
				"or keep the name attribute set.", vlanID, state.Name.ValueString()),
		)
		return nil
	}

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

// trunkAllMinVLANs is the minimum number of existing non-default VLANs a port
// must be a tagged member of before it is inferred to be a "tag all VLANs"
// trunk. It guards against misclassifying an access or lightly-tagged port as a
// trunk when only a handful of VLANs exist.
const trunkAllMinVLANs = 3

// findTrunkAllPorts returns the ports that behave as "tag all VLANs" trunks:
// ports that are tagged members of every existing non-default VLAN, allowing a
// single exception for the port's own untagged VLAN.
//
// The FastIron CLI has no native "tag all present and future VLANs" primitive;
// the provider's tag_all_vlans option is expanded into an explicit per-VLAN
// tagged list at the time the icx_interface_ethernet resource is applied. When
// a VLAN is added later, those interface resources show no Terraform diff and
// are never re-applied, so the new VLAN ends up missing from every trunk. This
// helper lets icx_vlan.Create reconcile that by re-deriving the trunk ports
// from the running config so the new VLAN can be tagged onto them.
//
// newVLANID is excluded from consideration (it does not exist yet). A port must
// be tagged on at least trunkAllMinVLANs VLANs to qualify, which avoids false
// positives on small or lightly-configured switches.
func findTrunkAllPorts(config *parser.RunningConfig, newVLANID int) []string {
	type vlanInfo struct {
		tagged   map[string]bool
		untagged map[string]bool
	}

	var existing []vlanInfo
	ports := map[string]bool{}
	for _, v := range config.VLANs {
		if v.ID == 1 || v.ID == newVLANID {
			continue
		}
		tagged := make(map[string]bool, len(v.TaggedPorts))
		for _, p := range v.TaggedPorts {
			tagged[p] = true
			ports[p] = true
		}
		untagged := make(map[string]bool, len(v.UntaggedPorts))
		for _, p := range v.UntaggedPorts {
			untagged[p] = true
		}
		existing = append(existing, vlanInfo{tagged: tagged, untagged: untagged})
	}

	if len(existing) < trunkAllMinVLANs {
		return nil
	}

	var trunkPorts []string
	for port := range ports {
		taggedCount := 0
		qualifies := true
		for _, v := range existing {
			switch {
			case v.tagged[port]:
				taggedCount++
			case v.untagged[port]:
				// This VLAN is the port's untagged VLAN — the one allowed
				// exception for a trunk-all port.
			default:
				// Port is neither tagged nor untagged on this VLAN, so it is not
				// a full trunk.
				qualifies = false
			}
			if !qualifies {
				break
			}
		}
		if qualifies && taggedCount >= trunkAllMinVLANs {
			trunkPorts = append(trunkPorts, port)
		}
	}

	sort.Strings(trunkPorts)
	return trunkPorts
}
