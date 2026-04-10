package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	icxdatasource "github.com/pgehres/terraform-provider-fastiron-icx/internal/datasource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	icxresource "github.com/pgehres/terraform-provider-fastiron-icx/internal/resource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var _ provider.Provider = &FastIronICXProvider{}

type FastIronICXProvider struct {
	version string
}

type FastIronICXProviderModel struct {
	Host           types.String `tfsdk:"host"`
	Port           types.Int64  `tfsdk:"port"`
	Username       types.String `tfsdk:"username"`
	Password       types.String `tfsdk:"password"`
	EnablePassword types.String `tfsdk:"enable_password"`
	Timeout        types.Int64  `tfsdk:"timeout"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &FastIronICXProvider{
			version: version,
		}
	}
}

func (p *FastIronICXProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "icx"
	resp.Version = p.version
}

func (p *FastIronICXProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Terraform provider for Brocade/Ruckus ICX switches running FastIron firmware. Communicates via SSH CLI.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Description: "Hostname or IP address of the ICX switch. Can also be set with the FASTIRON_HOST environment variable.",
				Optional:    true,
			},
			"port": schema.Int64Attribute{
				Description: "SSH port. Defaults to 22. Can also be set with the FASTIRON_PORT environment variable.",
				Optional:    true,
			},
			"username": schema.StringAttribute{
				Description: "SSH username. Can also be set with the FASTIRON_USERNAME environment variable.",
				Optional:    true,
			},
			"password": schema.StringAttribute{
				Description: "SSH password. Can also be set with the FASTIRON_PASSWORD environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
			"enable_password": schema.StringAttribute{
				Description: "Enable mode password. Required if the switch requires enable authentication. Can also be set with the FASTIRON_ENABLE_PASSWORD environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
			"timeout": schema.Int64Attribute{
				Description: "SSH connection timeout in seconds. Defaults to 30.",
				Optional:    true,
			},
		},
	}
}

func (p *FastIronICXProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config FastIronICXProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	host := stringValueOrEnv(config.Host, "FASTIRON_HOST")
	username := stringValueOrEnv(config.Username, "FASTIRON_USERNAME")
	password := stringValueOrEnv(config.Password, "FASTIRON_PASSWORD")
	enablePassword := stringValueOrEnv(config.EnablePassword, "FASTIRON_ENABLE_PASSWORD")

	port := 22
	if !config.Port.IsNull() && !config.Port.IsUnknown() {
		port = int(config.Port.ValueInt64())
	} else if envPort := os.Getenv("FASTIRON_PORT"); envPort != "" {
		var p int
		if _, err := fmt.Sscanf(envPort, "%d", &p); err == nil && p > 0 {
			port = p
		}
	}

	timeout := 30
	if !config.Timeout.IsNull() && !config.Timeout.IsUnknown() {
		timeout = int(config.Timeout.ValueInt64())
	}

	if host == "" {
		resp.Diagnostics.AddError("Missing host", "The host must be set in the provider configuration or the FASTIRON_HOST environment variable.")
		return
	}
	if username == "" {
		resp.Diagnostics.AddError("Missing username", "The username must be set in the provider configuration or the FASTIRON_USERNAME environment variable.")
		return
	}
	if password == "" {
		resp.Diagnostics.AddError("Missing password", "The password must be set in the provider configuration or the FASTIRON_PASSWORD environment variable.")
		return
	}

	client, err := sshclient.NewClient(sshclient.Options{
		Host:           host,
		Port:           port,
		Username:       username,
		Password:       password,
		EnablePassword: enablePassword,
		TimeoutSeconds: timeout,
	})
	if err != nil {
		resp.Diagnostics.AddError("SSH connection failed", "Unable to connect to the ICX switch: "+err.Error())
		return
	}

	data := &providerdata.ProviderData{
		Client: client,
	}

	resp.DataSourceData = data
	resp.ResourceData = data
}

func (p *FastIronICXProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		icxresource.NewVLANResource,
		icxresource.NewInterfaceEthernetResource,
		icxresource.NewInterfaceVEResource,
		icxresource.NewUserResource,
		icxresource.NewAAAResource,
		icxresource.NewSystemResource,
		icxresource.NewPoEResource,
		icxresource.NewRawConfigResource,
	}
}

func (p *FastIronICXProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		icxdatasource.NewRunningConfigDataSource,
		icxdatasource.NewStackUnitDataSource,
	}
}

func stringValueOrEnv(val types.String, envVar string) string {
	if !val.IsNull() && !val.IsUnknown() {
		return val.ValueString()
	}
	return os.Getenv(envVar)
}
