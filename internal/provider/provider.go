package provider

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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
	// client is the SSH session opened by Configure, kept so Close can
	// release it. Terraform itself never needs this: a provider process
	// exits after each command and the connection dies with it.
	client *sshclient.Client
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
	Transport                 types.String `tfsdk:"transport"`
	AllowEmptyCredentials     types.Bool   `tfsdk:"allow_empty_credentials"`
	InsecureTransportAck      types.Bool   `tfsdk:"insecure_transport_ack"`
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
		Description: "Terraform provider for Brocade/Ruckus ICX switches running FastIron firmware. " +
			"Communicates via the switch CLI over SSH (default) or, for lab and competition switches that expose nothing else, telnet.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Description: "Hostname or IP address of the ICX switch. Can also be set with the FASTIRON_HOST environment variable.",
				Optional:    true,
			},
			"port": schema.Int64Attribute{
				Description: "TCP port of the CLI transport. Defaults to 22 for SSH and 23 for telnet. Can also be set with the FASTIRON_PORT environment variable.",
				Optional:    true,
			},
			"username": schema.StringAttribute{
				Description: "Login username. Required unless allow_empty_credentials is set. Can also be set with the FASTIRON_USERNAME environment variable.",
				Optional:    true,
				Validators:  []validator.String{noControlCharacters{}},
			},
			"password": schema.StringAttribute{
				Description: "Login password. Required unless allow_empty_credentials is set. Can also be set with the FASTIRON_PASSWORD environment variable.",
				Optional:    true,
				Sensitive:   true,
				Validators:  []validator.String{noControlCharacters{}},
			},
			"enable_password": schema.StringAttribute{
				Description: "Enable mode password. Required if the switch requires enable authentication. Can also be set with the FASTIRON_ENABLE_PASSWORD environment variable.",
				Optional:    true,
				Sensitive:   true,
				Validators:  []validator.String{noControlCharacters{}},
			},
			"timeout": schema.Int64Attribute{
				Description: "Connection and per-command read timeout in seconds. Defaults to 30.",
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
			"transport": schema.StringAttribute{
				Description: "CLI transport: \"ssh\" (default) or \"telnet\". Telnet is PLAINTEXT: credentials and every " +
					"command cross the network unencrypted and the switch's identity is never verified. It exists only for " +
					"factory-fresh or competition switches that expose no SSH, and requires insecure_transport_ack = true. " +
					"Can also be set with the FASTIRON_TRANSPORT environment variable.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf(sshclient.TransportSSH, sshclient.TransportTelnet)},
			},
			"allow_empty_credentials": schema.BoolAttribute{
				Description: "If true, permit an empty username, password and/or enable_password, as a factory-fresh switch " +
					"with no local users accepts. Over SSH the provider then also offers keyboard-interactive authentication " +
					"answered with the (empty) password. WARNING: a switch that accepts this is open to anyone who can reach " +
					"it; lab and competition use only. A warning is emitted on every plan while this is set. Defaults to false.",
				Optional: true,
			},
			"insecure_transport_ack": schema.BoolAttribute{
				Description: "Required (true) when transport = \"telnet\": acknowledges that the connection is plaintext " +
					"and unauthenticated — anyone on the path can read the credentials and configuration or alter the " +
					"commands. Lab and competition use only. Has no effect over SSH. Defaults to false.",
				Optional: true,
			},
		},
	}
}

// connectionSettings is the provider configuration with environment
// variables applied and nothing validated yet.
type connectionSettings struct {
	Host, Username, Password, EnablePassword string
	HostKey, Transport                       string
	Port, Timeout                            int
	InsecureSkipHostKeyVerify                bool
	LegacySSHAlgorithms                      bool
	AllowEmptyCredentials                    bool
	InsecureTransportAck                     bool
}

