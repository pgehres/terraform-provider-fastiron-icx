package resource

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &ManagementIPResource{}
	_ resource.ResourceWithImportState = &ManagementIPResource{}
	_ resource.ResourceWithModifyPlan  = &ManagementIPResource{}
)

// managementIPID is the fixed ID of the singleton.
const managementIPID = "management"

// ManagementIPResource manages the switch image's global management
// addressing: `ip address A.B.C.D M.M.M.M`, `ip default-gateway G` and `ip dns
// server-address ...`. The router image has none of these — it addresses
// management on a VE interface (icx_interface_ve) — so every operation is
// gated on dialect.CapGlobalManagementIP.
//
// Safety model. The provider talks to the switch over the very address this
// resource manages, so:
//   - an `ip address` command is only ever sent with allow_management_ip_change
//     = true, is always the last command of the apply, and goes through
//     ExecuteExpectingDisconnect so `write memory` is queued in the same write;
//   - Delete sends nothing (removing the address would cut off remote access
//     for good) and only forgets the resource;
//   - default_gateway and dns_servers left out of the configuration are kept
//     as the device has them, never removed.
type ManagementIPResource struct {
	client  sshclient.CommandExecutor
	dialect dialect.Dialect
}

type ManagementIPResourceModel struct {
	ID                      types.String `tfsdk:"id"`
	IPAddress               types.String `tfsdk:"ip_address"`
	DefaultGateway          types.String `tfsdk:"default_gateway"`
	DNSServers              types.List   `tfsdk:"dns_servers"`
	AllowManagementIPChange types.Bool   `tfsdk:"allow_management_ip_change"`
}

func NewManagementIPResource() resource.Resource {
	return &ManagementIPResource{}
}

func (r *ManagementIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_management_ip"
}

func (r *ManagementIPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the global management IP address, default gateway and DNS servers of a " +
			"switch-image ICX device (`ip address`, `ip default-gateway`, `ip dns server-address`). " +
			"Switch image only: router-image devices address management on a VE interface " +
			"(icx_interface_ve). Singleton resource with ID \"management\". Destroying it sends no " +
			"commands; the address stays on the device.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"ip_address": schema.StringAttribute{
				Description: "Management IP address in CIDR notation (e.g. \"10.0.20.5/24\"), sent as " +
					"`ip address 10.0.20.5 255.255.255.0`. Changing it over SSH severs the provider's " +
					"own session, so a change is refused unless allow_management_ip_change is true.",
				Required: true,
				Validators: []validator.String{
					ipv4CIDRValidator{},
				},
			},
			"default_gateway": schema.StringAttribute{
				Description: "Default gateway (`ip default-gateway`). When omitted the device's current " +
					"gateway is kept and recorded in state; the provider only removes a gateway that was " +
					"previously set through this attribute.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					ipv4Validator{},
				},
			},
			"dns_servers": schema.ListAttribute{
				Description: "DNS servers (`ip dns server-address`), in order, up to four. When omitted " +
					"the device's current servers are kept and recorded in state.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.List{
					listvalidator.SizeAtMost(4),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(ipv4Validator{}),
				},
			},
			"allow_management_ip_change": schema.BoolAttribute{
				Description: "Must be true for the provider to send an `ip address` command at all — " +
					"on create when the device's address differs from ip_address, and on any change of " +
					"ip_address. The SSH session is expected to drop; afterwards point the provider's host " +
					"at the new address and verify the startup configuration saved it. Defaults to false.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
		},
	}
}

func (r *ManagementIPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerdata.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *providerdata.ProviderData, got %T", req.ProviderData))
		return
	}
	r.client = data.Client
	r.dialect = data.Dialect
}

const (
	titleIPChangeRefused = "Management IP change refused"
	titleIPChangeSevered = "Management IP changed; SSH session severed (expected)"
)

// ipChangeProcedure is the after-care every message about an address change
// must carry.
const ipChangeProcedure = "Run this change on its own (`terraform apply -target=<this resource>`), since every " +
	"command after it fails until the provider reconnects. The provider sends the new address last, " +
	"together with `end` and `write memory` in a single write, and treats the dropped session as " +
	"expected. Afterwards point the provider block's host at the new address, run `terraform plan` " +
	"(it should show no changes), and confirm the startup configuration holds the new address " +
	"(`show configuration | include ip address`); if it does not, run `write memory` by hand."

func ipChangeRefusedMessage(from, to string) string {
	if from == "" {
		from = "<none: the device has no static ip address>"
	}
	return fmt.Sprintf("Setting the management address would send `ip address` (%s -> %s). The provider is "+
		"connected to the switch through that address, so the command severs its own SSH session. "+
		"No commands were sent. If this change is intended, set allow_management_ip_change = true. %s",
		from, to, ipChangeProcedure)
}

func ipChangeSeveredMessage(to string) string {
	return fmt.Sprintf("The new management address %s was sent and the switch stopped answering, as expected. "+
		"Whether the queued `write memory` ran cannot be confirmed from here. %s", to, ipChangeProcedure)
}

