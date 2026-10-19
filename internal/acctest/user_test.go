package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

func userConfig(password string) string {
	return fmt.Sprintf(`
resource "icx_user" "scratch" {
  username = %q
  password = %q
}
`, scratchUser, password)
}

// TestAccUser creates the scratch user, changes its password, imports it
// and deletes it. The login user is never involved: Setup refuses to run
// when it is the scratch user name, and icx_user itself refuses to delete
// the account the provider is authenticated as.
func TestAccUser(t *testing.T) {
	env := Setup(t)
	before := env.Snapshot(t)
	env.preflightScratchUser(t, before)

	const rn = "icx_user.scratch"
	userOnDevice := func(want bool) resource.TestCheckFunc {
		return env.deviceCheck(func(cfg *parser.RunningConfig) error {
			found := false
			for _, u := range cfg.Users {
				if u.Username == scratchUser {
					found = true
				}
			}
			if found != want {
				return fmt.Errorf("user %q on device: %t, want %t", scratchUser, found, want)
			}
			return nil
		})
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: env.ProviderFactories(t),
		CheckDestroy: env.checkDestroy(before, func(cfg *parser.RunningConfig) error {
			for _, u := range cfg.Users {
				if u.Username == scratchUser {
					return fmt.Errorf("user %q still exists after destroy", scratchUser)
				}
			}
			return nil
		}),
		Steps: []resource.TestStep{
			{
				Config: env.ProviderConfig() + userConfig("tfaccPass1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rn, "id", scratchUser),
					resource.TestCheckResourceAttr(rn, "username", scratchUser),
					userOnDevice(true),
				),
			},
			{
				// Password change: re-issues `username ... password ...`.
				Config: env.ProviderConfig() + userConfig("tfaccPass2"),
				Check:  userOnDevice(true),
			},
			{
				ResourceName:      rn,
				ImportState:       true,
				ImportStateVerify: true,
				// The password cannot be read from the switch (CLAUDE.md
				// gotcha #2).
				ImportStateVerifyIgnore: []string{"password"},
			},
		},
	})
}
