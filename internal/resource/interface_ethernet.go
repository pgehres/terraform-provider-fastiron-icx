package resource

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &InterfaceEthernetResource{}
	_ resource.ResourceWithImportState = &InterfaceEthernetResource{}
	_ resource.ResourceWithModifyPlan  = &InterfaceEthernetResource{}
)

// rePort matches FastIron port identifiers in unit/slot/port format (e.g., "1/1/15").
var rePort = regexp.MustCompile(`^\d+/\d+/\d+$`)

// rePortName matches valid FastIron port names: no control characters or double
// quotes. Spaces are allowed — quotePortName wraps them in double quotes for
// the CLI command.
var rePortName = regexp.MustCompile(`^[^"\x00-\x1f]+$`)

type InterfaceEthernetResource struct {
	client  sshclient.CommandExecutor
	dialect dialect.Dialect
	// nativeBulkTagging is the provider's experimental native_bulk_tagging
	// opt-in (default false). See bulkTaggingAllowed.
	nativeBulkTagging bool
	// ports is the provider instance's registry of ports claimed during the
	// current operation; nil (never configured) disables duplicate detection.
	ports *providerdata.PortRegistry
}

type InterfaceEthernetResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Port                 types.String `tfsdk:"port"`
	PortName             types.String `tfsdk:"port_name"`
	SpanningTreePt2PtMac types.Bool   `tfsdk:"spanning_tree_pt2pt_mac"`
	OpticalMonitor       types.Bool   `tfsdk:"optical_monitor"`
	Enabled              types.Bool   `tfsdk:"enabled"`
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
			"enabled": schema.BoolAttribute{
				Description: "Administrative state of the port: false sends `disable` (admin-down), true sends `enable`. " +
					"When omitted the port's current state is kept and recorded in state (zero-diff adoption), so " +
					"a port disabled by hand stays disabled until the attribute is set explicitly. Destroy re-enables " +
					"a port this resource disabled. Disabling the port that carries the switch's management next hop " +
					"is refused at plan time; see the resource documentation.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"untagged_vlan": schema.Int64Attribute{
				Description: "VLAN ID for untagged traffic on this port. The port will be set as untagged in this VLAN.",
				Optional:    true,
				Validators: []validator.Int64{
					int64validator.Between(1, 4094),
				},
			},
			"tagged_vlans": schema.SetAttribute{
				Description: "Set of VLAN IDs for tagged traffic on this port. Mutually exclusive with tag_all_vlans.",
				Optional:    true,
				ElementType: types.Int64Type,
				Validators: []validator.Set{
					setvalidator.ValueInt64sAre(
						int64validator.Between(1, 4094),
					),
				},
			},
			"tag_all_vlans": schema.BoolAttribute{
				Description: "Tag all VLANs on the switch (excluding untagged_vlan and VLAN 1) on this port. Useful for trunk ports. Mutually exclusive with tagged_vlans. Read checks the port's actual membership against that expansion: when a VLAN is missing, state reports tag_all_vlans = false with the actual tagged_vlans so the plan shows the repair, which applies additively.",
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
					// hostname / ip address / ip default-gateway bypass their
					// dedicated attributes' safeguards (see rawguard.go).
					guardedRawConfigListValidator(),
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
	r.dialect = data.Dialect
	r.nativeBulkTagging = data.NativeBulkTagging
	r.ports = data.Ports
}