// ModifyPlan refuses, at plan time, what would fail or do damage at apply:
// the resource on a known router image, and an `ip address` change without
// allow_management_ip_change. Destroy plans pass (Delete sends nothing). For
// a create the device is consulted — a read — so a mismatch between the
// configured address and the device's shows up in `terraform plan`, not
// mid-apply; the apply-time checks in Create/Update remain as the backstop.
func (r *ManagementIPResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	if !requireCapability(r.dialect, dialect.CapGlobalManagementIP, "icx_management_ip", &resp.Diagnostics) {
		return
	}
	var plan ManagementIPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.IPAddress.IsUnknown() {
		return
	}

	var current string
	if req.State.Raw.IsNull() {
		if r.client == nil {
			return
		}
		config, err := r.getRunningConfig()
		if err != nil {
			// Not an error at plan time: Create reads again and reports it.
			return
		}
		current = globalIPToCIDR(config.Global.IPAddress)
	} else {
		var state ManagementIPResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		current = state.IPAddress.ValueString()
	}

	if current == plan.IPAddress.ValueString() {
		return
	}
	switch {
	case plan.AllowManagementIPChange.IsUnknown():
		// Decided at apply time.
	case !plan.AllowManagementIPChange.ValueBool():
		resp.Diagnostics.AddAttributeError(path.Root("ip_address"), titleIPChangeRefused,
			ipChangeRefusedMessage(current, plan.IPAddress.ValueString()))
	default:
		resp.Diagnostics.AddAttributeWarning(path.Root("ip_address"), "Management IP change will sever the SSH session",
			fmt.Sprintf("Applying will send `ip address` (%s -> %s) as the last command and the provider's "+
				"session will drop. %s", current, plan.IPAddress.ValueString(), ipChangeProcedure))
	}
}

