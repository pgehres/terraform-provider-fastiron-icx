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
// resource manages — and, when Terraform runs from another subnet, through
// the default gateway the switch routes its replies by — so:
//   - an `ip address` or `ip default-gateway` command is only ever sent with
//     allow_management_ip_change = true (refused at plan time otherwise), and
//     both go last, through ExecuteExpectingDisconnect, so `end` and `write
//     memory` are queued in the same write (see apply for the ordering);
//   - Delete sends nothing (removing the address would cut off remote access
//     for good) and only forgets the resource;
//   - default_gateway and dns_servers left out of the configuration are kept
//     as the device has them. A gateway is never removed (Optional+Computed:
//     a null configuration value plans as the prior state); DNS servers are
//     cleared only by an explicit dns_servers = [].
//   - a DHCP-learned address (`ip address A M dynamic`) is refused outright:
//     it is not this resource's to manage.
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
					"gateway is kept and recorded in state. A gateway cannot be removed through this " +
					"resource: removing the attribute keeps the current one. Changing it can sever the " +
					"provider's session (the switch routes its replies by it), so a change is refused " +
					"unless allow_management_ip_change is true.",
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
					"the device's current servers are kept and recorded in state; set it to [] to remove " +
					"them all.",
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
				Description: "Must be true for the provider to send an `ip address` or `ip default-gateway` " +
					"command at all — on create when the device's address or gateway differs from the " +
					"configuration, and on any change of ip_address or default_gateway. The SSH session may " +
					"drop; afterwards point the provider's host at the new address (if it changed) and verify " +
					"the startup configuration saved the change. Defaults to false.",
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
	titleIPChangeRefused      = "Management IP change refused"
	titleGatewayChangeRefused = "Management default gateway change refused"
	titleIPChangeSevered      = "Management addressing changed; SSH session severed (expected)"
	titleDHCPAddress          = "Management address is DHCP-learned"
)

// ipChangeProcedure is the after-care every message about an address or
// gateway change must carry.
const ipChangeProcedure = "Run this change on its own (`terraform apply -target=<this resource>`), since every " +
	"command after it fails if the session drops. The provider sends the gateway and address changes " +
	"last, together with `end` and `write memory` in a single write, and treats a dropped session as " +
	"expected. Afterwards point the provider block's host at the new address if it changed (and make sure " +
	"the machine running Terraform can route to it), run `terraform plan` (it should show no changes), and " +
	"confirm the startup configuration holds the change (`show configuration | include ip`); if it does " +
	"not, run `write memory` by hand."

func ipChangeRefusedMessage(from, to string) string {
	if from == "" {
		from = "<none: the device has no static ip address>"
	}
	return fmt.Sprintf("Setting the management address would send `ip address` (%s -> %s). The provider is "+
		"connected to the switch through that address, so the command severs its own SSH session. "+
		"No commands were sent. If this change is intended, set allow_management_ip_change = true. %s",
		from, to, ipChangeProcedure)
}

func gatewayChangeRefusedMessage(from, to string) string {
	if from == "" {
		from = "<none>"
	}
	return fmt.Sprintf("Setting the default gateway would send `ip default-gateway` (%s -> %s). The switch "+
		"routes its replies to any off-subnet client through that gateway, so when Terraform reaches the "+
		"switch through a router the command can sever the provider's own SSH session, leaving the change "+
		"unsaved. No commands were sent. If this change is intended, set allow_management_ip_change = true. %s",
		from, to, ipChangeProcedure)
}

func ipChangeSeveredMessage(sent []string) string {
	return fmt.Sprintf("%s was sent and the switch stopped answering, as expected. "+
		"Whether the queued `write memory` ran cannot be confirmed from here. %s",
		"`"+strings.Join(sent, "` and `")+"`", ipChangeProcedure)
}

func dhcpAddressMessage(raw string) string {
	return fmt.Sprintf("The device's management address (`ip address %s`) was learned from DHCP. "+
		"icx_management_ip manages a static address only and would report the lease as configuration, so "+
		"it refuses to read or adopt it. Remove the resource from the configuration (and `terraform state "+
		"rm` it if it is already in state), or configure a static address from the console first.", raw)
}

