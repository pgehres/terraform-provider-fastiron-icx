package acctest

import (
	"fmt"
	"sort"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

// interfaceConfig renders both scratch VLANs plus the scratch port. tagged
// and untagged are rendered only when non-zero; the port references the
// VLANs so Terraform destroys the port's membership before the VLANs.
func interfaceConfig(port string, vlan, vlan2 int, portName string, tagged []int, untagged int) string {
	s := fmt.Sprintf(`
resource "icx_vlan" "scratch" {
  vlan_id = %d
  name    = "tfacc-scratch"
}

resource "icx_vlan" "scratch2" {
  vlan_id = %d
  name    = "tfacc-scratch2"
}

resource "icx_interface_ethernet" "scratch" {
  port      = %q
  port_name = %q
`, vlan, vlan2, port, portName)
	if len(tagged) > 0 {
		refs := make([]string, len(tagged))
		for i, id := range tagged {
			refs[i] = vlanRef(id, vlan, vlan2)
		}
		s += "  tagged_vlans = ["
		for i, r := range refs {
			if i > 0 {
				s += ", "
			}
			s += r
		}
		s += "]\n"
	}
	if untagged != 0 {
		s += fmt.Sprintf("  untagged_vlan = %s\n", vlanRef(untagged, vlan, vlan2))
	}
	s += "}\n"
	return s
}

func vlanRef(id, vlan, vlan2 int) string {
	switch id {
	case vlan:
		return "icx_vlan.scratch.vlan_id"
	case vlan2:
		return "icx_vlan.scratch2.vlan_id"
	}
	return strconv.Itoa(id)
}

// TestAccInterfaceEthernet drives the scratch port through the membership
// shapes icx_interface_ethernet supports: access (untagged only), tagged
// only, trunk with an untagged VLAN, and back to access. On FastIron below
// 08.0.80 the trunk-with-untagged step is where the provider's dialect layer
// emits `dual-mode` instead of a VLAN-context `untagged` entry; the device
// check accepts whichever form the running firmware uses, and the test
// output says which one it saw.
func TestAccInterfaceEthernet(t *testing.T) {
	env := Setup(t)
	env.requireScratchPort(t)
	before := env.Snapshot(t)
	port, vlan, vlan2 := env.ScratchPort, env.ScratchVLAN, env.ScratchVLAN2
	env.preflightScratchVLANs(t, before, vlan, vlan2)
	env.preflightScratchPort(t, before)
	env.preflightTrunkReconcile(t, before, vlan, vlan2)

	const rn = "icx_interface_ethernet.scratch"

	// membershipIs checks the port's membership on the device. For the
	// trunk-with-untagged shape the expected VLAN-context lists differ by
	// dialect, so the check derives them from the running config itself.
	membershipIs := func(portName string, tagged []int, untagged int) resource.TestCheckFunc {
		return env.deviceCheck(func(cfg *parser.RunningConfig) error {
			if iface := cfg.FindEthernetInterface(port); iface == nil || iface.PortName != portName {
				got := "<no stanza>"
				if iface != nil {
					got = strconv.Quote(iface.PortName)
				}
				return fmt.Errorf("port %s: port-name on device is %s, want %q", port, got, portName)
			}
			got := membershipOf(cfg, port)
			wantTagged := append([]int(nil), tagged...)
			var wantUntagged []int
			var wantDual *int
			dualMode := dialect.FromConfig(cfg).Has(dialect.CapDualMode) && untagged != 0 && len(tagged) > 0
			switch {
			case dualMode:
				// Pre-08.0.80: tagged in the untagged VLAN too, plus a
				// dual-mode line naming it.
				wantTagged = append(wantTagged, untagged)
				wantDual = &untagged
				t.Logf("port %s: dual-mode dialect in use (%s)", port, got)
			case untagged != 0:
				wantUntagged = []int{untagged}
			}
			sort.Ints(wantTagged)
			if !equalInts(got.tagged, wantTagged) || !equalInts(got.untagged, wantUntagged) ||
				(got.dualMode == nil) != (wantDual == nil) || (wantDual != nil && *got.dualMode != *wantDual) {
				want := vlanMembership{tagged: wantTagged, untagged: wantUntagged, dualMode: wantDual}
				return fmt.Errorf("port %s membership on device: %s; want %s", port, got, want)
			}
			return nil
		})
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: env.ProviderFactories(t),
		CheckDestroy: env.checkDestroy(before, func(cfg *parser.RunningConfig) error {
			if m := membershipOf(cfg, port); len(m.tagged)+len(m.untagged) > 0 || m.dualMode != nil {
				return fmt.Errorf("port %s still has VLAN membership after destroy: %s", port, m)
			}
			if iface := cfg.FindEthernetInterface(port); iface != nil {
				return fmt.Errorf("port %s still has an interface stanza after destroy (port-name %q)", port, iface.PortName)
			}
			for _, id := range []int{vlan, vlan2} {
				if cfg.FindVLAN(id) != nil {
					return fmt.Errorf("VLAN %d still exists after destroy", id)
				}
			}
			return nil
		}),
		Steps: []resource.TestStep{
			{
				// Access port.
				Config: env.ProviderConfig() + interfaceConfig(port, vlan, vlan2, "tfacc-access", nil, vlan),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rn, "id", port),
					resource.TestCheckResourceAttr(rn, "port_name", "tfacc-access"),
					resource.TestCheckResourceAttr(rn, "untagged_vlan", strconv.Itoa(vlan)),
					resource.TestCheckNoResourceAttr(rn, "tagged_vlans.#"),
					membershipIs("tfacc-access", nil, vlan),
				),
			},
			{
				// Tagged only: the untagged membership is removed and the
				// port returns to the default VLAN for untagged traffic.
				Config: env.ProviderConfig() + interfaceConfig(port, vlan, vlan2, "tfacc-trunk", []int{vlan}, 0),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rn, "port_name", "tfacc-trunk"),
					resource.TestCheckNoResourceAttr(rn, "untagged_vlan"),
					resource.TestCheckResourceAttr(rn, "tagged_vlans.#", "1"),
					resource.TestCheckTypeSetElemAttr(rn, "tagged_vlans.*", strconv.Itoa(vlan)),
					membershipIs("tfacc-trunk", []int{vlan}, 0),
				),
			},
			{
				// Trunk with an untagged VLAN: VLAN-context `untagged` on
				// 08.0.9x, `dual-mode` on 08.0.30.
				Config: env.ProviderConfig() + interfaceConfig(port, vlan, vlan2, "tfacc-trunk", []int{vlan}, vlan2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rn, "untagged_vlan", strconv.Itoa(vlan2)),
					resource.TestCheckResourceAttr(rn, "tagged_vlans.#", "1"),
					resource.TestCheckTypeSetElemAttr(rn, "tagged_vlans.*", strconv.Itoa(vlan)),
					membershipIs("tfacc-trunk", []int{vlan}, vlan2),
				),
			},
			{
				// Back to an access port in the first scratch VLAN: leaves
				// dual-mode (`no dual-mode`) on 08.0.30.
				Config: env.ProviderConfig() + interfaceConfig(port, vlan, vlan2, "tfacc-access", nil, vlan),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rn, "untagged_vlan", strconv.Itoa(vlan)),
					resource.TestCheckNoResourceAttr(rn, "tagged_vlans.#"),
					membershipIs("tfacc-access", nil, vlan),
				),
			},
			{
				ResourceName:      rn,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}
