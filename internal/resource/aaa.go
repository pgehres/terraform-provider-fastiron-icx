package resource

import (
	"context"
	"fmt"

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
	_ resource.Resource                = &AAAResource{}
	_ resource.ResourceWithImportState = &AAAResource{}
)

type AAAResource struct {
	client sshclient.CommandExecutor
}

type AAAResourceModel struct {
	ID               types.String `tfsdk:"id"`
	WebServerAuth    types.String `tfsdk:"web_server_auth"`
	LoginAuth        types.String `tfsdk:"login_auth"`
	EnableAAAConsole types.Bool   `tfsdk:"enable_aaa_console"`
}

func NewAAAResource() resource.Resource {
	return &AAAResource{}
}

func (r *AAAResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_aaa"
}

func (r *AAAResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages AAA authentication settings on an ICX switch. This is a singleton resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"web_server_auth": schema.StringAttribute{
				Description: "AAA authentication method for web server (e.g., \"default local\").",
				Optional:    true,
			},
			"login_auth": schema.StringAttribute{
				Description: "AAA authentication method for login (e.g., \"default local\").",
				Optional:    true,
			},
			"enable_aaa_console": schema.BoolAttribute{
				Description: "Enable AAA authentication for console access.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
		},
	}
}

func (r *AAAResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AAAResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AAAResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := r.buildCommands(&plan)

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to configure AAA", err.Error())
			return
		}

		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}

	plan.ID = types.StringValue("aaa")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AAAResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AAAResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	if config.AAA.WebServerAuth != "" {
		state.WebServerAuth = types.StringValue(config.AAA.WebServerAuth)
	} else {
		state.WebServerAuth = types.StringNull()
	}
	if config.AAA.LoginAuth != "" {
		state.LoginAuth = types.StringValue(config.AAA.LoginAuth)
	} else {
		state.LoginAuth = types.StringNull()
	}
	state.EnableAAAConsole = types.BoolValue(config.AAA.EnableAAAConsole)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AAAResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AAAResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var commands []string

	// Handle removals.
	if !state.WebServerAuth.IsNull() && plan.WebServerAuth.IsNull() {
		commands = append(commands, fmt.Sprintf("no aaa authentication web-server %s", state.WebServerAuth.ValueString()))
	}
	if !state.LoginAuth.IsNull() && plan.LoginAuth.IsNull() {
		commands = append(commands, fmt.Sprintf("no aaa authentication login %s", state.LoginAuth.ValueString()))
	}
	if state.EnableAAAConsole.ValueBool() && !plan.EnableAAAConsole.ValueBool() {
		commands = append(commands, "no enable aaa console")
	}

	// Handle additions/changes.
	if !plan.WebServerAuth.IsNull() && !plan.WebServerAuth.Equal(state.WebServerAuth) {
		commands = append(commands, fmt.Sprintf("aaa authentication web-server %s", plan.WebServerAuth.ValueString()))
	}
	if !plan.LoginAuth.IsNull() && !plan.LoginAuth.Equal(state.LoginAuth) {
		commands = append(commands, fmt.Sprintf("aaa authentication login %s", plan.LoginAuth.ValueString()))
	}
	if plan.EnableAAAConsole.ValueBool() && !state.EnableAAAConsole.ValueBool() {
		commands = append(commands, "enable aaa console")
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to update AAA", err.Error())
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

func (r *AAAResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), "aaa")...)
}

func (r *AAAResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AAAResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var commands []string
	if !state.WebServerAuth.IsNull() {
		commands = append(commands, fmt.Sprintf("no aaa authentication web-server %s", state.WebServerAuth.ValueString()))
	}
	if !state.LoginAuth.IsNull() {
		commands = append(commands, fmt.Sprintf("no aaa authentication login %s", state.LoginAuth.ValueString()))
	}
	if state.EnableAAAConsole.ValueBool() {
		commands = append(commands, "no enable aaa console")
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to remove AAA config", err.Error())
			return
		}
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}
}

func (r *AAAResource) buildCommands(plan *AAAResourceModel) []string {
	var commands []string
	if !plan.WebServerAuth.IsNull() {
		commands = append(commands, fmt.Sprintf("aaa authentication web-server %s", plan.WebServerAuth.ValueString()))
	}
	if !plan.LoginAuth.IsNull() {
		commands = append(commands, fmt.Sprintf("aaa authentication login %s", plan.LoginAuth.ValueString()))
	}
	if plan.EnableAAAConsole.ValueBool() {
		commands = append(commands, "enable aaa console")
	}
	return commands
}

func (r *AAAResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}
