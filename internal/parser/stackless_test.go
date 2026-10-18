package parser

import (
	"testing"
)

// The 6430-C12 fixture is SYNTHETIC — hand-written to model FastIron 08.0.30
// switch-image output (stackless, bare top-level module lines) and marked as
// such in the file itself. It must be replaced with a real capture once an
// ICX 6430-C12 is wired into the lab.
func TestParseRunningConfig_6430_C12_Synthetic(t *testing.T) {
	text := loadTestConfig(t, "../../testdata/config.6430-c12.txt")
	config, err := ParseRunningConfig(text)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// Bare module lines must synthesize stack unit 1.
	if len(config.StackUnits) != 1 {
		t.Fatalf("expected 1 synthesized stack unit, got %d", len(config.StackUnits))
	}
	su := config.StackUnits[0]
	if su.ID != 1 {
		t.Errorf("stack unit ID: expected 1, got %d", su.ID)
	}
	if len(su.Modules) != 2 {
		t.Fatalf("expected 2 modules, got %d", len(su.Modules))
	}
	if su.Modules[0].ID != 1 || su.Modules[0].Type != "icx6430-c12-port-management-module" {
		t.Errorf("module 1: got ID %d type %q", su.Modules[0].ID, su.Modules[0].Type)
	}
	if su.Modules[1].ID != 2 || su.Modules[1].Type != "icx6430-sfp-2port-2g-module" {
		t.Errorf("module 2: got ID %d type %q", su.Modules[1].ID, su.Modules[1].Type)
	}
	if len(su.StackPorts) != 0 {
		t.Errorf("expected no stack ports on a stackless model, got %v", su.StackPorts)
	}

	// Version.
	if config.Global.Version != "08.0.30dT311" {
		t.Errorf("version: %q", config.Global.Version)
	}

	// Switch image: global IP + default gateway, no VE interfaces.
	if config.Global.IPAddress != "10.0.20.8 255.255.255.0" {
		t.Errorf("global ip address: %q", config.Global.IPAddress)
	}
	if config.Global.DefaultGateway != "10.0.20.1" {
		t.Errorf("default gateway: %q", config.Global.DefaultGateway)
	}
	if len(config.VEInterfaces) != 0 {
		t.Errorf("expected no VE interfaces on switch image, got %d", len(config.VEInterfaces))
	}

	// VLANs: 1, 20, 30, 70.
	if len(config.VLANs) != 4 {
		t.Fatalf("expected 4 VLANs, got %d", len(config.VLANs))
	}
	v20 := config.FindVLAN(20)
	if v20 == nil {
		t.Fatal("VLAN 20 not found")
	}
	if v20.Name != "management" {
		t.Errorf("VLAN 20 name: %q", v20.Name)
	}
	if len(v20.TaggedPorts) != 2 {
		t.Errorf("VLAN 20 tagged ports: expected 2, got %v", v20.TaggedPorts)
	}
	if len(v20.UntaggedPorts) != 4 {
		t.Errorf("VLAN 20 untagged ports: expected 4, got %v", v20.UntaggedPorts)
	}

	// Users.
	if len(config.Users) != 1 || config.Users[0].Username != "admin" {
		t.Errorf("users: %+v", config.Users)
	}

	// Interfaces.
	eth := config.FindEthernetInterface("1/2/1")
	if eth == nil {
		t.Fatal("interface 1/2/1 not found")
	}
	if eth.PortName != "uplink to core" {
		t.Errorf("1/2/1 port-name: %q", eth.PortName)
	}
}

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
