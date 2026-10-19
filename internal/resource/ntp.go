package resource

import (
	"context"
	"fmt"
	"regexp"
	"strings"

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
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &NTPResource{}
	_ resource.ResourceWithImportState = &NTPResource{}
	_ resource.ResourceWithModifyPlan  = &NTPResource{}
)

// reNTPServer is the charset for an NTP server: an IPv4 address or a DNS
// name. Interpolated bare into `server %s` / `no server %s`, so no whitespace,
// quotes, `?` (CLI help) or control characters.
var reNTPServer = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)

// reTimezone matches `<region> <zone>` as `clock timezone` takes it: a region
// keyword (australia, europe, gmt, us) and a zone token such as "pacific",
// "cet" or "gmt+05:30". Case-insensitive because the device prints the zone
// capitalised ("us Pacific") while the CLI accepts it either way.
var reTimezone = regexp.MustCompile(`^(?i)(australia|europe|gmt|us) [A-Za-z0-9+:-]+$`)

type NTPResource struct {
	client  sshclient.CommandExecutor
	dialect dialect.Dialect
}

type NTPResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Servers    types.List   `tfsdk:"servers"`
	Serve      types.Bool   `tfsdk:"serve"`
	Timezone   types.String `tfsdk:"timezone"`
	SummerTime types.Bool   `tfsdk:"summer_time"`
}

func NewNTPResource() resource.Resource {
	return &NTPResource{}
}

func (r *NTPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ntp"
}

func (r *NTPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages NTP servers and the system clock settings (time zone, summer time) on an ICX switch. " +
			"This is a singleton resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"servers": schema.ListAttribute{
				Description: "NTP servers (`server <address>` in the `ntp` block), in order. IPv4 addresses; FastIron " +
					"08.0.x documents only IP addresses here, a DNS name is accepted by the provider but may be " +
					"rejected by the switch. Omitted or empty means no servers.",
				Optional:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					listvalidator.ValueStringsAre(
						stringvalidator.LengthBetween(1, 253),
						stringvalidator.RegexMatches(reNTPServer,
							"must be an IPv4 address or DNS name (letters, digits, '.', '-')"),
					),
					listvalidator.UniqueValues(),
				},
			},
			"serve": schema.BoolAttribute{
				Description: "Whether the switch serves time to downstream NTP clients. false sends `disable serve` " +
					"(client-only). Defaults to true, the device default.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
			},
			"timezone": schema.StringAttribute{
				Description: "System clock time zone as `<region> <zone>` for `clock timezone`, e.g. \"us pacific\", " +
					"\"europe cet\" or \"gmt gmt+05:30\". Compared case-insensitively with the device, which prints " +
					"e.g. \"us Pacific\". The europe and australia regions require FastIron 08.0.50 or later. " +
					"Omitted means the device default (GMT).",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 32),
					stringvalidator.RegexMatches(reTimezone,
						`must be "<region> <zone>" with region australia, europe, gmt or us (e.g. "us pacific")`),
				},
			},
			"summer_time": schema.BoolAttribute{
				Description: "Enable daylight saving time for the configured zone (`clock summer-time`, default dates). " +
					"Defaults to false.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
		},
	}
}

