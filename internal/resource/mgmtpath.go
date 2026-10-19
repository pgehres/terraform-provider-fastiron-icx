package resource

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// Management-path detection for `disable` on icx_interface_ethernet.
//
// Administratively disabling the port that carries the provider's own SSH
// session severs it mid-apply — the same class of self-inflicted outage as
// `-replace` on a live trunk (CLAUDE.md gotcha #9). The switch cannot tell us
// which port that is directly, but its ARP table can tell us which port leads
// to the management next hop: the switch image's `ip default-gateway`, or the
// router image's default-route next hop(s). Terraform's traffic reaches the
// switch through that port whenever Terraform is not on the switch's own
// subnet, which is the usual case for a managed fleet.
//
// The check is best effort. When the next hop cannot be determined from the
// running config, or `show arp` fails or has no entry for it, the disable is
// allowed with a warning; a Terraform host on the switch's own subnet is not
// covered (its ARP entry is there, but the provider does not know its own
// address). The detection is read-only: one `show arp`.

// rePortID matches a FastIron port in unit/slot/port form, or a LAG id, as it
// appears in the Port column of `show arp`.
var rePortID = regexp.MustCompile(`^(\d+/\d+/\d+|lg\d+|lag\d+)$`)

// managementNextHops returns the IPv4 next hops the switch's management
// traffic leaves through, as far as the running config shows: the global
// `ip default-gateway` (switch image) and the next hop of every default
// `ip route` (router image).
func managementNextHops(config *parser.RunningConfig) []string {
	var hops []string
	add := func(ip string) {
		if ip == "" || stringSliceContains(hops, ip) {
			return
		}
		hops = append(hops, ip)
	}
	if f := strings.Fields(config.Global.DefaultGateway); len(f) > 0 {
		add(f[0])
	}
	for _, route := range config.Global.StaticRoutes {
		f := strings.Fields(route)
		// ip route 0.0.0.0/0 <next-hop>   |   ip route 0.0.0.0 0.0.0.0 <next-hop>
		switch {
		case len(f) >= 4 && f[2] == "0.0.0.0/0":
			add(f[3])
		case len(f) >= 5 && f[2] == "0.0.0.0" && f[3] == "0.0.0.0":
			add(f[4])
		}
	}
	return hops
}

// arpPorts maps every IP address in `show arp` output to the port its MAC
// was learned on. Lines without a recognisable port are skipped.
func arpPorts(output string) map[string]string {
	ports := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		var ip, port string
		for _, f := range fields {
			if ip == "" && isIPv4(f) {
				ip = f
			} else if rePortID.MatchString(strings.TrimSuffix(f, "*")) {
				port = strings.TrimSuffix(f, "*")
			}
		}
		if ip != "" && port != "" {
			ports[ip] = port
		}
	}
	return ports
}

func isIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// managementPathPorts resolves the ports the management next hops are
// reached through. It returns the hop→port map for the hops that resolved,
// and a reason when nothing could be resolved.
func managementPathPorts(client sshclient.CommandExecutor, config *parser.RunningConfig) (map[string]string, string) {
	hops := managementNextHops(config)
	if len(hops) == 0 {
		return nil, "the running config has no ip default-gateway or default ip route to locate the management path with"
	}
	output, err := client.Execute("show arp")
	if err != nil {
		return nil, "show arp failed: " + err.Error()
	}
	ports := arpPorts(output)
	found := make(map[string]string)
	for _, hop := range hops {
		if p, ok := ports[hop]; ok {
			found[hop] = p
		}
	}
	if len(found) == 0 {
		return nil, fmt.Sprintf("show arp has no entry for the management next hop (%s)", strings.Join(hops, ", "))
	}
	return found, ""
}

// checkManagementPathDisable refuses to disable port when the switch's
// management next hop is reached through it, recording an error and returning
// false. When the path cannot be determined it records a warning and returns
// true. No configuration commands are sent.
func checkManagementPathDisable(client sshclient.CommandExecutor, config *parser.RunningConfig, port string, diags *diag.Diagnostics) bool {
	found, reason := managementPathPorts(client, config)
	if reason != "" {
		diags.AddWarning(
			fmt.Sprintf("Cannot verify that port %s is not the management path", port),
			fmt.Sprintf("enabled = false will administratively disable port %s. The provider could not confirm "+
				"that this port does not carry its own SSH session (%s). Disabling the uplink that carries the "+
				"management VLAN severs the session mid-apply and leaves the switch reachable only by console. "+
				"Check `show arp` / `show mac-address` for the path to the Terraform host before applying.",
				port, reason),
		)
		return true
	}
	for hop, p := range found {
		if p == port {
			diags.AddError(
				"Refusing to disable the management path port",
				fmt.Sprintf("Port %s is the port through which this switch reaches its management next hop %s "+
					"(per `show arp`), so it carries the provider's own SSH session. Disabling it would sever "+
					"the session mid-apply and leave the switch reachable only by console. No commands were sent. "+
					"If the port really must go down, do it from the console.", port, hop),
			)
			return false
		}
	}
	return true
}
