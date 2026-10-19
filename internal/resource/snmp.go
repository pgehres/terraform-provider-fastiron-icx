package resource

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
	_ resource.Resource                = &SNMPResource{}
	_ resource.ResourceWithImportState = &SNMPResource{}
)

// reCommunityString is the charset for a community string: printable ASCII
// without space, double or single quote, backslash, or `?` (which the
// FastIron CLI treats as a help request). It is interpolated bare into
// `snmp-server community <string> ...`.
var reCommunityString = regexp.MustCompile(`^[!#-&(->@-\[\]-~]+$`)

// reSNMPACL is the charset for a standard ACL name or number.
var reSNMPACL = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// reSNMPText is the charset for contact/location text: printable characters
// including spaces (FastIron takes the rest of the line), but no control
// characters, double quotes or `?`.
var reSNMPText = regexp.MustCompile(`^[^\x00-\x1f\x7f"?]+$`)

type SNMPResource struct {
	client sshclient.CommandExecutor
}

type SNMPResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Communities types.List   `tfsdk:"communities"`
	Contact     types.String `tfsdk:"contact"`
	Location    types.String `tfsdk:"location"`
}

type SNMPCommunityModel struct {
	String types.String `tfsdk:"string"`
	Mode   types.String `tfsdk:"mode"`
	ACL    types.String `tfsdk:"acl"`
}

var snmpCommunityAttrTypes = map[string]attr.Type{
	"string": types.StringType,
	"mode":   types.StringType,
	"acl":    types.StringType,
}

func NewSNMPResource() resource.Resource {
	return &SNMPResource{}
}

func (r *SNMPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snmp"
}

func (r *SNMPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages SNMP community strings, contact and location on an ICX switch. This is a singleton resource. " +
			"Community strings are write-only: the switch never prints them back, so Read cannot verify them.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"communities": schema.ListNestedAttribute{
				Description: "SNMP v1/v2c community strings (`snmp-server community <string> ro|rw [<acl>]`). " +
					"Write-only: the running config shows them masked or encrypted, so the provider keeps the " +
					"configured values in state and cannot detect drift in them. After import the first plan " +
					"re-adds every community; that apply is idempotent.",
				Optional: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"string": schema.StringAttribute{
							Description: "The community string (1-32 printable characters; no spaces, quotes, backslash or '?').",
							Required:    true,
							Sensitive:   true,
							Validators: []validator.String{
								stringvalidator.LengthBetween(1, 32),
								stringvalidator.RegexMatches(reCommunityString,
									"community string must be printable ASCII without spaces, quotes, backslash or '?'"),
							},
						},
						"mode": schema.StringAttribute{
							Description: "Access mode: \"ro\" (read-only) or \"rw\" (read-write).",
							Required:    true,
							Validators: []validator.String{
								stringvalidator.OneOf("ro", "rw"),
							},
						},
						"acl": schema.StringAttribute{
							Description: "Optional standard ACL name or number restricting which managers may use this community.",
							Optional:    true,
							Validators: []validator.String{
								stringvalidator.LengthBetween(1, 64),
								stringvalidator.RegexMatches(reSNMPACL, "acl must be an ACL name or number (letters, digits, '_', '.', '-')"),
							},
						},
					},
				},
			},
			"contact": schema.StringAttribute{
				Description: "`snmp-server contact` text (up to 255 characters, spaces allowed).",
				Optional:    true,
				Validators:  []validator.String{snmpTextValidator("contact")},
			},
			"location": schema.StringAttribute{
				Description: "`snmp-server location` text (up to 255 characters, spaces allowed).",
				Optional:    true,
				Validators:  []validator.String{snmpTextValidator("location")},
			},
		},
	}
}

func snmpTextValidator(name string) validator.String {
	return stringvalidator.All(
		stringvalidator.LengthBetween(1, 255),
		stringvalidator.RegexMatches(reSNMPText, name+" must not contain control characters, double quotes or '?'"),
	)
}