func (r *NTPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// timezoneNeedsRegions reports whether tz uses a region FastIron added in
// 08.0.50 (europe, australia).
func timezoneNeedsRegions(tz types.String) bool {
	if tz.IsNull() || tz.IsUnknown() {
		return false
	}
	region := strings.ToLower(strings.Fields(tz.ValueString())[0])
	return region == "europe" || region == "australia"
}

// checkCapabilities refuses a timezone region the device's firmware lacks.
func (r *NTPResource) checkCapabilities(plan *NTPResourceModel, diags *diag.Diagnostics) bool {
	if timezoneNeedsRegions(plan.Timezone) {
		return requireCapability(r.dialect, dialect.CapClockTimezoneRegions,
			fmt.Sprintf("The timezone region %q of icx_ntp", strings.Fields(plan.Timezone.ValueString())[0]), diags)
	}
	return true
}

func (r *NTPResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan NTPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.checkCapabilities(&plan, &resp.Diagnostics)
}

func (r *NTPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan NTPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.checkCapabilities(&plan, &resp.Diagnostics) {
		return
	}

	commands := r.buildCommands(ctx, &plan, nil, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.apply(commands, "configure", &resp.Diagnostics) {
		return
	}

	plan.ID = types.StringValue("ntp")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *NTPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state NTPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	if len(config.NTP.Servers) > 0 {
		list, d := types.ListValueFrom(ctx, types.StringType, config.NTP.Servers)
		resp.Diagnostics.Append(d...)
		state.Servers = list
	} else {
		state.Servers = types.ListNull(types.StringType)
	}
	state.Serve = types.BoolValue(!config.NTP.DisableServe)

	// The device capitalises the zone ("us Pacific"); keep the configured
	// spelling when it is the same zone.
	switch {
	case config.Clock.Timezone == "":
		state.Timezone = types.StringNull()
	case !state.Timezone.IsNull() && strings.EqualFold(state.Timezone.ValueString(), config.Clock.Timezone):
		// keep
	default:
		state.Timezone = types.StringValue(config.Clock.Timezone)
	}
	state.SummerTime = types.BoolValue(config.Clock.SummerTime)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *NTPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state NTPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.checkCapabilities(&plan, &resp.Diagnostics) {
		return
	}

	commands := r.buildCommands(ctx, &plan, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.apply(commands, "update", &resp.Diagnostics) {
		return
	}

	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *NTPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state NTPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Return every managed setting to the device default: no servers, serving
	// enabled, GMT, no summer time.
	var commands []string
	if state.SummerTime.ValueBool() {
		commands = append(commands, "no clock summer-time")
	}
	if !state.Timezone.IsNull() {
		commands = append(commands, "no clock timezone "+strings.ToLower(state.Timezone.ValueString()))
	}
	servers := listToStringSlice(ctx, state.Servers, &resp.Diagnostics)
	if len(servers) > 0 || !state.Serve.ValueBool() {
		commands = append(commands, "ntp")
		for _, s := range servers {
			commands = append(commands, "no server "+s)
		}
		if !state.Serve.ValueBool() {
			commands = append(commands, "no disable serve")
		}
		commands = append(commands, "exit")
	}
	r.apply(commands, "reset", &resp.Diagnostics)
}

func (r *NTPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), "ntp")...)
}

// buildCommands renders the commands that take the device from state (nil on
// create: assume device defaults) to plan. Clock settings go first, then one
// `ntp` block with removals before additions.
func (r *NTPResource) buildCommands(ctx context.Context, plan, state *NTPResourceModel, diags *diag.Diagnostics) []string {
	var commands []string

	var stateTZ types.String = types.StringNull()
	var stateSummer, stateServe = false, true
	var stateServers []string
	if state != nil {
		stateTZ = state.Timezone
		stateSummer = state.SummerTime.ValueBool()
		stateServe = state.Serve.ValueBool()
		stateServers = listToStringSlice(ctx, state.Servers, diags)
	}
	planServers := listToStringSlice(ctx, plan.Servers, diags)
	if diags.HasError() {
		return nil
	}

	// Time zone: compare case-insensitively so "us pacific" vs the device's
	// "us Pacific" is not a change.
	switch {
	case plan.Timezone.IsNull() && !stateTZ.IsNull():
		commands = append(commands, "no clock timezone "+strings.ToLower(stateTZ.ValueString()))
	case !plan.Timezone.IsNull() && (stateTZ.IsNull() || !strings.EqualFold(plan.Timezone.ValueString(), stateTZ.ValueString())):
		commands = append(commands, "clock timezone "+strings.ToLower(plan.Timezone.ValueString()))
	}
	if plan.SummerTime.ValueBool() != stateSummer {
		if plan.SummerTime.ValueBool() {
			commands = append(commands, "clock summer-time")
		} else {
			commands = append(commands, "no clock summer-time")
		}
	}

	var ntp []string
	for _, s := range stateServers {
		if !stringSliceContains(planServers, s) {
			ntp = append(ntp, "no server "+s)
		}
	}
	for _, s := range planServers {
		if !stringSliceContains(stateServers, s) {
			ntp = append(ntp, "server "+s)
		}
	}
	if plan.Serve.ValueBool() != stateServe {
		if plan.Serve.ValueBool() {
			ntp = append(ntp, "no disable serve")
		} else {
			ntp = append(ntp, "disable serve")
		}
	}
	if len(ntp) > 0 {
		commands = append(commands, "ntp")
		commands = append(commands, ntp...)
		commands = append(commands, "exit")
	}
	return commands
}

// apply sends commands (if any) and saves. It reports whether the caller may
// continue.
func (r *NTPResource) apply(commands []string, op string, diags *diag.Diagnostics) bool {
	if len(commands) == 0 {
		return true
	}
	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		diags.AddError(fmt.Sprintf("Failed to %s NTP/clock settings", op), err.Error())
		return false
	}
	if err := r.client.WriteMemory(); err != nil {
		diags.AddError("Failed to save configuration", err.Error())
		return false
	}
	return true
}

func (r *NTPResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}
