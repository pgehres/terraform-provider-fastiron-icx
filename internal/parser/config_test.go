package parser

import (
	"os"
	"testing"
)

func loadTestConfig(t *testing.T, filename string) string {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("failed to read %s: %v", filename, err)
	}
	return string(data)
}

func TestParseRunningConfig_7250_24(t *testing.T) {
	text := loadTestConfig(t, "../../testdata/config.7250-24.txt")
	config, err := ParseRunningConfig(text)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// Stack unit.
	if len(config.StackUnits) != 1 {
		t.Fatalf("expected 1 stack unit, got %d", len(config.StackUnits))
	}
	su := config.StackUnits[0]
	if su.ID != 1 {
		t.Errorf("stack unit ID: expected 1, got %d", su.ID)
	}
	if len(su.Modules) != 2 {
		t.Errorf("expected 2 modules, got %d", len(su.Modules))
	}
	if su.Modules[0].Type != "icx7250-24-port-management-module" {
		t.Errorf("module 1 type: %s", su.Modules[0].Type)
	}
	if len(su.StackPorts) != 2 {
		t.Errorf("expected 2 stack ports, got %d", len(su.StackPorts))
	}

	// VLANs: 1, 10, 20, 30, 40, 50, 60, 70 = 8.
	if len(config.VLANs) != 8 {
		t.Fatalf("expected 8 VLANs, got %d", len(config.VLANs))
	}

	// VLAN 1.
	v1 := config.FindVLAN(1)
	if v1 == nil {
		t.Fatal("VLAN 1 not found")
	}
	if v1.Name != "DEFAULT-VLAN" {
		t.Errorf("VLAN 1 name: %q", v1.Name)
	}
	if !v1.SpanningTree {
		t.Error("VLAN 1 should have spanning-tree")
	}

	// VLAN 20 (management).
	v20 := config.FindVLAN(20)
	if v20 == nil {
		t.Fatal("VLAN 20 not found")
	}
	if v20.Name != "management" {
		t.Errorf("VLAN 20 name: %q", v20.Name)
	}
	if v20.RouterInterface == nil || *v20.RouterInterface != 1 {
		t.Error("VLAN 20 should have router-interface ve 1")
	}
	if !v20.MulticastPassive {
		t.Error("VLAN 20 should have multicast passive")
	}
	if len(v20.UntaggedPorts) == 0 {
		t.Error("VLAN 20 should have untagged ports")
	}

	// VLAN 30 with multicast version 3.
	v30 := config.FindVLAN(30)
	if v30 == nil {
		t.Fatal("VLAN 30 not found")
	}
	if v30.MulticastVersion == nil || *v30.MulticastVersion != 3 {
		t.Error("VLAN 30 should have multicast version 3")
	}

	// VLAN 10 with router-interface ve 10.
	v10 := config.FindVLAN(10)
	if v10 == nil {
		t.Fatal("VLAN 10 not found")
	}
	if v10.RouterInterface == nil || *v10.RouterInterface != 10 {
		t.Error("VLAN 10 should have router-interface ve 10")
	}

	// Ethernet interfaces.
	eth127 := config.FindEthernetInterface("1/2/7")
	if eth127 == nil {
		t.Fatal("interface 1/2/7 not found")
	}
	if eth127.PortName != "trunk-to-sw2" {
		t.Errorf("1/2/7 port-name: %q", eth127.PortName)
	}
	if !eth127.SpanningTreePt2PtMac {
		t.Error("1/2/7 should have spanning-tree pt2pt-mac")
	}
	if !eth127.OpticalMonitorDisable {
		t.Error("1/2/7 should have no optical-monitor")
	}

	// VE interfaces.
	if len(config.VEInterfaces) != 2 {
		t.Fatalf("expected 2 VE interfaces, got %d", len(config.VEInterfaces))
	}
	ve1 := config.FindVEInterface(1)
	if ve1 == nil {
		t.Fatal("VE 1 not found")
	}
	if ve1.IPAddress != "10.0.20.2 255.255.255.0" {
		t.Errorf("VE 1 IP: %q", ve1.IPAddress)
	}
	ve10 := config.FindVEInterface(10)
	if ve10 == nil {
		t.Fatal("VE 10 not found")
	}
	if ve10.IPAddress != "10.0.10.2 255.255.255.0" {
		t.Errorf("VE 10 IP: %q", ve10.IPAddress)
	}

	// Users.
	if len(config.Users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(config.Users))
	}
	if config.Users[0].Username != "admin" {
		t.Errorf("username: %q", config.Users[0].Username)
	}

	// AAA.
	if config.AAA.WebServerAuth != "default local" {
		t.Errorf("AAA web-server: %q", config.AAA.WebServerAuth)
	}
	if config.AAA.LoginAuth != "default local" {
		t.Errorf("AAA login: %q", config.AAA.LoginAuth)
	}
	if !config.AAA.EnableAAAConsole {
		t.Error("AAA enable console should be true")
	}

	// Global settings.
	if !config.Global.GlobalSTP {
		t.Error("global-stp should be true")
	}
	if !config.Global.DHCPClientDisable {
		t.Error("dhcp-client disable should be true")
	}
	if !config.Global.OpticalMonitor {
		t.Error("optical-monitor should be true")
	}
	if !config.Global.OpticalMonitorNonRuckus {
		t.Error("optical-monitor non-ruckus should be true")
	}
	if !config.Global.ManagerDisable {
		t.Error("manager disable should be true")
	}
	if config.Global.ManagerPortList != "987" {
		t.Errorf("manager port-list: %q", config.Global.ManagerPortList)
	}
}

