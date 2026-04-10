package resource

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var _ resource.Resource = &RawConfigResource{}

type RawConfigResource struct {
	client sshclient.CommandExecutor
}

type RawConfigResourceModel struct {
	ID              types.String `tfsdk:"id"`
	Commands        types.List   `tfsdk:"commands"`
	DestroyCommands types.List   `tfsdk:"destroy_commands"`
	ExpectInConfig  types.List   `tfsdk:"expect_in_config"`
}

func NewRawConfigResource() resource.Resource {
	return &RawConfigResource{}
}

func (r *RawConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_raw_config"
}

func (r *RawConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages arbitrary CLI configuration lines on an ICX switch. Use this for features not covered by specific resources.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"commands": schema.ListAttribute{
				Description: "CLI commands to execute in config mode on create/update.",
				Required:    true,
				ElementType: types.StringType,
			},
			"destroy_commands": schema.ListAttribute{
				Description: "CLI commands to execute in config mode on destroy. If not specified, each command from 'commands' is prefixed with 'no'.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"expect_in_config": schema.ListAttribute{
				Description: "Lines expected to appear in the running config. If any are missing, Terraform detects drift and will re-apply.",
				Optional:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (r *RawConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *RawConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RawConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := listToStringSlice(ctx, plan.Commands, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to apply raw config", err.Error())
			return
		}
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}

	// Generate a deterministic ID from the commands.
	plan.ID = types.StringValue(hashCommands(commands))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RawConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RawConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	expectLines := listToStringSlice(ctx, state.ExpectInConfig, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// If no expect_in_config is set, we can't verify drift — just trust state.
	if len(expectLines) == 0 {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	// Check running config for expected lines.
	runningConfig, err := r.client.GetRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	for _, expected := range expectLines {
		if !strings.Contains(runningConfig, strings.TrimSpace(expected)) {
			// Expected line is missing — remove from state to trigger re-apply.
			resp.State.RemoveResource(ctx)
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RawConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RawConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Remove old commands first.
	oldCommands := listToStringSlice(ctx, state.Commands, &resp.Diagnostics)
	oldDestroy := listToStringSlice(ctx, state.DestroyCommands, &resp.Diagnostics)

	var removeCommands []string
	if len(oldDestroy) > 0 {
		removeCommands = oldDestroy
	} else {
		for _, cmd := range oldCommands {
			removeCommands = append(removeCommands, "no "+cmd)
		}
	}

	if len(removeCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(removeCommands); err != nil {
			resp.Diagnostics.AddError("Failed to remove old raw config", err.Error())
			return
		}
	}

	// Apply new commands.
	newCommands := listToStringSlice(ctx, plan.Commands, &resp.Diagnostics)
	if len(newCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(newCommands); err != nil {
			resp.Diagnostics.AddError("Failed to apply raw config", err.Error())
			return
		}
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}

	plan.ID = types.StringValue(hashCommands(newCommands))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RawConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RawConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	destroyCommands := listToStringSlice(ctx, state.DestroyCommands, &resp.Diagnostics)
	if len(destroyCommands) == 0 {
		// Auto-generate destroy commands by prefixing with "no".
		commands := listToStringSlice(ctx, state.Commands, &resp.Diagnostics)
		for _, cmd := range commands {
			destroyCommands = append(destroyCommands, "no "+cmd)
		}
	}

	if len(destroyCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(destroyCommands); err != nil {
			resp.Diagnostics.AddError("Failed to remove raw config", err.Error())
			return
		}
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}
}

// hashCommands generates a simple deterministic ID from command list.
func hashCommands(commands []string) string {
	if len(commands) == 0 {
		return "raw"
	}
	// Use first command as a readable prefix, truncated.
	first := strings.ReplaceAll(commands[0], " ", "-")
	if len(first) > 40 {
		first = first[:40]
	}
	return fmt.Sprintf("raw-%s", first)
}
