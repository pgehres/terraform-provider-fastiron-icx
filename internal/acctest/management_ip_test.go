package acctest

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

// testDNSServer is appended to the device's DNS server list for the duration
// of the test. It is in the TEST-NET-1 documentation range and is never the
// first (primary) server, so name resolution on the device is unaffected.
const testDNSServer = "192.0.2.53"

func managementIPConfig(cidr string, dns []string) string {
	return fmt.Sprintf(`
data "icx_stack_unit" "this" {}

resource "icx_management_ip" "this" {
  ip_address  = %q
  dns_servers = %s
}
`, cidr, hclStringList(dns))
}

// TestAccManagementIP exercises icx_management_ip on a switch-image device
// through the only attribute that is safe to change unattended: dns_servers.
// The address is rendered as the device has it and the gateway is left to
// adopt the device's, so no `ip address` or `ip default-gateway` command is
// ever sent (both are refused without allow_management_ip_change anyway).
// Changing the address or the gateway — including whether the switch
// accepts an off-subnet gateway — is a manual lab test with console access;
// see CLAUDE.md.
//
// Router-image devices skip: the resource does not exist there. The image is
// read the same way the icx_stack_unit data source reports it, and the data
// source is included in the configuration so its image_type is checked live.
func TestAccManagementIP(t *testing.T) {
	env := Setup(t)
	before := env.Snapshot(t)
	g := before.Config.Global

	if img := before.Config.ImageType(); img != parser.ImageTypeSwitch {
		t.Skipf("icx_management_ip is switch-image only; %s runs the %s image", env.Host, img)
	}
	if g.IPAddressDynamic {
		t.Skipf("%s has a DHCP-learned management address; icx_management_ip refuses those", env.Host)
	}
	if g.IPAddress == "" {
		t.Skipf("%s has no global `ip address`; nothing for icx_management_ip to adopt", env.Host)
	}
	cidr, err := addressMaskToCIDR(g.IPAddress)
	if err != nil {
		t.Fatalf("device `ip address %s`: %v", g.IPAddress, err)
	}
	original := append([]string(nil), g.DNSServers...)
	if len(original) >= 4 {
		t.Skipf("%s already has %d DNS servers (the maximum); no room for a test server", env.Host, len(original))
	}
	for _, s := range original {
		if s == testDNSServer {
			t.Fatalf("%s already lists the harness's test DNS server %s", env.Host, testDNSServer)
		}
	}
	withTest := append(append([]string(nil), original...), testDNSServer)

	const rn = "icx_management_ip.this"
	dnsOnDevice := func(want []string) resource.TestCheckFunc {
		return env.deviceCheck(func(cfg *parser.RunningConfig) error {
			got := cfg.Global.DNSServers
			if len(got) != len(want) {
				return fmt.Errorf("DNS servers on device: %v, want %v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					return fmt.Errorf("DNS servers on device: %v, want %v", got, want)
				}
			}
			return nil
		})
	}
	dnsAttrs := func(want []string) resource.TestCheckFunc {
		checks := []resource.TestCheckFunc{resource.TestCheckResourceAttr(rn, "dns_servers.#", strconv.Itoa(len(want)))}
		for i, s := range want {
			checks = append(checks, resource.TestCheckResourceAttr(rn, fmt.Sprintf("dns_servers.%d", i), s))
		}
		return resource.ComposeAggregateTestCheckFunc(checks...)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: env.ProviderFactories(t),
		// Destroy sends nothing by design; only the configuration must match.
		CheckDestroy: env.checkDestroy(before, nil),
		Steps: []resource.TestStep{
			{
				Config: env.ProviderConfig() + managementIPConfig(cidr, withTest),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.icx_stack_unit.this", "image_type", "switch"),
					resource.TestCheckResourceAttr(rn, "id", "management"),
					resource.TestCheckResourceAttr(rn, "ip_address", cidr),
					resource.TestCheckResourceAttr(rn, "allow_management_ip_change", "false"),
					dnsAttrs(withTest),
					dnsOnDevice(withTest),
				),
			},
			{
				// Back to the original list ([] when the device had none).
				Config: env.ProviderConfig() + managementIPConfig(cidr, original),
				Check: resource.ComposeAggregateTestCheckFunc(
					dnsAttrs(original),
					dnsOnDevice(original),
				),
			},
		},
	})
}
