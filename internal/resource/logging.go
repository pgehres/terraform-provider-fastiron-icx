package resource

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &LoggingResource{}
	_ resource.ResourceWithImportState = &LoggingResource{}
)

// loggingLevels are the syslog severities `logging buffered <level>` accepts.
var loggingLevels = []string{
	"alerts", "critical", "debugging", "emergencies", "errors", "informational", "notifications", "warnings",
}

// loggingFacilities are the names `logging facility <name>` accepts.
var loggingFacilities = []string{
	"auth", "cron", "daemon", "kern", "local0", "local1", "local2", "local3", "local4", "local5", "local6", "local7",
	"lpr", "mail", "news", "sys9", "sys10", "sys11", "sys12", "sys13", "sys14", "syslog", "user", "uucp",
}

type LoggingResource struct {
	client sshclient.CommandExecutor
}

type LoggingResourceModel struct {
	ID              types.String `tfsdk:"id"`
	Hosts           types.List   `tfsdk:"hosts"`
	BufferedEntries types.Int64  `tfsdk:"buffered_entries"`
	DisabledLevels  types.Set    `tfsdk:"disabled_levels"`
	Facility        types.String `tfsdk:"facility"`
}

func NewLoggingResource() resource.Resource {
	return &LoggingResource{}
}

func (r *LoggingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_logging"
}

func (r *LoggingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages syslog settings on an ICX switch: syslog hosts, the local buffer size, " +
			"per-level buffered logging and the syslog facility. This is a singleton resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"hosts": schema.ListAttribute{
				Description: "Syslog servers (`logging host <ipv4>`), in order. Omitted or empty means none.",
				Optional:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					listvalidator.ValueStringsAre(ipv4Validator{}),
					listvalidator.UniqueValues(),
				},
			},
			"buffered_entries": schema.Int64Attribute{
				Description: "Size of the local syslog buffer (`logging buffered <n>`, 1-1000). Omitted means the device " +
					"default of 50. FastIron applies a buffer-size change only after `write memory` and a reload.",
				Optional: true,
				Validators: []validator.Int64{
					int64validator.Between(1, 1000),
				},
			},
			"disabled_levels": schema.SetAttribute{
				Description: "Syslog severities whose buffered logging is turned off (`no logging buffered <level>`): " +
					"alerts, critical, debugging, emergencies, errors, informational, notifications, warnings. " +
					"Omitted or empty means all levels stay at the device default.",
				Optional:    true,
				ElementType: types.StringType,
				Validators: []validator.Set{
					setvalidator.ValueStringsAre(stringvalidator.OneOf(loggingLevels...)),
				},
			},
			"facility": schema.StringAttribute{
				Description: "Syslog facility for messages sent to the hosts (`logging facility <name>`), e.g. \"local7\". " +
					"Omitted means the device default (user).",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf(loggingFacilities...),
				},
			},
		},
	}
}

func (r *LoggingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// setToStringSlice converts a Terraform Set of String to a sorted Go slice.
func setToStringSlice(ctx context.Context, set types.Set, diags *diag.Diagnostics) []string {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(set.ElementsAs(ctx, &out, false)...)
	sort.Strings(out)
	return out
}

func (r *LoggingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LoggingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	commands := r.buildCommands(ctx, &plan, nil, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.apply(commands, "configure", &resp.Diagnostics) {
		return
	}
	plan.ID = types.StringValue("logging")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *LoggingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LoggingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}
	l := config.Logging

	if len(l.Hosts) > 0 {
		list, d := types.ListValueFrom(ctx, types.StringType, l.Hosts)
		resp.Diagnostics.Append(d...)
		state.Hosts = list
	} else {
		state.Hosts = types.ListNull(types.StringType)
	}
	if l.BufferedEntries > 0 {
		state.BufferedEntries = types.Int64Value(int64(l.BufferedEntries))
	} else {
		state.BufferedEntries = types.Int64Null()
	}
	if len(l.BufferedDisabledLevels) > 0 {
		set, d := types.SetValueFrom(ctx, types.StringType, l.BufferedDisabledLevels)
		resp.Diagnostics.Append(d...)
		state.DisabledLevels = set
	} else {
		state.DisabledLevels = types.SetNull(types.StringType)
	}
	if l.Facility != "" {
		state.Facility = types.StringValue(l.Facility)
	} else {
		state.Facility = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *LoggingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state LoggingResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
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

func (r *LoggingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state LoggingResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Back to device defaults: no hosts, 50-entry buffer, all levels on,
	// facility user.
	defaults := &LoggingResourceModel{
		Hosts:           types.ListNull(types.StringType),
		BufferedEntries: types.Int64Null(),
		DisabledLevels:  types.SetNull(types.StringType),
		Facility:        types.StringNull(),
	}
	commands := r.buildCommands(ctx, defaults, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	r.apply(commands, "reset", &resp.Diagnostics)
}

func (r *LoggingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), "logging")...)
}

// buildCommands renders the commands from state (nil = device defaults) to
// plan: removals first, then additions, per attribute.
func (r *LoggingResource) buildCommands(ctx context.Context, plan, state *LoggingResourceModel, diags *diag.Diagnostics) []string {
	var stateHosts, stateLevels []string
	stateEntries := types.Int64Null()
	stateFacility := types.StringNull()
	if state != nil {
		stateHosts = listToStringSlice(ctx, state.Hosts, diags)
		stateLevels = setToStringSlice(ctx, state.DisabledLevels, diags)
		stateEntries = state.BufferedEntries
		stateFacility = state.Facility
	}
	planHosts := listToStringSlice(ctx, plan.Hosts, diags)
	planLevels := setToStringSlice(ctx, plan.DisabledLevels, diags)
	if diags.HasError() {
		return nil
	}

	var commands []string
	for _, h := range stateHosts {
		if !stringSliceContains(planHosts, h) {
			commands = append(commands, "no logging host "+h)
		}
	}
	for _, h := range planHosts {
		if !stringSliceContains(stateHosts, h) {
			commands = append(commands, "logging host "+h)
		}
	}

	if !plan.BufferedEntries.Equal(stateEntries) {
		if plan.BufferedEntries.IsNull() {
			commands = append(commands, fmt.Sprintf("no logging buffered %d", stateEntries.ValueInt64()))
		} else {
			commands = append(commands, fmt.Sprintf("logging buffered %d", plan.BufferedEntries.ValueInt64()))
		}
	}

	for _, l := range stateLevels {
		if !stringSliceContains(planLevels, l) {
			commands = append(commands, "logging buffered "+l)
		}
	}
	for _, l := range planLevels {
		if !stringSliceContains(stateLevels, l) {
			commands = append(commands, "no logging buffered "+l)
		}
	}

	if !plan.Facility.Equal(stateFacility) {
		if plan.Facility.IsNull() {
			commands = append(commands, "no logging facility")
		} else {
			commands = append(commands, "logging facility "+plan.Facility.ValueString())
		}
	}
	return commands
}

func (r *LoggingResource) apply(commands []string, op string, diags *diag.Diagnostics) bool {
	if len(commands) == 0 {
		return true
	}
	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		diags.AddError(fmt.Sprintf("Failed to %s logging settings", op), err.Error())
		return false
	}
	if err := r.client.WriteMemory(); err != nil {
		diags.AddError("Failed to save configuration", err.Error())
		return false
	}
	return true
}

func (r *LoggingResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}
