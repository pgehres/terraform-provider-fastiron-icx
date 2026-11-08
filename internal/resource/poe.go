package resource

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

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
	_ resource.Resource                     = &PoEResource{}
	_ resource.ResourceWithImportState      = &PoEResource{}
	_ resource.ResourceWithConfigValidators = &PoEResource{}
)

type PoEResource struct {
	client sshclient.CommandExecutor
}

type PoEResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Port       types.String `tfsdk:"port"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	PowerLimit types.Int64  `tfsdk:"power_limit"`
	Priority   types.Int64  `tfsdk:"priority"`
}

func NewPoEResource() resource.Resource {
	return &PoEResource{}
}

func (r *PoEResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_poe"
}

func (r *PoEResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages Power over Ethernet (PoE) settings on an ICX switch port. " +
			"The admin state is read from 'show inline power <port>' and the options from the port's " +
			"running configuration, so changes made on the switch show as plan diffs. " +
			"A port has at most one PoE option: power_limit and priority cannot both be set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"port": schema.StringAttribute{
				Description: "Port identifier in unit/module/port format (e.g., \"1/1/1\").",
				Required:    true,
			},
			"enabled": schema.BoolAttribute{
				Description: "Enable inline power (PoE) on this port.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"power_limit": schema.Int64Attribute{
				Description: "Power limit in milliwatts for this port (`inline power power-limit`). " +
					"Conflicts with priority. Requires enabled = true.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"priority": schema.Int64Attribute{
				Description: "PoE priority class for power management (`inline power priority`): " +
					"1 (highest) to 3 (lowest, the default). Conflicts with power_limit. Requires enabled = true.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.Between(1, 3)},
			},
		},
	}
}

// ConfigValidators rejects option combinations FastIron cannot hold: one
// option per port, and no option on a port with PoE off.
func (r *PoEResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.Conflicting(path.MatchRoot("power_limit"), path.MatchRoot("priority")),
		poeOptionNeedsEnabled{},
	}
}

// poeOptionNeedsEnabled refuses power_limit or priority with enabled = false:
// `no inline power` leaves no option on the port, so the plan could never
// converge.
type poeOptionNeedsEnabled struct{}

func (poeOptionNeedsEnabled) Description(context.Context) string {
	return "power_limit and priority require enabled = true"
}

func (v poeOptionNeedsEnabled) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v poeOptionNeedsEnabled) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg PoEResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Enabled.IsNull() || cfg.Enabled.IsUnknown() || cfg.Enabled.ValueBool() {
		return
	}
	for _, opt := range []struct {
		name string
		val  types.Int64
	}{{"power_limit", cfg.PowerLimit}, {"priority", cfg.Priority}} {
		if !opt.val.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root(opt.name), "PoE option on a disabled port",
				opt.name+" cannot be set with enabled = false: a port with PoE off has no PoE options.")
		}
	}
}

func (r *PoEResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *PoEResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PoEResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := r.buildCommands(&plan)

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to configure PoE", err.Error())
			return
		}
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}

	plan.ID = types.StringValue(plan.Port.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PoEResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PoEResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	device, err := r.readDevice(state.Port.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read PoE state", err.Error())
		return
	}
	state.Enabled = types.BoolValue(device.enabled)
	state.PowerLimit = int64OrNull(device.powerLimit)
	state.Priority = int64OrNull(device.priority)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// poeDevice is a port's PoE state as read from the switch.
type poeDevice struct {
	enabled    bool
	powerLimit *int64
	priority   *int64
}

// readDevice reads a port's PoE state. The admin state comes from
// `show inline power <port>`, because whether a port without an
// ` inline power` line is powered depends on the model (on by default on an
// ICX 7450, an explicit line on the ICX 7150 fixtures). The options come from
// the interface stanza of the running configuration.
func (r *PoEResource) readDevice(port string) (poeDevice, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return poeDevice{}, fmt.Errorf("read running config: %w", err)
	}
	cfg, err := parser.ParseRunningConfig(output)
	if err != nil {
		return poeDevice{}, fmt.Errorf("parse running config: %w", err)
	}
	var dev poeDevice
	for _, iface := range cfg.EthernetInterfaces {
		if iface.Port == port {
			dev.powerLimit = iface.PoE.PowerLimit
			dev.priority = iface.PoE.Priority
		}
	}

	show, err := r.client.Execute("show inline power " + port)
	if err != nil {
		return poeDevice{}, fmt.Errorf("show inline power %s: %w", port, err)
	}
	row, ok := parser.ParseInlinePower(show)[port]
	if !ok {
		return poeDevice{}, fmt.Errorf("no PoE status for port %s in `show inline power %s` (not a PoE port?): %q",
			port, port, strings.TrimSpace(show))
	}
	dev.enabled = row.AdminOn
	return dev, nil
}

func int64OrNull(v *int64) types.Int64 {
	if v == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*v)
}

func (r *PoEResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state PoEResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Enabled.Equal(state.Enabled) || !plan.PowerLimit.Equal(state.PowerLimit) || !plan.Priority.Equal(state.Priority) {
		if err := r.client.ExecuteInConfigMode(r.buildCommands(&plan)); err != nil {
			resp.Diagnostics.AddError("Failed to update PoE", err.Error())
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

func (r *PoEResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PoEResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A plain `inline power` removes any option and leaves PoE on. Never send
	// `no inline power <option>`: on FastIron 09.0.10 that turns PoE off.
	commands := []string{
		fmt.Sprintf("interface ethernet %s", state.Port.ValueString()),
		"inline power",
		"exit",
	}
	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		resp.Diagnostics.AddError("Failed to reset PoE", err.Error())
		return
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}
}

func (r *PoEResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("port"), req.ID)...)
}

// buildCommands returns the commands that put the port into the planned
// state. FastIron 09.0.10 keeps one ` inline power` option per port, and a new
// `inline power <option>` replaces the previous one, so one command sets the
// whole state: `no inline power`, `inline power power-limit N`,
// `inline power priority N`, or a plain `inline power` (on, no option).
func (r *PoEResource) buildCommands(plan *PoEResourceModel) []string {
	cmd := "inline power"
	switch {
	case !plan.Enabled.ValueBool():
		cmd = "no inline power"
	case !plan.PowerLimit.IsNull():
		cmd = fmt.Sprintf("inline power power-limit %d", plan.PowerLimit.ValueInt64())
	case !plan.Priority.IsNull():
		cmd = fmt.Sprintf("inline power priority %d", plan.Priority.ValueInt64())
	}
	return []string{fmt.Sprintf("interface ethernet %s", plan.Port.ValueString()), cmd, "exit"}
}
