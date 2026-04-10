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
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &PoEResource{}
	_ resource.ResourceWithImportState = &PoEResource{}
)

type PoEResource struct {
	client sshclient.CommandExecutor
}

type PoEResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Port       types.String `tfsdk:"port"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	PowerLimit types.Int64  `tfsdk:"power_limit"`
}

func NewPoEResource() resource.Resource {
	return &PoEResource{}
}

func (r *PoEResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_poe"
}

func (r *PoEResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages Power over Ethernet (PoE) settings on an ICX switch port.",
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
				Description: "Power limit in milliwatts for this port.",
				Optional:    true,
			},
		},
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

	// PoE status is read via "show inline power" — parse its output.
	// For now, preserve state since parsing show inline power is complex.
	// TODO: parse "show inline power <port>" output for accurate reads.
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *PoEResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state PoEResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	port := plan.Port.ValueString()
	var commands []string

	if plan.Enabled.ValueBool() != state.Enabled.ValueBool() {
		if plan.Enabled.ValueBool() {
			commands = append(commands,
				fmt.Sprintf("interface ethernet %s", port),
				"inline power",
				"exit",
			)
		} else {
			commands = append(commands,
				fmt.Sprintf("interface ethernet %s", port),
				"no inline power",
				"exit",
			)
		}
	}

	if !plan.PowerLimit.Equal(state.PowerLimit) {
		if plan.PowerLimit.IsNull() && !state.PowerLimit.IsNull() {
			commands = append(commands,
				fmt.Sprintf("interface ethernet %s", port),
				"no inline power power-limit",
				"exit",
			)
		} else if !plan.PowerLimit.IsNull() {
			commands = append(commands,
				fmt.Sprintf("interface ethernet %s", port),
				fmt.Sprintf("inline power power-limit %d", plan.PowerLimit.ValueInt64()),
				"exit",
			)
		}
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
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

	port := state.Port.ValueString()
	commands := []string{
		fmt.Sprintf("interface ethernet %s", port),
	}

	if !state.PowerLimit.IsNull() {
		commands = append(commands, "no inline power power-limit")
	}

	// Reset to default (enabled).
	if !state.Enabled.ValueBool() {
		commands = append(commands, "inline power")
	}

	commands = append(commands, "exit")

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

func (r *PoEResource) buildCommands(plan *PoEResourceModel) []string {
	port := plan.Port.ValueString()
	commands := []string{fmt.Sprintf("interface ethernet %s", port)}

	if plan.Enabled.ValueBool() {
		commands = append(commands, "inline power")
	} else {
		commands = append(commands, "no inline power")
	}

	if !plan.PowerLimit.IsNull() {
		commands = append(commands, fmt.Sprintf("inline power power-limit %d", plan.PowerLimit.ValueInt64()))
	}

	commands = append(commands, "exit")
	return commands
}
