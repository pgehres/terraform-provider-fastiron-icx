package parser

import (
	"testing"
)

// The 6430-C12 fixture is a sanitized capture from a real ICX 6430-C12 running
// FastIron 08.0.30uT311 (switch image). Notable dialect differences from the
// 08.0.95 fixtures: trunk untagged membership appears as `dual-mode <vlan>` on
// the interface (a command that no longer exists on 08.0.95), and the DHCP
// client is disabled via `no ip dhcp-client enable` rather than
// `ip dhcp-client disable`. The parser interprets both: dual-mode as
// EthernetInterface.DualModeVLAN, and the DHCP form as DHCPClientDisable.
func TestParseRunningConfig6430C12(t *testing.T) {
	text := loadTestConfig(t, "../../testdata/config.6430-c12.txt")
	config, err := ParseRunningConfig(text)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// Despite being a non-stackable model, the 6430 still wraps its module
	// lines in a `stack unit 1` stanza — the ordinary parsing path.
	if len(config.StackUnits) != 1 {
		t.Fatalf("expected 1 stack unit, got %d", len(config.StackUnits))
	}
	su := config.StackUnits[0]
	if su.ID != 1 {
		t.Errorf("stack unit ID: expected 1, got %d", su.ID)
	}
	if len(su.Modules) != 3 {
		t.Fatalf("expected 3 modules, got %d", len(su.Modules))
	}
	if su.Modules[0].ID != 1 || su.Modules[0].Type != "icx6430c-12-port-management-module" {
		t.Errorf("module 1: got ID %d type %q", su.Modules[0].ID, su.Modules[0].Type)
	}
	if su.Modules[2].ID != 3 || su.Modules[2].Type != "icx6430c-fiber-2port-2g-module" {
		t.Errorf("module 3: got ID %d type %q", su.Modules[2].ID, su.Modules[2].Type)
	}
	if len(su.StackPorts) != 0 {
		t.Errorf("expected no stack ports on a non-stackable model, got %v", su.StackPorts)
	}

	// Version string: T311-style image code, unlike the 7000s' T211/T213.
	if config.Global.Version != "08.0.30uT311" {
		t.Errorf("version: %q", config.Global.Version)
	}

	// Switch image: global IP + default gateway, no VE interfaces.
	if config.Global.IPAddress != "10.0.20.6 255.255.255.0" {
		t.Errorf("global ip address: %q", config.Global.IPAddress)
	}
	if config.Global.DefaultGateway != "10.0.20.9" {
		t.Errorf("default gateway: %q", config.Global.DefaultGateway)
	}
	// `no ip dhcp-client enable` is the 08.0.30 switch-image spelling of
	// `ip dhcp-client disable`.
	if !config.Global.DHCPClientDisable {
		t.Error("expected `no ip dhcp-client enable` to parse as DHCPClientDisable")
	}
	for _, l := range config.RawLines {
		if l == "no ip dhcp-client enable" {
			t.Error("`no ip dhcp-client enable` must not be left as a raw line")
		}
	}
	if len(config.VEInterfaces) != 0 {
		t.Errorf("expected no VE interfaces on switch image, got %d", len(config.VEInterfaces))
	}

	// VLANs: 1, 101, 102, 103, 107, 109, 110, 120, 200.
	if len(config.VLANs) != 9 {
		t.Fatalf("expected 9 VLANs, got %d", len(config.VLANs))
	}
	v109 := config.FindVLAN(109)
	if v109 == nil {
		t.Fatal("VLAN 109 not found")
	}
	if v109.Name != "legacy" {
		t.Errorf("VLAN 109 name: %q", v109.Name)
	}
	if len(v109.TaggedPorts) != 2 {
		t.Errorf("VLAN 109 tagged ports: expected 2 (1/2/1 to 1/2/2 expanded), got %v", v109.TaggedPorts)
	}
	if len(v109.UntaggedPorts) != 6 {
		t.Errorf("VLAN 109 untagged ports: expected 6 (1/1/1 to 1/1/6 expanded), got %v", v109.UntaggedPorts)
	}
	v103 := config.FindVLAN(103)
	if v103 == nil {
		t.Fatal("VLAN 103 not found")
	}
	if v103.Name != "" {
		t.Errorf("VLAN 103 should be unnamed, got %q", v103.Name)
	}

	// Users.
	if len(config.Users) != 1 || config.Users[0].Username != "admin" {
		t.Errorf("users: %+v", config.Users)
	}

	// Interfaces: port-name parsing, and `dual-mode` sub-lines must not break
	// interface parsing.
	eth := config.FindEthernetInterface("1/2/1")
	if eth == nil {
		t.Fatal("interface 1/2/1 not found")
	}
	if eth.PortName != "uplink-to-icx-7250" {
		t.Errorf("1/2/1 port-name: %q", eth.PortName)
	}
	// `dual-mode  102` (two spaces, as the switch prints it) is captured; the
	// VLAN context lists 1/2/1 only as *tagged* in 102.
	if eth.DualModeVLAN == nil || *eth.DualModeVLAN != 102 {
		t.Errorf("1/2/1 dual-mode: got %v, want 102", eth.DualModeVLAN)
	}
	if len(eth.RawLines) != 0 {
		t.Errorf("1/2/1 dual-mode must not leak into RawLines: %v", eth.RawLines)
	}
	v102 := config.FindVLAN(102)
	if v102 == nil || len(v102.TaggedPorts) != 1 || v102.TaggedPorts[0] != "1/2/1" || len(v102.UntaggedPorts) != 0 {
		t.Errorf("VLAN 102 membership: %+v", v102)
	}
	eth22 := config.FindEthernetInterface("1/2/2")
	if eth22 == nil || eth22.DualModeVLAN == nil || *eth22.DualModeVLAN != 109 {
		t.Errorf("1/2/2 dual-mode: got %+v, want 109", eth22)
	}
	// Access ports carry no dual-mode; their untagged membership is purely in
	// VLAN context (VLAN 109 untagged ethe 1/1/1 to 1/1/6).
	for _, p := range []string{"1/1/1", "1/1/6", "1/1/9"} {
		if e := config.FindEthernetInterface(p); e != nil && e.DualModeVLAN != nil {
			t.Errorf("%s: access port must not have dual-mode, got %d", p, *e.DualModeVLAN)
		}
	}
}

