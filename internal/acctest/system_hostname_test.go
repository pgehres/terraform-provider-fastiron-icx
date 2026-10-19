package acctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

// systemConfig renders icx_system with hostname plus every other attribute
// set to what the device has right now. icx_system is a singleton that owns
// all of those settings: left to their defaults, the plan would show the
// device's real telnet / DHCP-client / optical-monitor / manager values as
// drift and the next apply would "fix" them — on an ICX 6430 that means
// re-enabling the DHCP client on a statically addressed switch. Rendering
// the device's values makes every command the provider sends for them an
// idempotent re-issue.
func systemConfig(g parser.GlobalSettings, hostname string) string {
	telnet := true
	if g.TelnetServerSet {
		telnet = g.TelnetServer
	}
	var b strings.Builder
	b.WriteString("resource \"icx_system\" \"this\" {\n")
	fmt.Fprintf(&b, "  hostname                   = %q\n", hostname)
	fmt.Fprintf(&b, "  global_stp                 = %t\n", g.GlobalSTP)
	fmt.Fprintf(&b, "  telnet_server              = %t\n", telnet)
	fmt.Fprintf(&b, "  dhcp_client_disable        = %t\n", g.DHCPClientDisable)
	fmt.Fprintf(&b, "  optical_monitor            = %t\n", g.OpticalMonitor)
	fmt.Fprintf(&b, "  optical_monitor_non_ruckus = %t\n", g.OpticalMonitorNonRuckus)
	fmt.Fprintf(&b, "  manager_registrar          = %t\n", g.ManagerRegistrar)
	fmt.Fprintf(&b, "  manager_disable            = %t\n", g.ManagerDisable)
	if g.ManagerPortList != "" {
		fmt.Fprintf(&b, "  manager_port_list          = %q\n", g.ManagerPortList)
	}
	b.WriteString("}\n")
	return b.String()
}

// systemForgetConfig forgets icx_system without destroying it. Destroying
// the singleton resets every non-default setting it owns (never the
// hostname), which would change device settings this test has no business
// touching; a `removed` block drops it from state instead, so the final
// `terraform destroy` has nothing to do.
const systemForgetConfig = `
removed {
  from = icx_system.this
  lifecycle {
    destroy = false
  }
}
`

// restoreHostname puts the device's hostname back to original ("" meaning
// the factory default, via `no hostname`) over the harness's own session,
// and saves. It is a no-op when the device already has that name. The
// commands go through the prompt re-detecting path, as the provider's do.
func (e *Env) restoreHostname(t *testing.T, original string) {
	t.Helper()
	s, err := e.snapshot()
	if err != nil {
		t.Errorf("restore hostname: %v", err)
		return
	}
	if s.Config.Global.Hostname == original {
		return
	}
	cmd := "no hostname"
	if original != "" {
		cmd = "hostname " + original
	}
	t.Logf("restoring hostname on %s: %q -> %q (`%s`)", e.Host, s.Config.Global.Hostname, original, cmd)
	c, err := e.connect()
	if err != nil {
		t.Errorf("restore hostname: connect: %v", err)
		return
	}
	defer func() { _ = c.Close() }()
	if err := c.ExecutePromptChanging(cmd); err != nil {
		t.Errorf("restore hostname: %v", err)
		return
	}
	if err := c.WriteMemory(); err != nil {
		t.Errorf("restore hostname: write memory: %v", err)
	}
}

// TestAccSystemHostname renames the device to "<original>-tfacc" and back
// through icx_system, which exercises the SSH client's live prompt
// re-detection: the provider must keep talking to the switch after its own
// `hostname` command changed the prompt, and every later provider session
// must detect the new prompt at connect time.
//
// When the device runs its factory-default name (no `hostname` line) the
// provider cannot restore it — icx_system never sends `no hostname` — so the
// revert step is skipped and the harness sends `no hostname` itself.
// Either way the original name is restored in t.Cleanup even when the test
// fails part-way.
func TestAccSystemHostname(t *testing.T) {
	env := Setup(t)
	before := env.Snapshot(t)
	original := before.Config.Global.Hostname
	global := before.Config.Global

	renamed := "tfacc-hostname"
	if original != "" {
		renamed = original + "-tfacc"
	}

	// Registered before ProviderFactories so it runs after the provider's
	// last session is closed (cleanups run last-in, first-out).
	t.Cleanup(func() { env.restoreHostname(t, original) })
	factories := env.ProviderFactories(t)

	const rn = "icx_system.this"
	hostnameOnDevice := func(want string) resource.TestCheckFunc {
		return env.deviceCheck(func(cfg *parser.RunningConfig) error {
			if cfg.Global.Hostname != want {
				return fmt.Errorf("hostname on device is %q, want %q", cfg.Global.Hostname, want)
			}
			return nil
		})
	}
	forgotten := func(s *terraform.State) error {
		if _, ok := s.RootModule().Resources[rn]; ok {
			return fmt.Errorf("%s is still in state after the removed block was applied", rn)
		}
		return nil
	}

	steps := []resource.TestStep{
		{
			Config: env.ProviderConfig() + systemConfig(global, renamed),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(rn, "id", "system"),
				resource.TestCheckResourceAttr(rn, "hostname", renamed),
				hostnameOnDevice(renamed),
			),
		},
	}
	if original != "" {
		steps = append(steps, resource.TestStep{
			Config: env.ProviderConfig() + systemConfig(global, original),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(rn, "hostname", original),
				hostnameOnDevice(original),
			),
		})
	} else {
		t.Logf("device runs its default hostname; the provider cannot revert to it, the harness will send `no hostname` after the test")
	}
	steps = append(steps, resource.TestStep{
		Config: env.ProviderConfig() + systemForgetConfig,
		Check:  forgotten,
	})

	resource.Test(t, resource.TestCase{
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			// `removed` blocks.
			tfversion.SkipBelow(tfversion.Version1_7_0),
		},
		ProtoV6ProviderFactories: factories,
		CheckDestroy: func(s *terraform.State) error {
			// Nothing is destroyed; put the default name back where the
			// provider could not, then require the pre-test configuration.
			env.restoreHostname(t, original)
			return env.checkDestroy(before, nil)(s)
		},
		Steps: steps,
	})
}
