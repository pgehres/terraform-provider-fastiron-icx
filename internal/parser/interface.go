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
	RawLines              []string
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
