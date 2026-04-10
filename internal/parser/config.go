package parser

import (
	"strings"
)

// RunningConfig represents a fully parsed FastIron running configuration.
type RunningConfig struct {
	VLANs              []VLAN
	EthernetInterfaces []EthernetInterface
	VEInterfaces       []VEInterface
	Users              []User
	AAA                AAAConfig
	Global             GlobalSettings
	StackUnits         []StackUnit
	RawLines           []string // Unparsed global lines
}

// ParseRunningConfig parses the full output of "show running-config" into structured data.
func ParseRunningConfig(text string) (*RunningConfig, error) {
	config := &RunningConfig{}
	lines := strings.Split(text, "\n")

	i := 0
	for i < len(lines) {
		line := strings.TrimRight(lines[i], "\r ")

		// Skip empty lines and comment markers.
		if line == "" || line == "!" || line == "Current configuration:" || line == "end" {
			i++
			continue
		}

		// Collect a stanza: the current line plus all subsequent indented lines.
		stanzaStart := i
		i++
		for i < len(lines) {
			next := strings.TrimRight(lines[i], "\r ")
			if next == "!" || next == "" {
				break
			}
			// Indented lines belong to the current stanza.
			if strings.HasPrefix(lines[i], " ") || strings.HasPrefix(lines[i], "\t") {
				i++
			} else {
				break
			}
		}

		stanzaLines := lines[stanzaStart:i]

		// Route the stanza to the appropriate parser.
		if err := config.parseStanza(stanzaLines); err != nil {
			return nil, err
		}
	}

	return config, nil
}

func (c *RunningConfig) parseStanza(lines []string) error {
	first := strings.TrimSpace(lines[0])

	switch {
	case strings.HasPrefix(first, "vlan "):
		return c.parseVLANStanza(lines)
	case strings.HasPrefix(first, "interface ethernet "):
		return c.parseEthernetStanza(lines)
	case strings.HasPrefix(first, "interface ve "):
		return c.parseVEStanza(lines)
	case strings.HasPrefix(first, "stack unit "):
		return c.parseStackUnitStanza(lines)
	case strings.HasPrefix(first, "username "):
		return c.parseUserLine(first)
	case strings.HasPrefix(first, "aaa "):
		c.parseAAALine(first)
	case strings.HasPrefix(first, "enable aaa"):
		c.parseAAALine(first)
	case strings.HasPrefix(first, "ver "):
		c.Global.Version = strings.TrimPrefix(first, "ver ")
	case first == "global-stp":
		c.Global.GlobalSTP = true
	case first == "no telnet server":
		c.Global.TelnetServer = false
		c.Global.TelnetServerSet = true
	case first == "telnet server":
		c.Global.TelnetServer = true
		c.Global.TelnetServerSet = true
	case first == "ip dhcp-client disable":
		c.Global.DHCPClientDisable = true
	case first == "optical-monitor":
		c.Global.OpticalMonitor = true
	case first == "optical-monitor non-ruckus-optic-enable":
		c.Global.OpticalMonitorNonRuckus = true
	case first == "manager registrar":
		c.Global.ManagerRegistrar = true
	case first == "manager disable":
		c.Global.ManagerDisable = true
	case strings.HasPrefix(first, "manager port-list "):
		c.Global.ManagerPortList = strings.TrimPrefix(first, "manager port-list ")
	case strings.HasPrefix(first, "ip route "):
		c.Global.StaticRoutes = append(c.Global.StaticRoutes, first)
	case strings.HasPrefix(first, "ip default-network "):
		c.Global.DefaultNetwork = strings.TrimPrefix(first, "ip default-network ")
	case strings.HasPrefix(first, "ip default-gateway "):
		c.Global.DefaultGateway = strings.TrimPrefix(first, "ip default-gateway ")
	case strings.HasPrefix(first, "ip address "):
		c.Global.IPAddress = strings.TrimPrefix(first, "ip address ")
	default:
		c.RawLines = append(c.RawLines, first)
	}

	return nil
}
