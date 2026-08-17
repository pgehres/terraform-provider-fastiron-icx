package resource

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.Resource                = &UserResource{}
	_ resource.ResourceWithImportState = &UserResource{}
)

// reUsernameValid rejects whitespace, control chars, and quote characters.
// FastIron interpolates the username bare into CLI commands, so these chars
// would corrupt the command or the running-config parser.
var reUsernameValid = regexp.MustCompile(`^[^\s"']+$`)

// rePasswordNoNewlines rejects carriage returns and newlines. Spaces and
// other printable characters are allowed in passwords.
var rePasswordNoNewlines = regexp.MustCompile(`^[^\r\n]+$`)

type UserResource struct {
	client           sshclient.CommandExecutor
	providerUsername string // the account the provider is authenticated as
}

type UserResourceModel struct {
	ID       types.String `tfsdk:"id"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
}

func NewUserResource() resource.Resource {
	return &UserResource{}
}

func (r *UserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *UserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a local user account on an ICX switch.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"username": schema.StringAttribute{
				Description: "The username for the local account (1-48 chars, no whitespace or quote characters).",
				Required:    true,
				Validators: []validator.String{
					// Whitespace and quotes would corrupt the bare CLI command
					// `username <name> password <pass>` or the running-config parser.
					stringvalidator.RegexMatches(
						reUsernameValid,
						"username must not contain whitespace or quote characters",
					),
					stringvalidator.LengthBetween(1, 48),
				},
			},
			"password": schema.StringAttribute{
				Description: "The password for the user. This value cannot be read back from the switch.",
				Required:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					// Newlines would inject additional CLI commands into the session.
					stringvalidator.RegexMatches(
						rePasswordNoNewlines,
						"password must not contain carriage returns or newlines",
					),
				},
			},
		},
	}
}

func (r *UserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerdata.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *providerdata.ProviderData, got %T", req.ProviderData))
		return
	}
	r.client = data.Client
	r.providerUsername = data.Username
}

func (r *UserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan UserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	password := plan.Password.ValueString()
	commands := []string{
		fmt.Sprintf("username %s password %s", plan.Username.ValueString(), password),
	}

	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		resp.Diagnostics.AddError("Failed to create user", redactPassword(err.Error(), password))
		return
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", redactPassword(err.Error(), password))
		return
	}

	plan.ID = types.StringValue(plan.Username.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state UserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	username := state.Username.ValueString()
	var found bool
	for _, u := range config.Users {
		if u.Username == username {
			found = true
			break
		}
	}

	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	// Password cannot be read back — keep the value from state.
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *UserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state UserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	password := plan.Password.ValueString()
	var commands []string

	// If username changed, remove old and create new.
	if plan.Username.ValueString() != state.Username.ValueString() {
		commands = append(commands, fmt.Sprintf("no username %s", state.Username.ValueString()))
	}

	commands = append(commands,
		fmt.Sprintf("username %s password %s", plan.Username.ValueString(), password),
	)

	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		resp.Diagnostics.AddError("Failed to update user", redactPassword(err.Error(), password))
		return
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", redactPassword(err.Error(), password))
		return
	}

	plan.ID = types.StringValue(plan.Username.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state UserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Refuse to delete the account the provider session is authenticated as.
	// Doing so would remove the SSH credentials mid-apply, severing the
	// connection before the session can complete.
	if r.providerUsername != "" && state.Username.ValueString() == r.providerUsername {
		resp.Diagnostics.AddError(
			"Cannot delete the authenticated user",
			fmt.Sprintf(
				"Deleting user %q would remove the account the provider is currently authenticated as, "+
					"severing the SSH session mid-apply. Remove this resource from your configuration "+
					"or use a different provider account to manage it.",
				state.Username.ValueString(),
			),
		)
		return
	}

	commands := []string{
		fmt.Sprintf("no username %s", state.Username.ValueString()),
	}

	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		resp.Diagnostics.AddError("Failed to delete user", err.Error())
		return
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}
}

func (r *UserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("username"), req.ID)...)
}

func (r *UserResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}

// redactPassword replaces plaintext password occurrences in an error string.
// The SSH client wraps failed commands as fmt.Errorf("command %q: %s", cmd, errMsg),
// so the password embedded in the username command can appear in error output.
func redactPassword(errMsg, password string) string {
	if password == "" {
		return errMsg
	}
	errMsg = strings.ReplaceAll(errMsg, password, "(redacted)")
	// %q backslash-escapes quotes and non-printables, so also redact the
	// escaped form when it differs from the raw password.
	if escaped := strconv.Quote(password); escaped[1:len(escaped)-1] != password {
		errMsg = strings.ReplaceAll(errMsg, escaped[1:len(escaped)-1], "(redacted)")
	}
	return errMsg
}
