package datasource

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var _ datasource.DataSource = &RunningConfigDataSource{}

type RunningConfigDataSource struct {
	client sshclient.CommandExecutor
}

type RunningConfigDataSourceModel struct {
	ID     types.String `tfsdk:"id"`
	Config types.String `tfsdk:"config"`
}

func NewRunningConfigDataSource() datasource.DataSource {
	return &RunningConfigDataSource{}
}

func (d *RunningConfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_running_config"
}

func (d *RunningConfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the full running configuration from an ICX switch.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
			},
			"config": schema.StringAttribute{
				Description: "The full running configuration text.",
				Computed:    true,
			},
		},
	}
}

func (d *RunningConfigDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *RunningConfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	output, err := d.client.GetRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	state := RunningConfigDataSourceModel{
		ID:     types.StringValue("running-config"),
		Config: types.StringValue(output),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