func TestParseRunningConfig_7150_c10zp(t *testing.T) {
	text := loadTestConfig(t, "../../testdata/config.7150-c10zp.txt")
	config, err := ParseRunningConfig(text)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// This model has no global-stp.
	if config.Global.GlobalSTP {
		t.Error("c10zp should not have global-stp")
	}

	// Uses global ip address instead of VE.
	if config.Global.IPAddress != "10.0.20.5 255.255.255.0" {
		t.Errorf("global IP: %q", config.Global.IPAddress)
	}
	if config.Global.DefaultGateway != "10.0.20.1" {
		t.Errorf("default-gateway: %q", config.Global.DefaultGateway)
	}

	// No VE interfaces.
	if len(config.VEInterfaces) != 0 {
		t.Errorf("expected 0 VE interfaces, got %d", len(config.VEInterfaces))
	}

	// Should have VLANs: 1, 20, 30, 40, 50, 70, 900, 901, 902 = 9.
	if len(config.VLANs) != 9 {
		t.Errorf("expected 9 VLANs, got %d", len(config.VLANs))
	}

	// Port name with quotes.
	eth131 := config.FindEthernetInterface("1/3/1")
	if eth131 == nil {
		t.Fatal("interface 1/3/1 not found")
	}
	if eth131.PortName != "trunk to breakout" {
		t.Errorf("1/3/1 port-name: %q (expected unquoted)", eth131.PortName)
	}
}

func TestParseRunningConfig_7150_24(t *testing.T) {
	text := loadTestConfig(t, "../../testdata/config.7150-24.txt")
	config, err := ParseRunningConfig(text)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// VLANs: 1, 10, 20, 30, 40, 50, 60, 70 = 8.
	if len(config.VLANs) != 8 {
		t.Errorf("expected 8 VLANs, got %d", len(config.VLANs))
	}

	// VLAN 10 has no name.
	v10 := config.FindVLAN(10)
	if v10 == nil {
		t.Fatal("VLAN 10 not found")
	}
	if v10.Name != "" {
		t.Errorf("VLAN 10 should have empty name, got %q", v10.Name)
	}

	// Check a tagged port range.
	if len(v10.TaggedPorts) != 11 {
		t.Errorf("VLAN 10 expected 11 tagged ports, got %d: %v", len(v10.TaggedPorts), v10.TaggedPorts)
	}

	// Manager registrar should be set (not disable).
	if !config.Global.ManagerRegistrar {
		t.Error("manager registrar should be true")
	}
	if config.Global.ManagerDisable {
		t.Error("manager disable should be false")
	}
}

func TestParseRunningConfig_7150_c12(t *testing.T) {
	text := loadTestConfig(t, "../../testdata/config.7150-c12.txt")
	config, err := ParseRunningConfig(text)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	// Optical monitor settings.
	if !config.Global.OpticalMonitor {
		t.Error("optical-monitor should be true")
	}
	if !config.Global.OpticalMonitorNonRuckus {
		t.Error("optical-monitor non-ruckus should be true")
	}

	// VLAN 900 with no spanning-tree.
	v900 := config.FindVLAN(900)
	if v900 == nil {
		t.Fatal("VLAN 900 not found")
	}
	if v900.SpanningTree {
		t.Error("VLAN 900 should not have spanning-tree")
	}
	if v900.Name != "ISP-primary" {
		t.Errorf("VLAN 900 name: %q", v900.Name)
	}

	// VE interface with router-interface from VLAN 20.
	v20 := config.FindVLAN(20)
	if v20 == nil {
		t.Fatal("VLAN 20 not found")
	}
	if v20.RouterInterface == nil || *v20.RouterInterface != 1 {
		t.Error("VLAN 20 should have router-interface ve 1")
	}
	if !v20.MulticastPassive {
		t.Error("VLAN 20 should have multicast passive")
	}
}

func TestParseRunningConfig_AllConfigs(t *testing.T) {
	files := []string{
		"../../testdata/config.7150-24.txt",
		"../../testdata/config.7150-c10zp.txt",
		"../../testdata/config.7150-c12.txt",
		"../../testdata/config.7250-24.txt",
	}

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			text := loadTestConfig(t, f)
			_, err := ParseRunningConfig(text)
			if err != nil {
				t.Errorf("parse error: %v", err)
			}
		})
	}
}
