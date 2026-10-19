package parser

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// EthernetInterface represents a parsed ethernet interface configuration.
type EthernetInterface struct {
	Port                  string
	PortName              string
	SpanningTreePt2PtMac  bool
	OpticalMonitorDisable bool // true if "no optical-monitor" is set
	// DualModeVLAN is set when the interface carries a `dual-mode [vlan-id]`
	// line — the FastIron 08.0.30-era (pre-08.0.80) way of making a tagged
	// port also carry untagged traffic for one VLAN. The VLAN context lists
	// such a port only as *tagged*; the untagged VLAN lives here. A bare
	// `dual-mode` means the default VLAN (1). Nil when not configured.
	DualModeVLAN *int
	// Disabled is true when the interface carries a ` disable` line: the
	// port is administratively down. Ports without the line are enabled.
	Disabled bool
	RawLines []string
}

// VEInterface represents a parsed virtual ethernet interface configuration.
type VEInterface struct {
	ID        int
	IPAddress string // "A.B.C.D M.M.M.M" format as it appears in config
	RawLines  []string
}

var (
	ethIfRe = regexp.MustCompile(`^interface ethernet (\d+/\d+/\d+)`)
	veIfRe  = regexp.MustCompile(`^interface ve (\d+)`)
	// FastIron prints the dual-mode VLAN with two spaces: ` dual-mode  102`.
	dualModeRe = regexp.MustCompile(`^dual-mode(?:\s+(\d+))?$`)
)

func (c *RunningConfig) parseEthernetStanza(lines []string) error {
	first := strings.TrimSpace(lines[0])

	m := ethIfRe.FindStringSubmatch(first)
	if m == nil {
		return fmt.Errorf("cannot parse ethernet interface: %q", first)
	}

	iface := EthernetInterface{
		Port: m[1],
	}

	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "port-name "):
			name := strings.TrimPrefix(trimmed, "port-name ")
			// Remove surrounding quotes if present.
			name = strings.Trim(name, "\"")
			iface.PortName = name

		case trimmed == "spanning-tree 802-1w admin-pt2pt-mac":
			iface.SpanningTreePt2PtMac = true

		case trimmed == "no optical-monitor":
			iface.OpticalMonitorDisable = true

		case trimmed == "disable":
			iface.Disabled = true

		case dualModeRe.MatchString(trimmed):
			dm := dualModeRe.FindStringSubmatch(trimmed)
			vlanID := 1 // bare `dual-mode` = untagged in the default VLAN
			if dm[1] != "" {
				id, err := strconv.Atoi(dm[1])
				if err != nil {
					return fmt.Errorf("interface %s dual-mode: %w", iface.Port, err)
				}
				vlanID = id
			}
			iface.DualModeVLAN = &vlanID

		default:
			iface.RawLines = append(iface.RawLines, trimmed)
		}
	}

	c.EthernetInterfaces = append(c.EthernetInterfaces, iface)
	return nil
}

func (c *RunningConfig) parseVEStanza(lines []string) error {
	first := strings.TrimSpace(lines[0])

	m := veIfRe.FindStringSubmatch(first)
	if m == nil {
		return fmt.Errorf("cannot parse VE interface: %q", first)
	}

	id, err := strconv.Atoi(m[1])
	if err != nil {
		return fmt.Errorf("invalid VE ID: %w", err)
	}

	ve := VEInterface{
		ID: id,
	}

	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "ip address "):
			ve.IPAddress = strings.TrimPrefix(trimmed, "ip address ")
		default:
			ve.RawLines = append(ve.RawLines, trimmed)
		}
	}

	c.VEInterfaces = append(c.VEInterfaces, ve)
	return nil
}

// FindEthernetInterface returns the interface with the given port, or nil if not found.
func (c *RunningConfig) FindEthernetInterface(port string) *EthernetInterface {
	for i := range c.EthernetInterfaces {
		if c.EthernetInterfaces[i].Port == port {
			return &c.EthernetInterfaces[i]
		}
	}
	return nil
}

// FindVEInterface returns the VE interface with the given ID, or nil if not found.
func (c *RunningConfig) FindVEInterface(id int) *VEInterface {
	for i := range c.VEInterfaces {
		if c.VEInterfaces[i].ID == id {
			return &c.VEInterfaces[i]
		}
	}
	return nil
}

// HasDualModeLines reports whether any interface carries a `dual-mode` line.
// Only firmware that has the command prints it, so this is direct evidence of
// the 08.0.30-era trunk dialect (consumed by internal/dialect).
func (c *RunningConfig) HasDualModeLines() bool {
	for i := range c.EthernetInterfaces {
		if c.EthernetInterfaces[i].DualModeVLAN != nil {
			return true
		}
	}
	return false
}
