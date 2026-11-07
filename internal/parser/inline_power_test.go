package parser

import (
	"os"
	"testing"
)

// The fixture is `show inline power` from an ICX 7450-48P on FastIron
// 09.0.10k: 48 PoE ports, all admin On, none powering a device.
func TestParseInlinePowerFixture(t *testing.T) {
	b, err := os.ReadFile("../../testdata/inline-power.7450-48p.txt")
	if err != nil {
		t.Fatal(err)
	}
	rows := ParseInlinePower(string(b))
	if len(rows) != 48 {
		t.Fatalf("got %d rows, want 48", len(rows))
	}
	for _, p := range []string{"1/1/1", "1/1/9", "1/1/48"} {
		r, ok := rows[p]
		if !ok {
			t.Fatalf("no row for %s", p)
		}
		if !r.AdminOn || r.OperOn || r.ConsumedMW != 0 || r.AllocatedMW != 0 {
			t.Errorf("%s = %+v, want admin On, oper Off, 0 mW", p, r)
		}
	}
}

// Rows captured on 09.0.10k after `no inline power` (1/1/46) and from a
// re-enabled port (allocation without a device), plus a powered port in the
// same layout, and a port with a non-PoE device behind a PoE injector
// (Oper State "Non-PD").
func TestParseInlinePowerRows(t *testing.T) {
	out := " Port\tAdmin \tOper    ---Power(mWatts)---  PD Type  PD Class     Pri  Fault/\n" +
		"     \tState \tState   Consumed  Allocated                             Error\n" +
		"-----------------------------------------------------------------------------\n" +
		" 1/1/46\tOff\tOff            0          0  n/a      n/a            3  n/a\n" +
		" 1/1/47\tOn \tOff            0      30000  n/a      n/a            3  n/a\n" +
		"  1/1/1\tOn \tOn          9350      30000  IEEE PD  Class 4        2  n/a\r\n" +
		"  1/1/2\tOn \tNon-PD         0          0  n/a      n/a            1  n/a\n"
	rows := ParseInlinePower(out)
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(rows), rows)
	}
	if r := rows["1/1/46"]; r.AdminOn || r.OperOn {
		t.Errorf("1/1/46 = %+v, want admin Off", r)
	}
	if r := rows["1/1/47"]; !r.AdminOn || r.AllocatedMW != 30000 {
		t.Errorf("1/1/47 = %+v, want admin On, 30000 mW allocated", r)
	}
	if r := rows["1/1/1"]; !r.OperOn || r.ConsumedMW != 9350 {
		t.Errorf("1/1/1 = %+v, want oper On, 9350 mW consumed", r)
	}
	if r, ok := rows["1/1/2"]; !ok || !r.AdminOn || r.OperOn {
		t.Errorf("1/1/2 = %+v (found %v), want admin On, oper not On", r, ok)
	}
}

func TestParseInlinePowerNoRows(t *testing.T) {
	if rows := ParseInlinePower("Invalid input -> show inline power\nType ? for a list\n"); len(rows) != 0 {
		t.Errorf("got %+v, want no rows", rows)
	}
}

// Interface stanzas as `show running-config` printed them on 09.0.10k.
func TestParseEthernetPoEOptions(t *testing.T) {
	cfg := "ver 09.0.10k_cd3T211\n!\n" +
		"interface ethernet 1/1/45\n inline power power-limit 25000\n!\n" +
		"interface ethernet 1/1/46\n no inline power\n!\n" +
		"interface ethernet 1/1/47\n inline power priority 2\n!\n" +
		"interface ethernet 1/1/48\n inline power\n!\nend\n"
	rc, err := ParseRunningConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]PoEConfig{}
	for _, i := range rc.EthernetInterfaces {
		got[i.Port] = i.PoE
	}
	if p := got["1/1/45"]; p.PowerLimit == nil || *p.PowerLimit != 25000 || p.Priority != nil || p.Disabled {
		t.Errorf("1/1/45 = %+v, want power limit 25000 only", p)
	}
	if p := got["1/1/46"]; !p.Disabled || p.PowerLimit != nil || p.Priority != nil {
		t.Errorf("1/1/46 = %+v, want disabled only", p)
	}
	if p := got["1/1/47"]; p.Priority == nil || *p.Priority != 2 || p.PowerLimit != nil {
		t.Errorf("1/1/47 = %+v, want priority 2 only", p)
	}
	if p := got["1/1/48"]; p.Disabled || p.PowerLimit != nil || p.Priority != nil {
		t.Errorf("1/1/48 = %+v, want no options", p)
	}
}
