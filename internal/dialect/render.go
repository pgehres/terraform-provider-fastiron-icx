package dialect

import "fmt"

// DHCPClientDisable renders the global command that makes the DHCP client
// disabled (disable = true) or enabled (disable = false).
//
//	08.0.9x, 08.0.30 router image: ip dhcp-client disable  / no ip dhcp-client disable
//	08.0.30 switch image:          no ip dhcp-client enable / ip dhcp-client enable
func (d Dialect) DHCPClientDisable(disable bool) string {
	if d.Has(CapLegacyDHCPClient) {
		if disable {
			return "no ip dhcp-client enable"
		}
		return "ip dhcp-client enable"
	}
	if disable {
		return "ip dhcp-client disable"
	}
	return "no ip dhcp-client disable"
}

// DualMode renders the interface-context command selecting vlanID as the
// untagged VLAN of a tagged (dual-mode) port. VLAN 1 is the bare form. Only
// meaningful when d.Has(CapDualMode).
func (d Dialect) DualMode(vlanID int) string {
	if vlanID == 1 {
		return "dual-mode"
	}
	return fmt.Sprintf("dual-mode %d", vlanID)
}

// NoDualMode renders the command returning a port to tagged-only.
func (d Dialect) NoDualMode() string { return "no dual-mode" }