func (r *SNMPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// communitiesOf decodes the communities list; nil for null/unknown.
func communitiesOf(ctx context.Context, list types.List, diags *diag.Diagnostics) []SNMPCommunityModel {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	var out []SNMPCommunityModel
	diags.Append(list.ElementsAs(ctx, &out, false)...)
	return out
}

func (c SNMPCommunityModel) command() string {
	cmd := fmt.Sprintf("snmp-server community %s %s", c.String.ValueString(), c.Mode.ValueString())
	if !c.ACL.IsNull() && c.ACL.ValueString() != "" {
		cmd += " " + c.ACL.ValueString()
	}
	return cmd
}

func (c SNMPCommunityModel) noCommand() string {
	return fmt.Sprintf("no snmp-server community %s %s", c.String.ValueString(), c.Mode.ValueString())
}

func (c SNMPCommunityModel) equal(o SNMPCommunityModel) bool {
	return c.String.Equal(o.String) && c.Mode.Equal(o.Mode) && c.ACL.Equal(o.ACL)
}

func containsCommunity(list []SNMPCommunityModel, c SNMPCommunityModel) bool {
	for _, x := range list {
		if x.equal(c) {
			return true
		}
	}
	return false
}

// secretsOf collects every community string in the given lists, for
// redaction of diagnostics.
func secretsOf(lists ...[]SNMPCommunityModel) []string {
	var out []string
	for _, l := range lists {
		for _, c := range l {
			if s := c.String.ValueString(); s != "" && !stringSliceContains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// redactSecrets applies redactPassword for every secret.
func redactSecrets(msg string, secrets []string) string {
	for _, s := range secrets {
		msg = redactPassword(msg, s)
	}
	return msg
}

func (r *SNMPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SNMPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	communities := communitiesOf(ctx, plan.Communities, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var commands []string
	for _, c := range communities {
		commands = append(commands, c.command())
	}
	if !plan.Contact.IsNull() {
		commands = append(commands, "snmp-server contact "+plan.Contact.ValueString())
	}
	if !plan.Location.IsNull() {
		commands = append(commands, "snmp-server location "+plan.Location.ValueString())
	}

	if !r.apply(commands, "configure", secretsOf(communities), &resp.Diagnostics) {
		return
	}
	plan.ID = types.StringValue("snmp")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SNMPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SNMPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	if config.SNMP.Contact != "" {
		state.Contact = types.StringValue(config.SNMP.Contact)
	} else {
		state.Contact = types.StringNull()
	}
	if config.SNMP.Location != "" {
		state.Location = types.StringValue(config.SNMP.Location)
	} else {
		state.Location = types.StringNull()
	}

	// Communities are write-only (printed masked or encrypted): keep state.
	// The one thing the device does tell us is that none exist at all — then
	// state's communities are gone and the next plan re-adds them.
	if len(config.SNMP.Communities) == 0 && !state.Communities.IsNull() {
		state.Communities = types.ListNull(types.ObjectType{AttrTypes: snmpCommunityAttrTypes})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SNMPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SNMPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	planComms := communitiesOf(ctx, plan.Communities, &resp.Diagnostics)
	stateComms := communitiesOf(ctx, state.Communities, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	var commands []string
	for _, c := range stateComms {
		if !containsCommunity(planComms, c) {
			commands = append(commands, c.noCommand())
		}
	}
	for _, c := range planComms {
		if !containsCommunity(stateComms, c) {
			commands = append(commands, c.command())
		}
	}
	if !plan.Contact.Equal(state.Contact) {
		if plan.Contact.IsNull() {
			commands = append(commands, "no snmp-server contact")
		} else {
			commands = append(commands, "snmp-server contact "+plan.Contact.ValueString())
		}
	}
	if !plan.Location.Equal(state.Location) {
		if plan.Location.IsNull() {
			commands = append(commands, "no snmp-server location")
		} else {
			commands = append(commands, "snmp-server location "+plan.Location.ValueString())
		}
	}

	if !r.apply(commands, "update", secretsOf(planComms, stateComms), &resp.Diagnostics) {
		return
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SNMPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SNMPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	comms := communitiesOf(ctx, state.Communities, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var commands []string
	for _, c := range comms {
		commands = append(commands, c.noCommand())
	}
	if !state.Contact.IsNull() {
		commands = append(commands, "no snmp-server contact")
	}
	if !state.Location.IsNull() {
		commands = append(commands, "no snmp-server location")
	}
	r.apply(commands, "remove", secretsOf(comms), &resp.Diagnostics)
}

func (r *SNMPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), "snmp")...)
}

// apply sends commands (if any) and saves, redacting every community string
// from any error text.
func (r *SNMPResource) apply(commands []string, op string, secrets []string, diags *diag.Diagnostics) bool {
	if len(commands) == 0 {
		return true
	}
	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		diags.AddError(fmt.Sprintf("Failed to %s SNMP settings", op), redactSecrets(err.Error(), secrets))
		return false
	}
	if err := r.client.WriteMemory(); err != nil {
		diags.AddError("Failed to save configuration", redactSecrets(err.Error(), secrets))
		return false
	}
	return true
}

func (r *SNMPResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}