// The 6450-C12PD fixture is a sanitized capture from a real ICX 6450-C12PD
// running FastIron 08.0.30uT313 (router image) — the router-image counterpart
// to the 6430 switch-image fixture. It exercises multi-range tagged lines
// (`tagged ethe 1/1/1 to 1/1/2 ethe 1/1/5 ...`), `dual-mode`, `inline power`,
// and a VE interface on 08.0.30.
func TestParseRunningConfig6450C12PD(t *testing.T) {
	text := loadTestConfig(t, "../../testdata/config.6450-c12pd.txt")
	config, err := ParseRunningConfig(text)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if config.Global.Version != "08.0.30uT313" {
		t.Errorf("version: %q", config.Global.Version)
	}

	if len(config.StackUnits) != 1 {
		t.Fatalf("expected 1 stack unit, got %d", len(config.StackUnits))
	}
	if n := len(config.StackUnits[0].Modules); n != 3 {
		t.Errorf("expected 3 modules, got %d", n)
	}

	// Router image: VE interface with addr+mask, no global IP/gateway.
	if len(config.VEInterfaces) != 1 {
		t.Fatalf("expected 1 VE interface, got %d", len(config.VEInterfaces))
	}
	ve := config.VEInterfaces[0]
	if ve.ID != 1 {
		t.Errorf("VE ID: %d", ve.ID)
	}
	if ve.IPAddress != "10.0.20.7 255.255.255.0" {
		t.Errorf("VE IP: %q", ve.IPAddress)
	}
	if config.Global.IPAddress != "" || config.Global.DefaultGateway != "" {
		t.Errorf("router image must not set global IP/gateway, got %q / %q",
			config.Global.IPAddress, config.Global.DefaultGateway)
	}

	// VLAN 1 carries the router interface.
	v1 := config.FindVLAN(1)
	if v1 == nil {
		t.Fatal("VLAN 1 not found")
	}
	if v1.RouterInterface == nil || *v1.RouterInterface != 1 {
		t.Errorf("VLAN 1 router-interface: %v", v1.RouterInterface)
	}

	// Multi-range membership: tagged ethe 1/1/1 to 1/1/2 ethe 1/1/5 ethe 1/1/8
	// ethe 1/1/12 ethe 1/2/1 to 1/2/2 expands to 7 ports; untagged
	// ethe 1/1/3 to 1/1/4 ethe 1/1/6 expands to 3.
	if len(config.VLANs) != 9 {
		t.Fatalf("expected 9 VLANs, got %d", len(config.VLANs))
	}
	v102 := config.FindVLAN(102)
	if v102 == nil {
		t.Fatal("VLAN 102 not found")
	}
	if len(v102.TaggedPorts) != 7 {
		t.Errorf("VLAN 102 tagged ports: expected 7, got %v", v102.TaggedPorts)
	}
	if len(v102.UntaggedPorts) != 3 {
		t.Errorf("VLAN 102 untagged ports: expected 3, got %v", v102.UntaggedPorts)
	}

	// Interface parsing survives dual-mode and inline power sub-lines.
	eth := config.FindEthernetInterface("1/1/1")
	if eth == nil {
		t.Fatal("interface 1/1/1 not found")
	}
	if eth.PortName != "ap-1" {
		t.Errorf("1/1/1 port-name: %q", eth.PortName)
	}
	// Four dual-mode 102 ports (1/1/1, 1/1/2, 1/1/8, 1/1/12); VLAN 102 lists
	// all of them as tagged and none as untagged.
	for _, p := range []string{"1/1/1", "1/1/2", "1/1/8", "1/1/12"} {
		e := config.FindEthernetInterface(p)
		if e == nil || e.DualModeVLAN == nil || *e.DualModeVLAN != 102 {
			t.Errorf("%s dual-mode: got %+v, want 102", p, e)
			continue
		}
		tagged := false
		for _, tp := range v102.TaggedPorts {
			if tp == p {
				tagged = true
			}
		}
		if !tagged {
			t.Errorf("%s should be listed tagged in VLAN 102", p)
		}
		for _, up := range v102.UntaggedPorts {
			if up == p {
				t.Errorf("%s must not be listed untagged in VLAN 102", p)
			}
		}
	}
	// `inline power` remains a raw line, `dual-mode` does not.
	if len(eth.RawLines) != 1 || eth.RawLines[0] != "inline power" {
		t.Errorf("1/1/1 RawLines: %v, want [inline power]", eth.RawLines)
	}
	// 1/1/5 (trunk-1) is tagged everywhere with no dual-mode: a pure tagged trunk.
	if e := config.FindEthernetInterface("1/1/5"); e == nil || e.DualModeVLAN != nil {
		t.Errorf("1/1/5 should exist without dual-mode, got %+v", e)
	}

	// Unlike the 6430's `no ip dhcp-client enable`, the 6450 router image uses
	// the 08.0.95-style `ip dhcp-client disable`, which the parser interprets.
	if !config.Global.DHCPClientDisable {
		t.Error("expected DHCPClientDisable to be parsed true")
	}

	if len(config.Users) != 1 || config.Users[0].Username != "admin" {
		t.Errorf("users: %+v", config.Users)
	}
}

