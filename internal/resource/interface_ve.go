package resource

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var (
	_ resource.Resource                = &InterfaceVEResource{}
	_ resource.ResourceWithImportState = &InterfaceVEResource{}
)

type InterfaceVEResource struct {
	client sshclient.CommandExecutor
}

type InterfaceVEResourceModel struct {
	ID        types.String `tfsdk:"id"`
	VeID      types.Int64  `tfsdk:"ve_id"`
	IPAddress types.String `tfsdk:"ip_address"`
	RawConfig types.List   `tfsdk:"raw_config"`
}

func NewInterfaceVEResource() resource.Resource {
	return &InterfaceVEResource{}
}

func (r *InterfaceVEResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_interface_ve"
}

func (r *InterfaceVEResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a virtual ethernet (VE) interface on an ICX switch. Note: the VE must first be created by a VLAN's router_interface attribute.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"ve_id": schema.Int64Attribute{
				Description: "VE interface number.",
				Required:    true,
			},
			"ip_address": schema.StringAttribute{
				Description: "IP address in CIDR notation (e.g., \"10.0.1.1/24\"). Converted to address + mask format for the switch.",
				Optional:    true,
			},
			"raw_config": schema.ListAttribute{
				Description: "Additional raw CLI commands to execute within the VE interface context.",
				Optional:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (r *InterfaceVEResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *InterfaceVEResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan InterfaceVEResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := []string{fmt.Sprintf("interface ve %d", plan.VeID.ValueInt64())}

	if !plan.IPAddress.IsNull() {
		ipMask, err := cidrToAddressMask(plan.IPAddress.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid IP address", err.Error())
			return
		}
		commands = append(commands, fmt.Sprintf("ip address %s", ipMask))
	}

	rawLines := listToStringSlice(ctx, plan.RawConfig, &resp.Diagnostics)
	commands = append(commands, rawLines...)
	commands = append(commands, "exit")

	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		resp.Diagnostics.AddError("Failed to create VE interface", err.Error())
		return
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}

	plan.ID = types.StringValue(strconv.FormatInt(plan.VeID.ValueInt64(), 10))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *InterfaceVEResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state InterfaceVEResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	config, err := r.getRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	veID := int(state.VeID.ValueInt64())
	ve := config.FindVEInterface(veID)
	if ve == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	if ve.IPAddress != "" {
		cidr, err := addressMaskToCIDR(ve.IPAddress)
		if err != nil {
			// Store as-is if conversion fails.
			state.IPAddress = types.StringValue(ve.IPAddress)
		} else {
			state.IPAddress = types.StringValue(cidr)
		}
	} else {
		state.IPAddress = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *InterfaceVEResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state InterfaceVEResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	veID := plan.VeID.ValueInt64()
	commands := []string{fmt.Sprintf("interface ve %d", veID)}

	if !plan.IPAddress.Equal(state.IPAddress) {
		// Remove old IP first if changing.
		if !state.IPAddress.IsNull() {
			oldMask, _ := cidrToAddressMask(state.IPAddress.ValueString())
			if oldMask != "" {
				commands = append(commands, fmt.Sprintf("no ip address %s", oldMask))
			}
		}
		if !plan.IPAddress.IsNull() {
			newMask, err := cidrToAddressMask(plan.IPAddress.ValueString())
			if err != nil {
				resp.Diagnostics.AddError("Invalid IP address", err.Error())
				return
			}
			commands = append(commands, fmt.Sprintf("ip address %s", newMask))
		}
	}

	// Raw config.
	planRaw := listToStringSlice(ctx, plan.RawConfig, &resp.Diagnostics)
	stateRaw := listToStringSlice(ctx, state.RawConfig, &resp.Diagnostics)
	for _, line := range stateRaw {
		if !stringSliceContains(planRaw, line) {
			commands = append(commands, "no "+line)
		}
	}
	for _, line := range planRaw {
		if !stringSliceContains(stateRaw, line) {
			commands = append(commands, line)
		}
	}

	commands = append(commands, "exit")

	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		resp.Diagnostics.AddError("Failed to update VE interface", err.Error())
		return
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}

	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *InterfaceVEResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state InterfaceVEResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := []string{fmt.Sprintf("no interface ve %d", state.VeID.ValueInt64())}

	if err := r.client.ExecuteInConfigMode(commands); err != nil {
		resp.Diagnostics.AddError("Failed to delete VE interface", err.Error())
		return
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}
}

func (r *InterfaceVEResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	veID, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid VE ID", fmt.Sprintf("Expected numeric VE ID, got %q", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("ve_id"), veID)...)
}

func (r *InterfaceVEResource) getRunningConfig() (*parser.RunningConfig, error) {
	output, err := r.client.GetRunningConfig()
	if err != nil {
		return nil, err
	}
	return parser.ParseRunningConfig(output)
}

// cidrToAddressMask converts "10.0.1.1/24" to "10.0.1.1 255.255.255.0".
func cidrToAddressMask(cidr string) (string, error) {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	mask := net.IP(ipNet.Mask).String()
	return fmt.Sprintf("%s %s", ip.String(), mask), nil
}

// addressMaskToCIDR converts "10.0.1.1 255.255.255.0" to "10.0.1.1/24".
func addressMaskToCIDR(addrMask string) (string, error) {
	parts := strings.Fields(addrMask)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid address mask format: %q", addrMask)
	}

	ip := net.ParseIP(parts[0])
	if ip == nil {
		return "", fmt.Errorf("invalid IP: %q", parts[0])
	}

	mask := net.ParseIP(parts[1])
	if mask == nil {
		return "", fmt.Errorf("invalid mask: %q", parts[1])
	}

	maskBytes := mask.To4()
	if maskBytes == nil {
		return "", fmt.Errorf("invalid IPv4 mask: %q", parts[1])
	}

	ones, _ := net.IPMask(maskBytes).Size()
	return fmt.Sprintf("%s/%d", ip.String(), ones), nil
}
