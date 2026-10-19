package resource

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// ipv4Validator accepts a plain dotted-quad IPv4 address (no prefix length).
// netip.Addr.Is4 is false for IPv4-mapped IPv6 ("::ffff:10.0.20.1"), which
// net.IP.To4 would have accepted and the CLI would have received verbatim.
type ipv4Validator struct{}

func (ipv4Validator) Description(context.Context) string {
	return "must be an IPv4 address such as 10.0.20.1"
}

func (v ipv4Validator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v ipv4Validator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is4() {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IPv4 address",
			fmt.Sprintf("%q %s.", s, v.Description(ctx)))
	}
}

// ipv4CIDRValidator accepts an IPv4 host address in CIDR notation, e.g.
// "10.0.20.5/24" — the form icx_interface_ve uses too, converted to
// `address mask` for the CLI. The address must be dotted-quad IPv4: an
// IPv4-mapped IPv6 prefix ("::ffff:10.0.20.5/120") is refused, since it would
// otherwise be rendered with an IPv6 mask. The prefix length is 0-32.
type ipv4CIDRValidator struct{}

func (ipv4CIDRValidator) Description(context.Context) string {
	return "must be an IPv4 address in CIDR notation such as 10.0.20.5/24"
}

func (v ipv4CIDRValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v ipv4CIDRValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	prefix, err := netip.ParsePrefix(s)
	if err != nil || !prefix.Addr().Is4() {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IPv4 CIDR",
			fmt.Sprintf("%q %s.", s, v.Description(ctx)))
	}
}
