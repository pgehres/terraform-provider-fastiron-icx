package resource

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// guardedRawCommandValidator rejects raw CLI lines that would bypass the
// safeguards of a dedicated attribute:
//
//   - `hostname …` changes the CLI prompt the SSH client keys on, so sent
//     through an ordinary raw path the session waits out the read timeout and
//     every later command fails; icx_system.hostname uses the prompt
//     re-detecting path instead.
//   - `ip address …` and `ip default-gateway …` can sever the provider's own
//     session; icx_management_ip (switch image) gates them behind
//     allow_management_ip_change and queues the save in the same write, and
//     icx_interface_ve.ip_address handles router-image management. Through
//     raw_config the automatic `no` prefix on destroy would also send
//     `no ip address …` and remove the address for good.
//
// The `no` forms are rejected too, as are the unambiguous abbreviations the
// CLI accepts ("hostn", "ip addr", "ip default-g"). This is defense in depth
// at plan time, not a parser: anything that slips through still reaches the
// switch exactly as written.
type guardedRawCommandValidator struct{}

func (guardedRawCommandValidator) Description(context.Context) string {
	return "must not be a `hostname`, `ip address` or `ip default-gateway` command (or their `no` forms); " +
		"use icx_system.hostname, icx_management_ip or icx_interface_ve.ip_address instead"
}

func (v guardedRawCommandValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (guardedRawCommandValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if msg := guardedRawCommand(req.ConfigValue.ValueString()); msg != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Command not allowed in raw configuration", msg)
	}
}

// isAbbrev reports whether tok is an abbreviation of keyword at least min
// characters long, the way the FastIron CLI matches keywords.
func isAbbrev(tok, keyword string, min int) bool {
	return len(tok) >= min && strings.HasPrefix(keyword, tok)
}

// guardedRawCommand returns why cmd may not be sent as raw configuration, or
// "" when it may.
func guardedRawCommand(cmd string) string {
	fields := strings.Fields(strings.ToLower(cmd))
	if len(fields) > 0 && fields[0] == "no" {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return ""
	}
	switch {
	case isAbbrev(fields[0], "hostname", 5):
		return fmt.Sprintf("%q: set the hostname with icx_system.hostname. `hostname` changes the CLI prompt "+
			"the provider recognises command output by; sent as a raw command the session waits for the old "+
			"prompt until it times out and every later command fails.", cmd)
	case fields[0] == "ip" && len(fields) > 1 && isAbbrev(fields[1], "address", 3):
		return fmt.Sprintf("%q: manage the management address with icx_management_ip (switch image) or "+
			"icx_interface_ve.ip_address (router image). A raw `ip address` bypasses the "+
			"allow_management_ip_change safeguard and can sever the provider's session, and the automatic "+
			"`no` on destroy would remove the address.", cmd)
	case fields[0] == "ip" && len(fields) > 1 && isAbbrev(fields[1], "default-gateway", 9):
		return fmt.Sprintf("%q: manage the default gateway with icx_management_ip.default_gateway. A raw "+
			"`ip default-gateway` bypasses the allow_management_ip_change safeguard and can sever the "+
			"provider's session, and the automatic `no` on destroy would remove the gateway.", cmd)
	}
	return ""
}

// guardedRawConfigListValidator applies guardedRawCommandValidator to every
// element of a raw command list.
func guardedRawConfigListValidator() validator.List {
	return listvalidator.ValueStringsAre(guardedRawCommandValidator{})
}