// ModifyPlan runs two plan-time checks. First it rejects a port that another
// icx_interface_ethernet resource on the same provider instance has already
// claimed in this operation: two resources on one port clobber each other on
// every apply and the plan never converges (CLAUDE.md gotcha #10), and nothing
// in Terraform itself catches it — see providerdata.PortRegistry for what is
// and is not detected. Then it refuses to disable the port the switch reaches
// its management next hop through (see mgmtpath.go); that check runs only when
// the plan turns enabled from true (or unrecorded) to false, costs one
// running-config read plus one `show arp`, and sends no configuration. The
// same check is repeated in Create/Update as a backstop for a value only known
// at apply. Destroy plans skip both: they are not claims (the duplicate must
// stay removable) and they disable nothing.
func (r *InterfaceEthernetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	if r.ports != nil {
		r.claimPort(req.Config, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if r.client == nil {
		return
	}
	var plan InterfaceEthernetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state *InterfaceEthernetResourceModel
	if !req.State.Raw.IsNull() {
		state = &InterfaceEthernetResourceModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if !disablesPort(&plan, state) {
		return
	}
	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddWarning("Cannot verify the management path",
			"enabled = false will administratively disable port "+plan.Port.ValueString()+
				", but the running config could not be read to check whether that port carries the provider's "+
				"own session: "+err.Error())
		return
	}
	checkManagementPathDisable(r.client, config, plan.Port.ValueString(), &resp.Diagnostics)
}

// claimPort registers config's port with the provider's port registry and
// turns a conflicting earlier claim into a plan error.
func (r *InterfaceEthernetResource) claimPort(config tfsdk.Config, diags *diag.Diagnostics) {
	if config.Raw.IsNull() || !config.Raw.IsKnown() {
		return
	}
	// tftypes.Value.As hands back the Value's own attribute map, not a copy,
	// so build a separate map rather than deleting from it: the request's
	// Config must stay intact for the checks that run after this one.
	var raw map[string]tftypes.Value
	if err := config.Raw.As(&raw); err != nil {
		diags.AddError("Unexpected configuration shape", err.Error())
		return
	}
	portVal, ok := raw["port"]
	if !ok || portVal.IsNull() || !portVal.IsFullyKnown() {
		return // the port is not known until apply; nothing to compare yet
	}
	var port string
	if err := portVal.As(&port); err != nil {
		diags.AddError("Unexpected port value", err.Error())
		return
	}
	attrs := make(map[string]tftypes.Value, len(raw))
	for k, v := range raw {
		if k != "port" && k != "id" {
			attrs[k] = v
		}
	}

	conflicts := r.ports.Claim(port, attrs)
	if len(conflicts) == 0 {
		return
	}
	diags.AddAttributeError(path.Root("port"),
		fmt.Sprintf("Port %s is managed by more than one icx_interface_ethernet resource", port),
		fmt.Sprintf("Another icx_interface_ethernet resource using this provider instance also declares port %s, "+
			"with a different %s. Each apply would overwrite the other's port settings and VLAN membership, "+
			"so the plan can never converge and the port's membership is undefined. Keep a single resource "+
			"for the port. Before deleting the redundant resource from configuration, remove it from state "+
			"with `terraform state rm <address>`: otherwise its destroy strips VLAN membership the surviving "+
			"resource still owns, which on an uplink can cut the switch off.",
			port, strings.Join(conflicts, ", ")))
}

// dialectFor returns the dialect to use against config: the Configure-time
// detection, refined with evidence from config itself (its version when the
// provider could not detect one, and any `dual-mode` lines it contains).
func (r *InterfaceEthernetResource) dialectFor(config *parser.RunningConfig) dialect.Dialect {
	return r.dialect.Observe(config)
}

// boolKnown reports whether b holds a value (neither null nor unknown).
func boolKnown(b types.Bool) bool { return !b.IsNull() && !b.IsUnknown() }

// disablesPort reports whether applying plan over state administratively
// disables the port: the plan says enabled = false and state does not already
// record it as disabled. state may be nil (create).
func disablesPort(plan, state *InterfaceEthernetResourceModel) bool {
	if !boolKnown(plan.Enabled) || plan.Enabled.ValueBool() {
		return false
	}
	return state == nil || !boolKnown(state.Enabled) || state.Enabled.ValueBool()
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

	// enabled omitted: adopt the port's current admin state and send nothing
	// for it, so existing fleets pick the attribute up with a zero-diff plan.
	deviceDisabled := false
	if iface := config.FindEthernetInterface(plan.Port.ValueString()); iface != nil {
		deviceDisabled = iface.Disabled
	}
	if boolKnown(plan.Enabled) && !plan.Enabled.ValueBool() &&
		!checkManagementPathDisable(r.client, config, plan.Port.ValueString(), &resp.Diagnostics) {
		return
	}

	// Apply interface-level commands (built before the adoption below, so an
	// adopted value is never sent as a command).
	ifCommands := r.buildInterfaceCommands(&plan, deviceDisabled, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !boolKnown(plan.Enabled) {
		plan.Enabled = types.BoolValue(!deviceDisabled)
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
		execErr := r.client.ExecuteInConfigMode(vlanCommands)
		if !r.sentBulk(vlanCommands, config) {
			if execErr != nil {
				resp.Diagnostics.AddError("Failed to configure VLAN membership", execErr.Error())
				return
			}
		} else if _, failed := r.finishBulkTagging(execErr, &plan, config, &resp.Diagnostics); failed {
			// Deliberately no state: a Create that returns an error with
			// state set leaves the resource tainted, and the replace that
			// follows strips every VLAN from the trunk (CLAUDE.md gotcha #9).
			// Whatever the bulk command did has been saved; the next apply
			// re-runs Create additively.
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
		state.Enabled = types.BoolValue(!iface.Disabled)
	} else {
		state.PortName = types.StringNull()
		state.SpanningTreePt2PtMac = types.BoolValue(false)
		state.OpticalMonitor = types.BoolValue(true)
		state.Enabled = types.BoolValue(true)
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

	// enabled unknown (omitted from config, nothing recorded yet): keep
	// whatever state has; the port's admin state is not touched.
	if !boolKnown(plan.Enabled) {
		plan.Enabled = state.Enabled
	}
	if disablesPort(&plan, &state) && !checkManagementPathDisable(r.client, config, plan.Port.ValueString(), &resp.Diagnostics) {
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
		execErr := r.client.ExecuteInConfigMode(vlanCommands)
		if !r.sentBulk(vlanCommands, config) {
			if execErr != nil {
				resp.Diagnostics.AddError("Failed to update VLAN membership", execErr.Error())
				return
			}
		} else if after, failed := r.finishBulkTagging(execErr, &plan, config, &resp.Diagnostics); failed {
			// Record what the device actually has, as an explicit list with
			// tag_all_vlans = false, so the next plan shows the remaining
			// VLANs as an ordinary additive diff instead of hiding them
			// behind tag_all_vlans (CLAUDE.md gotcha #8).
			if after != nil {
				plan.ID = state.ID
				plan.TagAllVLANs = types.BoolValue(false)
				r.readVLANMembership(ctx, plan.Port.ValueString(), after.FindEthernetInterface(plan.Port.ValueString()), after, &plan, &resp.Diagnostics)
				resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			}
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
	// A port this resource disabled comes back up: enabled is the default
	// state Delete returns every other attribute to.
	if boolKnown(state.Enabled) && !state.Enabled.ValueBool() {
		commands = append(commands, "enable")
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

// buildInterfaceCommands generates commands for creating an interface
// configuration. deviceDisabled is the port's current admin state, so that an
// explicit enabled = true only sends `enable` when the port is actually down.
func (r *InterfaceEthernetResource) buildInterfaceCommands(plan *InterfaceEthernetResourceModel, deviceDisabled bool, diags *diag.Diagnostics) []string {
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

	if boolKnown(plan.Enabled) {
		if !plan.Enabled.ValueBool() {
			commands = append(commands, "disable")
		} else if deviceDisabled {
			commands = append(commands, "enable")
		}
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

	// Admin state. Both sides known and different; an unknown plan value was
	// already replaced by state in Update.
	if boolKnown(plan.Enabled) && boolKnown(state.Enabled) && plan.Enabled.ValueBool() != state.Enabled.ValueBool() {
		if plan.Enabled.ValueBool() {
			innerCommands = append(innerCommands, "enable")
		} else {
			innerCommands = append(innerCommands, "disable")
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
		return expandAllVLANs(config, int(untaggedVLAN))
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

// expandAllVLANs returns, sorted, the membership tag_all_vlans stands for on a
// device with this running config: every VLAN except the default VLAN 1 and the
// port's own untagged VLAN. Create, Update and Read all go through this one
// expansion, so what Read expects is exactly what an apply would write.
func expandAllVLANs(config *parser.RunningConfig, untaggedVLAN int) []int {
	var vlanIDs []int
	for _, v := range config.VLANs {
		if v.ID == untaggedVLAN || v.ID == 1 {
			continue
		}
		vlanIDs = append(vlanIDs, v.ID)
	}
	sort.Ints(vlanIDs)
	return vlanIDs
}

// buildVLANMembershipCommands generates VLAN commands for port membership.
// If oldState is nil, this is a create operation; otherwise it's an update.
//
// On FastIron releases that have the `dual-mode` interface command (08.0.30
// through 08.0.7x — dialect.CapDualMode) the trunk-untagged case is
// expressed differently and is handled by buildDualModeMembershipCommands.
// On 08.0.80+ (the ICX 7000-series fleet on 08.0.95) this function's
// VLAN-context-only form is used unchanged.
func (r *InterfaceEthernetResource) buildVLANMembershipCommands(ctx context.Context, plan *InterfaceEthernetResourceModel, oldState *InterfaceEthernetResourceModel, config *parser.RunningConfig, diags *diag.Diagnostics) []string {
	if r.dialectFor(config).Has(dialect.CapDualMode) {
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

	// Add new tagged VLAN membership: one `tagged ethe` per VLAN, or — only
	// when the provider opted in and the port is a tagged-only, non-management
	// tag_all_vlans trunk (bulkTaggingAllowed) — the single bulk command.
	var toAdd []int
	for _, vlanID := range newTaggedVLANs {
		if !intSliceContains(oldTaggedVLANs, vlanID) {
			toAdd = append(toAdd, vlanID)
		}
	}
	if len(toAdd) > 0 && r.bulkTaggingAllowed(plan, config) {
		commands = append(commands,
			fmt.Sprintf("interface ethernet %s", port),
			r.dialectFor(config).VlanConfigAddAllTagged(),
			"exit",
		)
		return commands
	}
	for _, vlanID := range toAdd {
		commands = append(commands,
			fmt.Sprintf("vlan %d", vlanID),
			fmt.Sprintf("tagged ethe %s", port),
			"exit",
		)
	}

	return commands
}

// bulkTaggingAllowed reports whether this tag_all_vlans apply may add its
// tagged membership with the single interface command
// `vlan-config add all-tagged` (dialect.CapVlanConfigAllTagged, FastIron
// 08.0.50+) instead of one `tagged ethe` per VLAN.
//
// The command is EXPERIMENTAL and strictly opt-in: it is never used unless the
// provider sets native_bulk_tagging = true, and with that off every command
// stream is the per-VLAN expansion byte for byte. Its documentation does not
// say what it does to the default VLAN 1 or to the port's own untagged VLAN.
// If it converted an uplink's untagged management VLAN to tagged, the SSH
// session would die before any read-back could correct it — the same class of
// self-inflicted outage as gotcha #9. So even when opted in it is used only on
// a tagged-only trunk, and the per-VLAN form (which exists everywhere and is
// always safe to fall back to) is used whenever:
//
//   - tag_all_vlans is off (the command is only ever a spelling of it);
//   - the plan sets untagged_vlan, or the device shows the port as an untagged
//     member of any VLAN (Create does not reconcile membership it was never
//     told about, so the device is checked as well as the plan);
//   - the dialect has dual-mode (08.0.5x-08.0.7x take the dual-mode builder
//     first), lacks the command, or has an unknown version;
//   - the port is the switch's detected management path (mgmtpath.go), or
//     that path cannot be determined. This is the only check that costs a
//     device read (`show arp`), so it runs last and only when everything
//     else already allows bulk.
//
// The command is a one-shot: what `write memory` saves is ordinary per-VLAN
// membership, so Read and Delete still work per VLAN and a VLAN created later
// is still handled by icx_vlan.Create.
func (r *InterfaceEthernetResource) bulkTaggingAllowed(plan *InterfaceEthernetResourceModel, config *parser.RunningConfig) bool {
	if !r.nativeBulkTagging || !plan.TagAllVLANs.ValueBool() {
		return false
	}
	if !plan.UntaggedVLAN.IsNull() && !plan.UntaggedVLAN.IsUnknown() && plan.UntaggedVLAN.ValueInt64() != 0 {
		return false
	}
	d := r.dialectFor(config)
	if !d.Known || d.Has(dialect.CapDualMode) || !d.Has(dialect.CapVlanConfigAllTagged) {
		return false
	}
	port := plan.Port.ValueString()
	for _, vlan := range config.VLANs {
		if stringSliceContains(vlan.UntaggedPorts, port) {
			return false
		}
	}
	found, reason := managementPathPorts(r.client, config)
	if reason != "" {
		return false
	}
	for _, p := range found {
		if p == port {
			return false
		}
	}
	return true
}

// sentBulk reports whether the membership stream contains the bulk command.
func (r *InterfaceEthernetResource) sentBulk(sent []string, before *parser.RunningConfig) bool {
	return stringSliceContains(sent, r.dialectFor(before).VlanConfigAddAllTagged())
}

// finishBulkTagging runs after a membership stream containing the bulk
// command was executed, successfully or not (execErr). It re-reads the running
// config and removes any tag the per-VLAN expansion would not have set (see
// bulkTaggingCorrections) — the command is undocumented on VLAN 1, so this is
// best-effort until validated on hardware.
//
// When execErr is set (the switch printed an `error - ...` line, or the
// session failed part-way), the bulk command may have tagged some VLANs before
// refusing the rest. Rather than leave that partial change in the running
// config only — unsaved, divergent from startup, and re-attempted on every
// apply — it still verifies, saves what succeeded with `write memory`, and
// reports the error. It returns the re-read config (nil if that read failed),
// from which the caller records state, and whether the apply failed.
func (r *InterfaceEthernetResource) finishBulkTagging(execErr error, plan *InterfaceEthernetResourceModel, before *parser.RunningConfig, diags *diag.Diagnostics) (*parser.RunningConfig, bool) {
	bulk := r.dialectFor(before).VlanConfigAddAllTagged()
	port := plan.Port.ValueString()
	failed := execErr != nil

	after, readErr := r.getRunningConfig()
	if readErr != nil {
		failed = true
		diags.AddError("Failed to verify bulk VLAN tagging",
			fmt.Sprintf("`%s` was sent for port %s but the running config could not be re-read to check its effect: %s",
				bulk, port, readErr.Error()))
	} else {
		var untagged int
		if !plan.UntaggedVLAN.IsNull() && !plan.UntaggedVLAN.IsUnknown() {
			untagged = int(plan.UntaggedVLAN.ValueInt64())
		}
		if corrections := bulkTaggingCorrections(after, port, untagged); len(corrections) > 0 {
			if err := r.client.ExecuteInConfigMode(corrections); err != nil {
				failed = true
				diags.AddError("Failed to correct bulk VLAN tagging", err.Error())
			}
		}
	}
	if execErr == nil {
		// Success (or a verification failure): the caller's normal path
		// saves, or its error return skips the save as before.
		return after, failed
	}

	saved := "The partial change has been saved with `write memory`, so the running and startup configs agree."
	if err := r.client.WriteMemory(); err != nil {
		saved = "Saving the partial change with `write memory` also failed (" + err.Error() +
			"), so the running config may differ from the startup config until the next successful apply."
	}
	diags.AddError("Bulk VLAN tagging failed",
		fmt.Sprintf("`%s` on port %s failed: %s. It may have tagged the port in some VLANs before failing. %s "+
			"On an update, state records the port's membership as read back from the device (tag_all_vlans = false "+
			"with the actual tagged_vlans), so the next plan shows the remaining VLANs as an additive change; on a "+
			"create no state is recorded and the next apply re-runs the create, which is additive. Remove the configuration the switch objected to, or set "+
			"native_bulk_tagging = false on the provider to use one `tagged ethe` per VLAN.",
			bulk, port, execErr.Error(), saved))
	return after, true
}

// bulkTaggingCorrections returns the VLAN-context commands that bring port's
// membership in config back to what the tag_all_vlans expansion stands for:
// tagged in no VLAN the expansion excludes (VLAN 1 and untaggedVLAN, which is
// 0 for a port without one), and still untagged in untaggedVLAN.
func bulkTaggingCorrections(config *parser.RunningConfig, port string, untaggedVLAN int) []string {
	var commands []string
	for _, vlan := range config.VLANs {
		if vlan.ID != 1 && vlan.ID != untaggedVLAN {
			continue
		}
		tagged := stringSliceContains(vlan.TaggedPorts, port)
		isUntagged := stringSliceContains(vlan.UntaggedPorts, port)
		if !tagged && (vlan.ID == 1 || isUntagged) {
			continue
		}
		commands = append(commands, fmt.Sprintf("vlan %d", vlan.ID))
		if tagged {
			commands = append(commands, fmt.Sprintf("no tagged ethe %s", port))
		}
		if vlan.ID == untaggedVLAN && !isUntagged {
			commands = append(commands, fmt.Sprintf("untagged ethe %s", port))
		}
		commands = append(commands, "exit")
	}
	return commands
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
	d := r.dialectFor(config)
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
			d.NoDualMode(),
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
			d.DualMode(newUntagged),
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
			r.dialectFor(config).NoDualMode(),
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
// A port in state with tag_all_vlans = true is checked against the expansion
// that option stands for (see reconcileTagAllVLANs): when the device matches,
// state is left exactly as it was, so a healthy trunk plans clean; when it
// does not, state reports the device truth so the plan shows the repair.
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

	if state.TagAllVLANs.ValueBool() {
		r.reconcileTagAllVLANs(ctx, port, taggedVLANs, untaggedVLAN, config, state, diags)
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

// reconcileTagAllVLANs is Read's half of tag_all_vlans (CLAUDE.md gotcha #8).
//
// tag_all_vlans cannot be read from the switch, but what it promises can be:
// the port tagged in every VLAN that expandAllVLANs returns. When the device
// honours that promise, state is left untouched — tagged_vlans stays whatever
// the last apply stored (null for a plain tag_all_vlans resource), so there is
// no diff and no perpetual churn. When a VLAN is missing (a hand-run `no tagged
// ethe`, a VLAN that predates Terraform on this switch, an apply that died
// halfway), state is rewritten to what the device actually has: tag_all_vlans =
// false and tagged_vlans = the real tagged set. Against a configuration that
// still says tag_all_vlans = true that is an ordinary update, and Update then
// resolves the old side from the explicit set and the new side from the
// expansion, so it issues `tagged ethe` for exactly the missing VLANs.
//
// The repair is additive by construction. The expansion is every VLAN except
// VLAN 1 and the port's untagged VLAN, and a FastIron port cannot be both
// tagged and untagged in one VLAN, so the device's tagged set is always a
// subset of the expansion: Update has nothing to remove. (The one removal a
// tag_all_vlans trunk can see is when untagged_vlan itself changes, which is an
// explicit configuration change, not drift.) Should a device ever report a
// tagged VLAN outside the expansion, the warning names it, because the apply
// would drop that membership.
//
// On dual-mode firmware the dual-mode VLAN is the untagged VLAN and is already
// excluded from actual by the caller, mirroring buildDualModeMembershipCommands.
func (r *InterfaceEthernetResource) reconcileTagAllVLANs(ctx context.Context, port string, actual []int64, untaggedVLAN int, config *parser.RunningConfig, state *InterfaceEthernetResourceModel, diags *diag.Diagnostics) {
	expected := expandAllVLANs(config, untaggedVLAN)

	have := make(map[int]bool, len(actual))
	for _, id := range actual {
		have[int(id)] = true
	}
	var missing, extra []int
	for _, id := range expected {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	for _, id := range actual {
		if !intSliceContains(expected, int(id)) {
			extra = append(extra, int(id))
		}
	}
	sort.Ints(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return
	}

	state.TagAllVLANs = types.BoolValue(false)
	if len(actual) > 0 {
		state.TaggedVLANs = int64SliceToSet(ctx, actual, diags)
	} else {
		state.TaggedVLANs = types.SetNull(types.Int64Type)
	}

	var detail strings.Builder
	fmt.Fprintf(&detail, "Port %s is declared tag_all_vlans = true, but the switch does not tag it in every VLAN.", port)
	if len(missing) > 0 {
		fmt.Fprintf(&detail, " Missing from the port: VLAN %s.", joinInts(missing))
	}
	if len(extra) > 0 {
		fmt.Fprintf(&detail, " Tagged outside the expansion: VLAN %s (an apply removes those memberships; check them first).", joinInts(extra))
	}
	detail.WriteString(" State now records the port's actual membership (tag_all_vlans = false, tagged_vlans = ")
	if len(actual) > 0 {
		fmt.Fprintf(&detail, "[%s]", joinInts64(actual))
	} else {
		detail.WriteString("null")
	}
	detail.WriteString("), so the plan shows an update back to tag_all_vlans = true; applying it tags the missing VLANs and removes nothing else." +
		" A host on a missing VLAN typically has link and a correct address but never gets an ARP reply: this port is a transit trunk that drops its frames.")
	diags.AddAttributeWarning(path.Root("tag_all_vlans"),
		fmt.Sprintf("tag_all_vlans trunk %s has drifted", port), detail.String())
}

func joinInts(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ", ")
}

func joinInts64(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ", ")
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
