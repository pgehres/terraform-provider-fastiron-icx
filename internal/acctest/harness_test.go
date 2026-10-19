package acctest

import (
	"strings"
	"testing"

	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

// These run without a switch and cover the harness's own logic: the
// configuration comparison every acceptance test ends with, and the HCL it
// renders.

func TestCompareConfigs(t *testing.T) {
	base := "Current configuration:\n!\nver 08.0.95mT213\n!\nvlan 1 name DEFAULT-VLAN by port\n!\nhostname sw1\n!\nend\n"
	cases := []struct {
		name          string
		before, after string
		wantErr       []string
	}{
		{"identical", base, base, nil},
		{"whitespace and blank lines ignored", base, strings.ReplaceAll(base, "\n!\n", "\n!   \n\n"), nil},
		{"reordered lines are equal", base, "Current configuration:\n!\nhostname sw1\n!\nver 08.0.95mT213\n!\nvlan 1 name DEFAULT-VLAN by port\n!\nend\n", nil},
		{"line left behind", base, strings.Replace(base, "hostname sw1\n", "hostname sw1\nvlan 3999 name tfacc-scratch by port\n", 1),
			[]string{"left behind", "vlan 3999 name tfacc-scratch by port (x1)"}},
		{"line missing", base, strings.Replace(base, "hostname sw1\n", "", 1),
			[]string{"missing", "hostname sw1 (x1)"}},
		{"changed line reports both", base, strings.Replace(base, "hostname sw1", "hostname sw1-tfacc", 1),
			[]string{"hostname sw1 (x1)", "hostname sw1-tfacc (x1)"}},
		{"count difference", base, strings.Replace(base, "!\nend", "!\n!\nend", 1), []string{"! (x1)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := compareConfigs(tc.before, tc.after)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestAddressMaskToCIDR(t *testing.T) {
	cases := map[string]string{
		"10.0.20.5 255.255.255.0":          "10.0.20.5/24",
		"192.168.2.240 255.255.255.0":      "192.168.2.240/24",
		"172.16.0.9 255.255.0.0 some-flag": "172.16.0.9/16",
		"10.1.1.1 255.255.255.252":         "10.1.1.1/30",
	}
	for in, want := range cases {
		got, err := addressMaskToCIDR(in)
		if err != nil || got != want {
			t.Errorf("addressMaskToCIDR(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "10.0.20.5", "10.0.20.5 255.0.255.0", "fe80::1 ffff::"} {
		if _, err := addressMaskToCIDR(bad); err == nil {
			t.Errorf("addressMaskToCIDR(%q) succeeded, want error", bad)
		}
	}
}

// systemConfig must mirror the device so that no setting other than the
// hostname is planned as a change.
func TestSystemConfigMirrorsDevice(t *testing.T) {
	g := parser.GlobalSettings{
		TelnetServer:      false,
		TelnetServerSet:   true,
		DHCPClientDisable: true,
		OpticalMonitor:    true,
		ManagerPortList:   "987",
	}
	got := systemConfig(g, "sw6430-tfacc")
	for _, want := range []string{
		`hostname                   = "sw6430-tfacc"`,
		"telnet_server              = false",
		"dhcp_client_disable        = true",
		"optical_monitor            = true",
		"optical_monitor_non_ruckus = false",
		"global_stp                 = false",
		"manager_registrar          = false",
		"manager_disable            = false",
		`manager_port_list          = "987"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config lacks %q:\n%s", want, got)
		}
	}

	// Telnet not explicitly configured means the device default (enabled).
	got = systemConfig(parser.GlobalSettings{}, "x")
	if !strings.Contains(got, "telnet_server              = true") {
		t.Errorf("unset telnet must render as true:\n%s", got)
	}
	if strings.Contains(got, "manager_port_list") {
		t.Errorf("empty manager_port_list must not be rendered:\n%s", got)
	}
}

// The interface configuration must reference the VLAN resources (so destroy
// order is membership first, VLANs last) and omit attributes that are unset
// rather than rendering null-equivalents.
func TestInterfaceConfigShapes(t *testing.T) {
	access := interfaceConfig("1/1/11", 3999, 3998, "tfacc-access", nil, 3999)
	if !strings.Contains(access, "untagged_vlan = icx_vlan.scratch.vlan_id") || strings.Contains(access, "tagged_vlans") {
		t.Errorf("access shape:\n%s", access)
	}
	trunk := interfaceConfig("1/1/11", 3999, 3998, "tfacc-trunk", []int{3999}, 3998)
	if !strings.Contains(trunk, "tagged_vlans = [icx_vlan.scratch.vlan_id]") ||
		!strings.Contains(trunk, "untagged_vlan = icx_vlan.scratch2.vlan_id") {
		t.Errorf("trunk shape:\n%s", trunk)
	}
	taggedOnly := interfaceConfig("1/1/11", 3999, 3998, "tfacc-trunk", []int{3999}, 0)
	if strings.Contains(taggedOnly, "untagged_vlan") {
		t.Errorf("tagged-only shape must not set untagged_vlan:\n%s", taggedOnly)
	}
	for _, c := range []string{access, trunk, taggedOnly} {
		if !strings.Contains(c, `port      = "1/1/11"`) || !strings.Contains(c, "resource \"icx_vlan\" \"scratch2\"") {
			t.Errorf("missing port or scratch2 VLAN:\n%s", c)
		}
	}
}

func TestManagementIPConfigDNSList(t *testing.T) {
	if got := managementIPConfig("10.0.20.5/24", nil); !strings.Contains(got, "dns_servers = []") {
		t.Errorf("empty list must render as []:\n%s", got)
	}
	got := managementIPConfig("10.0.20.5/24", []string{"10.0.20.1", testDNSServer})
	if !strings.Contains(got, `dns_servers = ["10.0.20.1", "192.0.2.53"]`) || !strings.Contains(got, `data "icx_stack_unit" "this" {}`) {
		t.Errorf("rendered:\n%s", got)
	}
}

func TestProviderConfigLegacySSH(t *testing.T) {
	e := &Env{}
	if got := e.ProviderConfig(); strings.Contains(got, "legacy") {
		t.Errorf("legacy_ssh_algorithms rendered when off:\n%s", got)
	}
	e.LegacySSH = true
	if got := e.ProviderConfig(); !strings.Contains(got, "legacy_ssh_algorithms = true") {
		t.Errorf("legacy_ssh_algorithms missing when on:\n%s", got)
	}
}