// resolveConnection applies the provider's connection policy to the settings
// and returns the sshclient options to connect with. Errors stop Configure;
// warnings are shown on every plan and apply, so the insecure opt-ins are
// never silent. The rules:
//
//   - host is required;
//   - username and password are required unless allow_empty_credentials,
//     which warns;
//   - over SSH one of host_key / insecure_skip_host_key_verify is required;
//   - telnet requires insecure_transport_ack, warns that it is plaintext,
//     and warns about SSH-only settings it ignores.
func resolveConnection(s connectionSettings) (sshclient.Options, diag.Diagnostics) {
	var diags diag.Diagnostics

	transport := s.Transport
	if transport == "" {
		transport = sshclient.TransportSSH
	}
	if transport != sshclient.TransportSSH && transport != sshclient.TransportTelnet {
		diags.AddError("Invalid transport",
			fmt.Sprintf("transport must be %q or %q, got %q.", sshclient.TransportSSH, sshclient.TransportTelnet, transport))
		return sshclient.Options{}, diags
	}

	if s.Host == "" {
		diags.AddError("Missing host", "The host must be set in the provider configuration or the FASTIRON_HOST environment variable.")
		return sshclient.Options{}, diags
	}

	if s.AllowEmptyCredentials {
		var empty []string
		for _, f := range []struct{ name, value string }{
			{"username", s.Username}, {"password", s.Password}, {"enable_password", s.EnablePassword},
		} {
			if f.value == "" {
				empty = append(empty, f.name)
			}
		}
		detail := "allow_empty_credentials is set. "
		if len(empty) > 0 {
			detail += "The provider will log in to " + s.Host + " with an empty " + strings.Join(empty, ", ") + ". "
		}
		detail += "A switch that accepts this is fully controllable by anyone who can reach it. " +
			"Configure local users and an enable password as soon as the switch is provisioned, " +
			"and remove allow_empty_credentials once they exist."
		diags.AddWarning("Connecting with empty credentials", detail)
	} else {
		// The first sentence of each is the pre-telnet wording, unchanged.
		if s.Username == "" {
			diags.AddError("Missing username", "The username must be set in the provider configuration or the FASTIRON_USERNAME environment variable."+
				emptyCredentialsHint)
			return sshclient.Options{}, diags
		}
		if s.Password == "" {
			diags.AddError("Missing password", "The password must be set in the provider configuration or the FASTIRON_PASSWORD environment variable."+
				emptyCredentialsHint)
			return sshclient.Options{}, diags
		}
	}

	// The schema validators cover values set in the configuration; this
	// covers the environment variables too. Credentials are written to the
	// switch verbatim (over telnet, as typed input), so a control character
	// could inject a command. The value is never included in the message.
	for _, f := range []struct{ name, value string }{
		{"username", s.Username}, {"password", s.Password}, {"enable_password", s.EnablePassword},
	} {
		if hasControlCharacters(f.value) {
			diags.AddError("Invalid "+f.name, f.name+" contains control characters (0x00-0x1f or 0x7f), which are not allowed.")
			return sshclient.Options{}, diags
		}
	}

	switch transport {
	case sshclient.TransportSSH:
		if s.HostKey == "" && !s.InsecureSkipHostKeyVerify {
			diags.AddError(
				"Missing SSH host key",
				fmt.Sprintf(
					"Set host_key to the switch's SSH public key or SHA256 fingerprint, "+
						"or set insecure_skip_host_key_verify = true for trusted lab networks only. "+
						"Obtain the host key with: ssh-keyscan %s (if that prints nothing — common on older "+
						"FastIron SSH stacks — use the provider repo's debug tool: go run ./cmd/debug-ssh -print-host-key)",
					s.Host,
				),
			)
			return sshclient.Options{}, diags
		}
		if s.InsecureTransportAck {
			diags.AddWarning("insecure_transport_ack has no effect over SSH",
				"insecure_transport_ack only acknowledges the plaintext telnet transport; the connection to "+s.Host+" uses SSH.")
		}
	case sshclient.TransportTelnet:
		if !s.InsecureTransportAck {
			diags.AddError("Telnet transport requires insecure_transport_ack",
				"transport = \"telnet\" sends credentials and every command to "+s.Host+" in PLAINTEXT, with no verification "+
					"of the switch's identity. Set insecure_transport_ack = true to acknowledge this. "+
					"Telnet is intended only for factory-fresh or competition switches that expose no SSH; "+
					"enable SSH (crypto key generate) and switch the provider back to SSH as soon as the switch is provisioned.")
			return sshclient.Options{}, diags
		}
		diags.AddWarning("Plaintext telnet transport",
			"The connection to "+s.Host+" uses telnet: credentials and the full configuration cross the network "+
				"unencrypted and the switch's identity is not verified. Anyone on the path can read or alter them. "+
				"Lab and competition networks only.")
		var ignored []string
		if s.HostKey != "" {
			ignored = append(ignored, "host_key")
		}
		if s.InsecureSkipHostKeyVerify {
			ignored = append(ignored, "insecure_skip_host_key_verify")
		}
		if s.LegacySSHAlgorithms {
			ignored = append(ignored, "legacy_ssh_algorithms")
		}
		if len(ignored) > 0 {
			diags.AddWarning("SSH settings ignored with telnet",
				strings.Join(ignored, ", ")+" only apply to the SSH transport and are ignored while transport = \"telnet\".")
		}
	}

	return sshclient.Options{
		Host:                      s.Host,
		Port:                      s.Port, // 0 lets the transport pick its default (22 / 23)
		Username:                  s.Username,
		Password:                  s.Password,
		EnablePassword:            s.EnablePassword,
		TimeoutSeconds:            s.Timeout,
		HostKey:                   s.HostKey,
		InsecureSkipHostKeyVerify: s.InsecureSkipHostKeyVerify,
		LegacySSHAlgorithms:       s.LegacySSHAlgorithms,
		Transport:                 transport,
		AllowEmptyCredentials:     s.AllowEmptyCredentials,
	}, diags
}