// ModifyPlan refuses, at plan time, what would fail or do damage at apply:
// the resource on a known router image, a DHCP-learned device address, and an
// `ip address` or `ip default-gateway` change without
// allow_management_ip_change. Destroy plans pass (Delete sends nothing). For
// a create the device is consulted — a read — so a mismatch between the
// configuration and the device shows up in `terraform plan`, not mid-apply;
// the apply-time checks in Create/Update remain as the backstop.
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

	var currentIP, currentGW string
	if req.State.Raw.IsNull() {
		if r.client == nil {
			return
		}
		config, err := r.getRunningConfig()
		if err != nil {
			// Not an error at plan time: Create reads again and reports it.
			return
		}
		if config.Global.IPAddressDynamic {
			resp.Diagnostics.AddAttributeError(path.Root("ip_address"), titleDHCPAddress,
				dhcpAddressMessage(config.Global.IPAddress))
			return
		}
		currentIP = globalIPToCIDR(config.Global.IPAddress)
		currentGW = deviceGateway(config)
	} else {
		var state ManagementIPResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		currentIP = state.IPAddress.ValueString()
		currentGW = state.DefaultGateway.ValueString()
	}

	ipChange := currentIP != plan.IPAddress.ValueString()
	// An unknown or null planned gateway means "keep the device's" — no change.
	gwChange := !plan.DefaultGateway.IsUnknown() && !plan.DefaultGateway.IsNull() &&
		plan.DefaultGateway.ValueString() != currentGW
	if !ipChange && !gwChange {
		return
	}
	switch {
	case plan.AllowManagementIPChange.IsUnknown():
		// Decided at apply time.
	case !plan.AllowManagementIPChange.ValueBool():
		if ipChange {
			resp.Diagnostics.AddAttributeError(path.Root("ip_address"), titleIPChangeRefused,
				ipChangeRefusedMessage(currentIP, plan.IPAddress.ValueString()))
		}
		if gwChange {
			resp.Diagnostics.AddAttributeError(path.Root("default_gateway"), titleGatewayChangeRefused,
				gatewayChangeRefusedMessage(currentGW, plan.DefaultGateway.ValueString()))
		}
	default:
		var changes []string
		attr := path.Root("ip_address")
		if gwChange {
			changes = append(changes, fmt.Sprintf("`ip default-gateway` (%s -> %s)", orNone(currentGW), plan.DefaultGateway.ValueString()))
			attr = path.Root("default_gateway")
		}
		if ipChange {
			changes = append(changes, fmt.Sprintf("`ip address` (%s -> %s)", orNone(currentIP), plan.IPAddress.ValueString()))
			attr = path.Root("ip_address")
		}
		resp.Diagnostics.AddAttributeWarning(attr, "Management addressing change may sever the SSH session",
			fmt.Sprintf("Applying will send %s as the last commands, and the provider's session may drop. %s",
				strings.Join(changes, " and "), ipChangeProcedure))
	}
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
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
	// dns_servers = [] (all removed) and "none on the device" are the same
	// thing; keep the configured empty list rather than flipping it to null,
	// which would show a diff on every plan.
	if !(device.DNSServers.IsNull() && isEmptyKnownList(state.DNSServers)) {
		state.DNSServers = device.DNSServers
	}
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
// from the device on create or from state on update) to `to`.
//
// DNS changes are not in the SSH path and go first, through the ordinary
// path. Gateway and address changes are session-risky — the address is the
// one the provider is connected to, and the gateway is the switch's return
// route to an off-subnet Terraform runner — so both require
// allow_management_ip_change and go last, together, through the
// disconnect-tolerant path with `end` and `write memory` queued behind them.
// A severed session is reported as a warning: the resource is in the state
// the plan asked for; only the save is unconfirmed.
func (r *ManagementIPResource) apply(ctx context.Context, op string, from, to *ManagementIPResourceModel, diags *diag.Diagnostics) {
	dnsCommands := dnsServerCommands(
		listToStringSlice(ctx, from.DNSServers, diags),
		listToStringSlice(ctx, to.DNSServers, diags))
	if diags.HasError() {
		return
	}

	// The risky batch: every command needed for the final addressing, in
	// one write. Order: gateway first, then address, then (appended by
	// ExecuteExpectingDisconnect) `end` and `write memory`.
	//
	// The whole batch is in the switch's input buffer before it executes the
	// first line, and the shell normally runs every buffered line even after
	// the TCP session is cut — which is why each line needed for the final
	// state must be in it, not sent afterwards. Order still matters for the
	// case where the shell dies with the session and drops what it has not
	// yet run: the `ip address` change is the command certain to cut a
	// runner connected through that address, whereas the gateway change only
	// might. Putting the gateway first means it has already executed when
	// the certain cut happens, so the device is never left with the new
	// address and the old (possibly off-subnet) gateway; and a gateway-only
	// change is followed directly by `end` + `write memory`. Whether FastIron
	// accepts a gateway outside the current subnet before the address moves
	// is not validated on a live device yet. If it rejected it, the address
	// line and the save would still run (they are already buffered), leaving
	// the new address with the old gateway; the CLI error is reported only if
	// the session survives. That is the first thing to check on a lab switch.
	gw := gatewayCommands(from.DefaultGateway, to.DefaultGateway)
	ipChange := !to.IPAddress.Equal(from.IPAddress)
	if !to.AllowManagementIPChange.ValueBool() {
		if ipChange {
			diags.AddAttributeError(path.Root("ip_address"), titleIPChangeRefused,
				ipChangeRefusedMessage(from.IPAddress.ValueString(), to.IPAddress.ValueString()))
		}
		if len(gw) > 0 {
			diags.AddAttributeError(path.Root("default_gateway"), titleGatewayChangeRefused,
				gatewayChangeRefusedMessage(from.DefaultGateway.ValueString(), to.DefaultGateway.ValueString()))
		}
		if diags.HasError() {
			return
		}
	}
	var risky []string
	var riskyAttr path.Path
	if len(gw) > 0 {
		risky = append(risky, gw...)
		riskyAttr = path.Root("default_gateway")
	}
	if ipChange {
		addrMask, err := cidrToAddressMask(to.IPAddress.ValueString())
		if err != nil {
			diags.AddError("Invalid IP address", err.Error())
			return
		}
		risky = append(risky, "ip address "+addrMask)
		riskyAttr = path.Root("ip_address")
	}

	if len(dnsCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(dnsCommands); err != nil {
			diags.AddError(fmt.Sprintf("Failed to %s management addressing", op), err.Error())
			return
		}
	}

	if len(risky) > 0 {
		err := r.client.ExecuteExpectingDisconnect(risky)
		switch {
		case err == nil:
			// Session survived and the batch's own `write memory` ran.
		case errors.Is(err, sshclient.ErrSessionSevered):
			diags.AddAttributeWarning(riskyAttr, titleIPChangeSevered, ipChangeSeveredMessage(risky))
		default:
			diags.AddError(fmt.Sprintf("Failed to %s management addressing", op), err.Error())
		}
		return
	}

	if len(dnsCommands) > 0 {
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
	if config.Global.IPAddressDynamic {
		diags.AddAttributeError(path.Root("ip_address"), titleDHCPAddress, dhcpAddressMessage(config.Global.IPAddress))
		return m
	}
	if cidr := globalIPToCIDR(config.Global.IPAddress); cidr != "" {
		m.IPAddress = types.StringValue(cidr)
	} else if config.Global.IPAddress != "" {
		diags.AddError("Unparseable management IP address",
			fmt.Sprintf("The device's global `ip address %s` could not be read as address and mask.", config.Global.IPAddress))
	}
	if gw := deviceGateway(config); gw != "" {
		m.DefaultGateway = types.StringValue(gw)
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

// deviceGateway returns the device's global default gateway address, or "".
func deviceGateway(config *parser.RunningConfig) string {
	if gw := strings.Fields(config.Global.DefaultGateway); len(gw) > 0 {
		return gw[0]
	}
	return ""
}

// isEmptyKnownList reports whether l is a known, non-null, empty list.
func isEmptyKnownList(l types.List) bool {
	return !l.IsNull() && !l.IsUnknown() && len(l.Elements()) == 0
}

// globalIPToCIDR converts the parser's global IP line ("10.0.20.5
// 255.255.255.0", possibly followed by a keyword) to CIDR, or "" when there
// is none or it cannot be parsed. A DHCP-learned ("dynamic") address is
// flagged by the parser and refused before this is consulted.
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
// change never passes through a gateway-less moment. A null or unknown target
// means "keep the device's" and sends nothing: default_gateway is
// Optional+Computed, so Terraform never plans a removal, and this resource
// never sends `no ip default-gateway` — removing the gateway can cut off an
// off-subnet provider for good, exactly like Delete.
func gatewayCommands(from, to types.String) []string {
	if to.IsNull() || to.IsUnknown() || to.ValueString() == "" || to.Equal(from) {
		return nil
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
