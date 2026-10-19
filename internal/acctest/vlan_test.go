package acctest

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

func vlanConfig(id int, name string, spanningTree bool) string {
	return fmt.Sprintf(`
resource "icx_vlan" "scratch" {
  vlan_id       = %d
  name          = %q
  spanning_tree = %t
}
`, id, name, spanningTree)
}

// TestAccVLAN creates the scratch VLAN with a name and spanning tree, renames
// it and turns spanning tree off in place, imports it, and destroys it. The
// device must be back to the pre-test running configuration afterwards.
func TestAccVLAN(t *testing.T) {
	env := Setup(t)
	before := env.Snapshot(t)
	id := env.ScratchVLAN
	env.preflightScratchVLANs(t, before, id)
	env.preflightTrunkReconcile(t, before, id)

	const rn = "icx_vlan.scratch"
	vlanOnDevice := func(name string, stp bool) resource.TestCheckFunc {
		return env.deviceCheck(func(cfg *parser.RunningConfig) error {
			v := cfg.FindVLAN(id)
			if v == nil {
				return fmt.Errorf("VLAN %d is not on the device", id)
			}
			if v.Name != name || v.SpanningTree != stp {
				return fmt.Errorf("VLAN %d on device: name %q spanning-tree %t, want name %q spanning-tree %t",
					id, v.Name, v.SpanningTree, name, stp)
			}
			return nil
		})
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: env.ProviderFactories(t),
		CheckDestroy: env.checkDestroy(before, func(cfg *parser.RunningConfig) error {
			if cfg.FindVLAN(id) != nil {
				return fmt.Errorf("VLAN %d still exists on the device after destroy", id)
			}
			return nil
		}),
		Steps: []resource.TestStep{
			{
				Config: env.ProviderConfig() + vlanConfig(id, "tfacc-scratch", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rn, "id", strconv.Itoa(id)),
					resource.TestCheckResourceAttr(rn, "vlan_id", strconv.Itoa(id)),
					resource.TestCheckResourceAttr(rn, "name", "tfacc-scratch"),
					resource.TestCheckResourceAttr(rn, "spanning_tree", "true"),
					vlanOnDevice("tfacc-scratch", true),
				),
			},
			{
				// In-place update: FastIron renames through the VLAN header
				// and spanning tree is removed with `no spanning-tree 802-1w`.
				Config: env.ProviderConfig() + vlanConfig(id, "tfacc-renamed", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rn, "name", "tfacc-renamed"),
					resource.TestCheckResourceAttr(rn, "spanning_tree", "false"),
					vlanOnDevice("tfacc-renamed", false),
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
