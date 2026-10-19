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
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	icxresource "github.com/pgehres/terraform-provider-fastiron-icx/internal/resource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var _ provider.Provider = &FastIronICXProvider{}

type FastIronICXProvider struct {
	version string
}

type FastIronICXProviderModel struct {
	Host                      types.String `tfsdk:"host"`
	Port                      types.Int64  `tfsdk:"port"`
	Username                  types.String `tfsdk:"username"`
	Password                  types.String `tfsdk:"password"`
	EnablePassword            types.String `tfsdk:"enable_password"`
	Timeout                   types.Int64  `tfsdk:"timeout"`
	HostKey                   types.String `tfsdk:"host_key"`
	InsecureSkipHostKeyVerify types.Bool   `tfsdk:"insecure_skip_host_key_verify"`
	LegacySSHAlgorithms       types.Bool   `tfsdk:"legacy_ssh_algorithms"`
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
			"host_key": schema.StringAttribute{
				Description: "The switch's SSH host public key in known_hosts format " +
					"(e.g. \"ssh-rsa AAAA...\"), or its SHA256 fingerprint " +
					"(e.g. \"SHA256:...\"). Obtain with: ssh-keyscan <host> — or, on older FastIron whose SSH stack " +
					"ssh-keyscan cannot negotiate with, the provider repo's debug tool (go run ./cmd/debug-ssh -print-host-key). " +
					"Can also be set with the FASTIRON_HOST_KEY environment variable.",
				Optional: true,
			},
			"insecure_skip_host_key_verify": schema.BoolAttribute{
				Description: "If true, skip SSH host key verification. " +
					"WARNING: this permits man-in-the-middle interception and " +
					"should only be used on isolated lab networks where MITM is not a concern.",
				Optional: true,
			},
			"legacy_ssh_algorithms": schema.BoolAttribute{
				Description: "If true, additionally offer legacy SSH algorithms " +
					"(diffie-hellman-group14-sha1/group1-sha1/group-exchange-sha1 key exchange, " +
					"aes128-cbc and 3des-cbc ciphers, ssh-rsa and ssh-dss host key signatures) " +
					"needed by very old FastIron SSH stacks, e.g. FastIron 08.0.30 on the EOL ICX 6430. " +
					"Modern algorithms remain preferred, so switches that support them are unaffected. " +
					"WARNING: these algorithms are cryptographically weak; enable only for EOL devices " +
					"on trusted networks. Defaults to false.",
				Optional: true,
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
	hostKey := stringValueOrEnv(config.HostKey, "FASTIRON_HOST_KEY")
	insecureSkip := !config.InsecureSkipHostKeyVerify.IsNull() &&
		!config.InsecureSkipHostKeyVerify.IsUnknown() &&
		config.InsecureSkipHostKeyVerify.ValueBool()
	legacySSH := !config.LegacySSHAlgorithms.IsNull() &&
		!config.LegacySSHAlgorithms.IsUnknown() &&
		config.LegacySSHAlgorithms.ValueBool()

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
	if hostKey == "" && !insecureSkip {
		resp.Diagnostics.AddError(
			"Missing SSH host key",
			fmt.Sprintf(
				"Set host_key to the switch's SSH public key or SHA256 fingerprint, "+
					"or set insecure_skip_host_key_verify = true for trusted lab networks only. "+
					"Obtain the host key with: ssh-keyscan %s (if that prints nothing — common on older "+
					"FastIron SSH stacks — use the provider repo's debug tool: go run ./cmd/debug-ssh -print-host-key)",
				host,
			),
		)
		return
	}

	client, err := sshclient.NewClient(sshclient.Options{
		Host:                      host,
		Port:                      port,
		Username:                  username,
		Password:                  password,
		EnablePassword:            enablePassword,
		TimeoutSeconds:            timeout,
		HostKey:                   hostKey,
		InsecureSkipHostKeyVerify: insecureSkip,
		LegacySSHAlgorithms:       legacySSH,
	})
	if err != nil {
		resp.Diagnostics.AddError("SSH connection failed", "Unable to connect to the ICX switch: "+err.Error())
		return
	}

	data := &providerdata.ProviderData{
		Client:   client,
		Username: username,
		Dialect:  dialect.Detect("", parser.ImageTypeUnknown),
	}

	// Detect the firmware dialect (release + image type) once per provider
	// instance so resources can refuse unsupported operations before any
	// commands are sent and render dialect-specific syntax.
	output, err := client.GetRunningConfig()
	if err != nil {
		// The client is already connected; close it so the switch's SSH
		// session is not leaked until process exit.
		_ = client.Close()
		resp.Diagnostics.AddError("Failed to read running config",
			"Unable to read the running configuration during provider setup: "+err.Error())
		return
	}
	if parsed, err := parser.ParseRunningConfig(output); err != nil {
		resp.Diagnostics.AddWarning("Could not detect firmware dialect",
			"The running configuration could not be parsed: "+err.Error()+
				". Firmware- and image-specific features will not be validated before commands are sent, "+
				"and FastIron 08.0.9x command syntax will be used.")
	} else {
		data.Dialect = dialect.FromConfig(parsed)
	}

	resp.DataSourceData = data
	resp.ResourceData = data
}

func (p *FastIronICXProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		icxresource.NewVLANResource,
		icxresource.NewInterfaceEthernetResource,
		icxresource.NewInterfaceVEResource,
		icxresource.NewManagementIPResource,
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