func (r *ManagementIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ManagementIPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !requireCapability(r.dialect, dialect.CapGlobalManagementIP, "icx_management_ip", &resp.Diagnostics) {
		return
	}

	// Create is a reconciliation against the device: anything already set the
	// way the plan wants is left alone, and attributes omitted from the
	// configuration adopt the device's values.
	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}
	device := managementFromConfig(ctx, config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.DefaultGateway.IsUnknown() {
		plan.DefaultGateway = device.DefaultGateway
	}
	if plan.DNSServers.IsUnknown() {
		plan.DNSServers = device.DNSServers
	}
	if plan.AllowManagementIPChange.IsUnknown() {
		plan.AllowManagementIPChange = types.BoolValue(false)
	}
	plan.ID = types.StringValue(managementIPID)

	r.apply(ctx, "configure", &device, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ManagementIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ManagementIPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A device positively running the router image has no global management
	// address (a switch→router reimage): drop the stale resource so it can be
	// rebuilt as icx_interface_ve, mirroring what icx_interface_ve does on a
	// switch image.
	if !r.dialect.Has(dialect.CapGlobalManagementIP) {
		resp.State.RemoveResource(ctx)
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}
	device := managementFromConfig(ctx, config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	state.IPAddress = device.IPAddress
	state.DefaultGateway = device.DefaultGateway
	state.DNSServers = device.DNSServers
	// Terraform-only; nothing on the device to read it from.
	if state.AllowManagementIPChange.IsNull() || state.AllowManagementIPChange.IsUnknown() {
		state.AllowManagementIPChange = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ManagementIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ManagementIPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !requireCapability(r.dialect, dialect.CapGlobalManagementIP, "icx_management_ip", &resp.Diagnostics) {
		return
	}

	// Omitted computed attributes with nothing in state stay unknown through
	// planning; "keep what the device has" means keep state.
	if plan.DefaultGateway.IsUnknown() {
		plan.DefaultGateway = state.DefaultGateway
	}
	if plan.DNSServers.IsUnknown() {
		plan.DNSServers = state.DNSServers
	}
	if plan.AllowManagementIPChange.IsUnknown() {
		plan.AllowManagementIPChange = state.AllowManagementIPChange
	}
	plan.ID = state.ID

	r.apply(ctx, "update", &state, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// apply reconciles the device from `from` (its current addressing, as read
// from the device on create or from state on update) to `to`. Gateway and
// DNS changes go first through the ordinary path; an address change, if
// allowed, goes last through the disconnect-tolerant path with the save
// queued behind it. A severed session is reported as a warning — the
// resource is in the state the plan asked for; only the save is unconfirmed.
func (r *ManagementIPResource) apply(ctx context.Context, op string, from, to *ManagementIPResourceModel, diags *diag.Diagnostics) {
	var commands []string
	commands = append(commands, gatewayCommands(from.DefaultGateway, to.DefaultGateway)...)
	commands = append(commands, dnsServerCommands(
		listToStringSlice(ctx, from.DNSServers, diags),
		listToStringSlice(ctx, to.DNSServers, diags))...)
	if diags.HasError() {
		return
	}

	var ipCommand string
	if !to.IPAddress.Equal(from.IPAddress) {
		if !to.AllowManagementIPChange.ValueBool() {
			diags.AddAttributeError(path.Root("ip_address"), titleIPChangeRefused,
				ipChangeRefusedMessage(from.IPAddress.ValueString(), to.IPAddress.ValueString()))
			return
		}
		addrMask, err := cidrToAddressMask(to.IPAddress.ValueString())
		if err != nil {
			diags.AddError("Invalid IP address", err.Error())
			return
		}
		ipCommand = "ip address " + addrMask
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			diags.AddError(fmt.Sprintf("Failed to %s management addressing", op), err.Error())
			return
		}
	}

	if ipCommand != "" {
		err := r.client.ExecuteExpectingDisconnect([]string{ipCommand})
		switch {
		case err == nil:
			// Session survived and the batch's own `write memory` ran.
		case errors.Is(err, sshclient.ErrSessionSevered):
			diags.AddAttributeWarning(path.Root("ip_address"), titleIPChangeSevered,
				ipChangeSeveredMessage(to.IPAddress.ValueString()))
		default:
			diags.AddError("Failed to set management IP address", err.Error())
		}
		return
	}

	if len(commands) > 0 {
		if err := r.client.WriteMemory(); err != nil {
			diags.AddError("Failed to save configuration", err.Error())
		}
	}
}

// Delete forgets the resource and sends nothing. Removing the management
// address, or the gateway the provider may be reaching the switch through,
// would cut off remote access to the device for good — a destroy must never
// be able to do that.
func (r *ManagementIPResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *ManagementIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !requireCapability(r.dialect, dialect.CapGlobalManagementIP, "icx_management_ip", &resp.Diagnostics) {
		return
	}
	if req.ID != managementIPID {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("icx_management_ip is a singleton; import it with the ID %q, got %q.", managementIPID, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), managementIPID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("allow_management_ip_change"), false)...)
}

func (r *ManagementIPResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}

// managementFromConfig reads the device's global management addressing into
// attribute values: null where the device has nothing configured.
func managementFromConfig(_ context.Context, config *parser.RunningConfig, diags *diag.Diagnostics) ManagementIPResourceModel {
	m := ManagementIPResourceModel{
		ID:                      types.StringValue(managementIPID),
		IPAddress:               types.StringNull(),
		DefaultGateway:          types.StringNull(),
		DNSServers:              types.ListNull(types.StringType),
		AllowManagementIPChange: types.BoolValue(false),
	}
	if cidr := globalIPToCIDR(config.Global.IPAddress); cidr != "" {
		m.IPAddress = types.StringValue(cidr)
	} else if config.Global.IPAddress != "" {
		diags.AddError("Unparseable management IP address",
			fmt.Sprintf("The device's global `ip address %s` could not be read as address and mask.", config.Global.IPAddress))
	}
	if gw := strings.TrimSpace(config.Global.DefaultGateway); gw != "" {
		m.DefaultGateway = types.StringValue(strings.Fields(gw)[0])
	}
	if len(config.Global.DNSServers) > 0 {
		elems := make([]attr.Value, len(config.Global.DNSServers))
		for i, s := range config.Global.DNSServers {
			elems[i] = types.StringValue(s)
		}
		list, d := types.ListValue(types.StringType, elems)
		diags.Append(d...)
		m.DNSServers = list
	}
	return m
}

// globalIPToCIDR converts the parser's global IP line ("10.0.20.5
// 255.255.255.0", possibly followed by a keyword such as "dynamic") to CIDR,
// or "" when there is none or it cannot be parsed.
func globalIPToCIDR(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return ""
	}
	cidr, err := addressMaskToCIDR(fields[0] + " " + fields[1])
	if err != nil {
		return ""
	}
	return cidr
}

// gatewayCommands moves the default gateway from `from` to `to`. FastIron
// keeps a single gateway and `ip default-gateway <new>` replaces it, so a
// change never passes through a gateway-less moment; only an explicit removal
// (to null, from set) sends `no ip default-gateway`.
func gatewayCommands(from, to types.String) []string {
	if to.Equal(from) {
		return nil
	}
	if to.IsNull() || to.ValueString() == "" {
		if from.IsNull() || from.ValueString() == "" {
			return nil
		}
		return []string{"no ip default-gateway " + from.ValueString()}
	}
	return []string{"ip default-gateway " + to.ValueString()}
}

// dnsServerCommands replaces the ordered DNS server list. Order is part of the
// setting (the first server is primary) and FastIron appends on add, so any
// difference removes the current servers and re-adds the wanted ones in a
// single `ip dns server-address A B ...` command. DNS is not in the SSH path,
// so the brief gap is harmless.
func dnsServerCommands(from, to []string) []string {
	if equalStrings(from, to) {
		return nil
	}
	var commands []string
	for _, s := range from {
		commands = append(commands, "no ip dns server-address "+s)
	}
	if len(to) > 0 {
		commands = append(commands, "ip dns server-address "+strings.Join(to, " "))
	}
	return commands
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
