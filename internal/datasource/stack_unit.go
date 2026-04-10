package datasource

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var _ datasource.DataSource = &StackUnitDataSource{}

type StackUnitDataSource struct {
	client sshclient.CommandExecutor
}

type StackUnitDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	UnitID     types.Int64  `tfsdk:"unit_id"`
	Modules    types.List   `tfsdk:"modules"`
	StackPorts types.List   `tfsdk:"stack_ports"`
}

var moduleObjectType = types.ObjectType{
	AttrTypes: map[string]attr.Type{
		"id":   types.Int64Type,
		"type": types.StringType,
	},
}

func NewStackUnitDataSource() datasource.DataSource {
	return &StackUnitDataSource{}
}

func (d *StackUnitDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stack_unit"
}

func (d *StackUnitDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads stack unit and module information from an ICX switch.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
			},
			"unit_id": schema.Int64Attribute{
				Description: "Stack unit ID to read. Defaults to 1.",
				Optional:    true,
			},
			"modules": schema.ListAttribute{
				Description: "List of modules in this stack unit.",
				Computed:    true,
				ElementType: moduleObjectType,
			},
			"stack_ports": schema.ListAttribute{
				Description: "List of stack port identifiers.",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (d *StackUnitDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerdata.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *providerdata.ProviderData, got %T", req.ProviderData))
		return
	}
	d.client = data.Client
}

func (d *StackUnitDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state StackUnitDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	unitID := 1
	if !state.UnitID.IsNull() {
		unitID = int(state.UnitID.ValueInt64())
	}

	output, err := d.client.GetRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	config, err := parser.ParseRunningConfig(output)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse running config", err.Error())
		return
	}

	var found *parser.StackUnit
	for i := range config.StackUnits {
		if config.StackUnits[i].ID == unitID {
			found = &config.StackUnits[i]
			break
		}
	}

	if found == nil {
		resp.Diagnostics.AddError("Stack unit not found",
			fmt.Sprintf("Stack unit %d not found in running config", unitID))
		return
	}

	state.ID = types.StringValue(strconv.Itoa(unitID))
	state.UnitID = types.Int64Value(int64(unitID))

	// Build modules list.
	moduleValues := make([]attr.Value, len(found.Modules))
	for i, m := range found.Modules {
		obj, diags := types.ObjectValue(
			moduleObjectType.AttrTypes,
			map[string]attr.Value{
				"id":   types.Int64Value(int64(m.ID)),
				"type": types.StringValue(m.Type),
			},
		)
		resp.Diagnostics.Append(diags...)
		moduleValues[i] = obj
	}
	modulesList, diags := types.ListValue(moduleObjectType, moduleValues)
	resp.Diagnostics.Append(diags...)
	state.Modules = modulesList

	// Build stack ports list.
	portValues := make([]attr.Value, len(found.StackPorts))
	for i, p := range found.StackPorts {
		portValues[i] = types.StringValue(p)
	}
	portsList, diags := types.ListValue(types.StringType, portValues)
	resp.Diagnostics.Append(diags...)
	state.StackPorts = portsList

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