// Bare top-level module lines (no `stack unit` wrapper) do not occur in the
// real 6430-C12 capture, but very old FastIron output has been reported
// without the wrapper — keep the tolerance and its tests as robustness.
func TestParseBareModuleLines(t *testing.T) {
	t.Run("bare modules synthesize unit 1", func(t *testing.T) {
		config, err := ParseRunningConfig("module 1 icx6430-c12-port-management-module\nmodule 2 icx6430-sfp-2port-2g-module\n")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if len(config.StackUnits) != 1 {
			t.Fatalf("expected 1 stack unit, got %d", len(config.StackUnits))
		}
		if config.StackUnits[0].ID != 1 {
			t.Errorf("unit ID: %d", config.StackUnits[0].ID)
		}
		if len(config.StackUnits[0].Modules) != 2 {
			t.Errorf("expected 2 modules, got %d", len(config.StackUnits[0].Modules))
		}
	})

	t.Run("bare module merges into existing stack unit 1", func(t *testing.T) {
		text := "stack unit 1\n  module 1 icx7150-24-port-management-module\n!\nmodule 2 icx7150-2-sfp-plus-port-20g-module\n"
		config, err := ParseRunningConfig(text)
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if len(config.StackUnits) != 1 {
			t.Fatalf("expected 1 stack unit, got %d", len(config.StackUnits))
		}
		if len(config.StackUnits[0].Modules) != 2 {
			t.Errorf("expected 2 modules, got %d", len(config.StackUnits[0].Modules))
		}
	})

	t.Run("stack unit stanza parsing is unchanged", func(t *testing.T) {
		text := loadTestConfig(t, "../../testdata/config.7150-c10zp.txt")
		config, err := ParseRunningConfig(text)
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		if len(config.StackUnits) != 1 {
			t.Fatalf("expected 1 stack unit, got %d", len(config.StackUnits))
		}
		if len(config.StackUnits[0].Modules) != 3 {
			t.Errorf("expected 3 modules, got %d", len(config.StackUnits[0].Modules))
		}
	})
}