// emptyCredentialsHint follows the original "Missing username/password"
// detail as a separate sentence.
const emptyCredentialsHint = " For a factory-fresh switch with no users, set allow_empty_credentials = true instead (lab use only)."

// hasControlCharacters reports whether s contains a control character
// (0x00-0x1f) or DEL.
func hasControlCharacters(s string) bool {
	for _, b := range []byte(s) {
		if b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}

// noControlCharacters rejects strings containing control characters. Unlike
// stringvalidator.RegexMatches its diagnostic never echoes the value, so it
// is safe on sensitive attributes.
type noControlCharacters struct{}

func (noControlCharacters) Description(context.Context) string {
	return "must not contain control characters (0x00-0x1f or 0x7f)"
}

func (v noControlCharacters) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v noControlCharacters) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || !hasControlCharacters(req.ConfigValue.ValueString()) {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid "+req.Path.String(),
		req.Path.String()+" "+v.Description(ctx)+".")
}

// boolValue returns a known bool attribute's value, false otherwise.
func boolValue(v types.Bool) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueBool()
}

func (p *FastIronICXProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config FastIronICXProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	settings := connectionSettings{
		Host:                      stringValueOrEnv(config.Host, "FASTIRON_HOST"),
		Username:                  stringValueOrEnv(config.Username, "FASTIRON_USERNAME"),
		Password:                  stringValueOrEnv(config.Password, "FASTIRON_PASSWORD"),
		EnablePassword:            stringValueOrEnv(config.EnablePassword, "FASTIRON_ENABLE_PASSWORD"),
		HostKey:                   stringValueOrEnv(config.HostKey, "FASTIRON_HOST_KEY"),
		Transport:                 stringValueOrEnv(config.Transport, "FASTIRON_TRANSPORT"),
		Timeout:                   30,
		InsecureSkipHostKeyVerify: boolValue(config.InsecureSkipHostKeyVerify),
		LegacySSHAlgorithms:       boolValue(config.LegacySSHAlgorithms),
		AllowEmptyCredentials:     boolValue(config.AllowEmptyCredentials),
		InsecureTransportAck:      boolValue(config.InsecureTransportAck),
	}

	// Port 0 means "the transport's default": 22 for SSH, 23 for telnet.
	if !config.Port.IsNull() && !config.Port.IsUnknown() {
		settings.Port = int(config.Port.ValueInt64())
	} else if envPort := os.Getenv("FASTIRON_PORT"); envPort != "" {
		var p int
		if _, err := fmt.Sscanf(envPort, "%d", &p); err == nil && p > 0 {
			settings.Port = p
		}
	}

	if !config.Timeout.IsNull() && !config.Timeout.IsUnknown() {
		settings.Timeout = int(config.Timeout.ValueInt64())
	}

	opts, diags := resolveConnection(settings)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	username := opts.Username

	client, err := sshclient.Connect(opts)
	if err != nil {
		if opts.Transport == sshclient.TransportTelnet {
			resp.Diagnostics.AddError("Telnet connection failed", "Unable to connect to the ICX switch over telnet: "+err.Error())
		} else {
			// Unchanged from before the telnet transport existed.
			resp.Diagnostics.AddError("SSH connection failed", "Unable to connect to the ICX switch: "+err.Error())
		}
		return
	}

	p.client = client

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
		p.client = nil
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

// Close releases the SSH session opened by Configure, if any. A provider
// served by Terraform never needs it (the process exits after each command),
// but the acceptance test harness runs the provider in-process — one instance
// per terraform command — and must close each instance before starting the
// next, or the leaked sessions exhaust the switch's SSH session limit a few
// commands into the first test. Safe to call on an unconfigured provider and
// more than once.
func (p *FastIronICXProvider) Close() error {
	if p.client == nil {
		return nil
	}
	err := p.client.Close()
	p.client = nil
	return err
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
		icxresource.NewNTPResource,
		icxresource.NewSNMPResource,
		icxresource.NewLoggingResource,
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
