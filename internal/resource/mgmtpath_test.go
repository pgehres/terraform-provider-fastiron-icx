package resource

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// Router-image `show arp` (numbered rows, Status column) and switch-image
// output (no numbering) both resolve; a stray `*` marker and LAG ids do too.
const arpRouter = `Total number of ARP entries: 4
Entries in default routing instance:
   No.   IP Address      MAC Address     Type    Age Port   Status
   1     10.0.20.1       0000.5e00.0101  Dynamic 0   1/2/1  Valid
   2     10.0.20.50      3c22.fb12.3456  Dynamic 3   1/1/5* Valid
   3     10.0.30.7       0050.5689.0001  Dynamic 1   lg1    Valid
   4     10.0.20.99      0000.0000.0000  Pending 0          Invalid
`

const arpSwitch = `Total Arp Entries : 2
IP Address       MAC Address        Type    Age   Port
10.0.20.9        609c.9f0a.5b60     Dynamic 2     1/2/1
10.0.20.44       b827.eb11.2233     Dynamic 0     1/1/3
`

func TestArpPorts(t *testing.T) {
	got := arpPorts(arpRouter)
	want := map[string]string{"10.0.20.1": "1/2/1", "10.0.20.50": "1/1/5", "10.0.30.7": "lg1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("router arp = %v, want %v", got, want)
	}
	got = arpPorts(arpSwitch)
	want = map[string]string{"10.0.20.9": "1/2/1", "10.0.20.44": "1/1/3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("switch arp = %v, want %v", got, want)
	}
	if len(arpPorts("Invalid input -> arp\nType ? for a list")) != 0 {
		t.Error("garbage must resolve nothing")
	}
}

// Next hops come from the fixtures' own config: the switch-image captures
// have `ip default-gateway`, the 6450 a default `ip route`, and the 7250 a
// default route too.
func TestManagementNextHops_Fixtures(t *testing.T) {
	for _, tt := range []struct {
		fixture string
		want    []string
	}{
		{"7150-c10zp", []string{"10.0.20.1"}},
		{"6430-c12", []string{"10.0.20.9"}},
		{"6450-c12pd", []string{"10.0.20.1"}},
	} {
		cfg, err := parser.ParseRunningConfig(loadFixture(t, tt.fixture))
		if err != nil {
			t.Fatal(err)
		}
		if got := managementNextHops(cfg); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: next hops = %v, want %v", tt.fixture, got, tt.want)
		}
	}
	cfg, _ := parser.ParseRunningConfig("ip route 0.0.0.0 0.0.0.0 192.0.2.1\nip route 0.0.0.0/0 192.0.2.1\nip route 10.0.0.0/8 192.0.2.2\n")
	if got := managementNextHops(cfg); !reflect.DeepEqual(got, []string{"192.0.2.1"}) {
		t.Errorf("long-form route: %v", got)
	}
}

func TestCheckManagementPathDisable(t *testing.T) {
	cfg, err := parser.ParseRunningConfig(loadFixture(t, "6430-c12")) // gateway 10.0.20.9
	if err != nil {
		t.Fatal(err)
	}
	t.Run("refuses the uplink", func(t *testing.T) {
		mock := sshclient.NewMockClient("")
		mock.Responses["show arp"] = arpSwitch
		var diags diag.Diagnostics
		if checkManagementPathDisable(mock, cfg, "1/2/1", &diags) || !diags.HasError() {
			t.Fatalf("expected refusal, got %v", diags)
		}
		if got := mock.Commands; !reflect.DeepEqual(got, []string{"show arp"}) {
			t.Errorf("commands = %v, want only show arp", got)
		}
	})
	t.Run("allows another port silently", func(t *testing.T) {
		mock := sshclient.NewMockClient("")
		mock.Responses["show arp"] = arpSwitch
		var diags diag.Diagnostics
		if !checkManagementPathDisable(mock, cfg, "1/1/3", &diags) || len(diags) != 0 {
			t.Fatalf("expected clean pass, got %v", diags)
		}
	})
	t.Run("warns when the hop is not in arp", func(t *testing.T) {
		mock := sshclient.NewMockClient("")
		mock.Responses["show arp"] = arpRouter // no 10.0.20.9
		var diags diag.Diagnostics
		if !checkManagementPathDisable(mock, cfg, "1/2/1", &diags) || diags.HasError() || diags.WarningsCount() != 1 {
			t.Fatalf("expected pass with one warning, got %v", diags)
		}
	})
	t.Run("warns without a next hop", func(t *testing.T) {
		noHop, _ := parser.ParseRunningConfig("ver 08.0.95mT213\nglobal-stp\n")
		mock := sshclient.NewMockClient("")
		var diags diag.Diagnostics
		if !checkManagementPathDisable(mock, noHop, "1/1/1", &diags) || diags.WarningsCount() != 1 {
			t.Fatalf("expected pass with one warning, got %v", diags)
		}
		if len(mock.Commands) != 0 {
			t.Errorf("no next hop: show arp must not be sent, got %v", mock.Commands)
		}
	})
}
