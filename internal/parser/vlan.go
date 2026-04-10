package parser

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// VLAN represents a parsed VLAN configuration stanza.
type VLAN struct {
	ID              int
	Name            string
	TaggedPorts     []string
	UntaggedPorts   []string
	RouterInterface *int // VE interface number, nil if not set
	SpanningTree    bool
	STPPriority     *int
	MulticastPassive bool
	MulticastVersion *int
}

var vlanHeaderRe = regexp.MustCompile(`^vlan\s+(\d+)(?:\s+name\s+(\S+))?\s+by\s+port`)

func (c *RunningConfig) parseVLANStanza(lines []string) error {
	first := strings.TrimSpace(lines[0])

	m := vlanHeaderRe.FindStringSubmatch(first)
	if m == nil {
		return fmt.Errorf("cannot parse VLAN header: %q", first)
	}

	id, err := strconv.Atoi(m[1])
	if err != nil {
		return fmt.Errorf("invalid VLAN ID: %w", err)
	}

	vlan := VLAN{
		ID:   id,
		Name: m[2], // May be empty string if no name.
	}

	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "tagged "):
			ports, err := ExpandPortRange(strings.TrimPrefix(trimmed, "tagged "))
			if err != nil {
				return fmt.Errorf("vlan %d tagged: %w", id, err)
			}
			vlan.TaggedPorts = append(vlan.TaggedPorts, ports...)

		case strings.HasPrefix(trimmed, "untagged "):
			ports, err := ExpandPortRange(strings.TrimPrefix(trimmed, "untagged "))
			if err != nil {
				return fmt.Errorf("vlan %d untagged: %w", id, err)
			}
			vlan.UntaggedPorts = append(vlan.UntaggedPorts, ports...)

		case strings.HasPrefix(trimmed, "router-interface ve "):
			veStr := strings.TrimPrefix(trimmed, "router-interface ve ")
			ve, err := strconv.Atoi(strings.TrimSpace(veStr))
			if err != nil {
				return fmt.Errorf("vlan %d router-interface: %w", id, err)
			}
			vlan.RouterInterface = &ve

		case trimmed == "spanning-tree 802-1w":
			vlan.SpanningTree = true

		case strings.HasPrefix(trimmed, "spanning-tree 802-1w priority "):
			priStr := strings.TrimPrefix(trimmed, "spanning-tree 802-1w priority ")
			pri, err := strconv.Atoi(strings.TrimSpace(priStr))
			if err != nil {
				return fmt.Errorf("vlan %d stp priority: %w", id, err)
			}
			vlan.STPPriority = &pri

		case trimmed == "multicast passive":
			vlan.MulticastPassive = true

		case strings.HasPrefix(trimmed, "multicast version "):
			verStr := strings.TrimPrefix(trimmed, "multicast version ")
			ver, err := strconv.Atoi(strings.TrimSpace(verStr))
			if err != nil {
				return fmt.Errorf("vlan %d multicast version: %w", id, err)
			}
			vlan.MulticastVersion = &ver
		}
	}

	c.VLANs = append(c.VLANs, vlan)
	return nil
}

// FindVLAN returns the VLAN with the given ID, or nil if not found.
func (c *RunningConfig) FindVLAN(id int) *VLAN {
	for i := range c.VLANs {
		if c.VLANs[i].ID == id {
			return &c.VLANs[i]
		}
	}
	return nil
}
