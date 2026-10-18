package parser

import (
	"testing"
)

// The 6430-C12 fixture is a sanitized capture from a real ICX 6430-C12 running
// FastIron 08.0.30uT311 (switch image). Notable dialect differences from the
// 08.0.95 fixtures: trunk untagged membership appears as `dual-mode <vlan>` on
// the interface (a command that no longer exists on 08.0.95), and the DHCP
// client is disabled via `no ip dhcp-client enable` rather than
// `ip dhcp-client disable`. The parser must tolerate both without error; it is
// not yet expected to interpret them.
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
