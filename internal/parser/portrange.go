package parser

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Port represents a parsed interface port identifier (unit/module/port).
type Port struct {
	Unit   int
	Module int
	Port   int
}

func (p Port) String() string {
	return fmt.Sprintf("%d/%d/%d", p.Unit, p.Module, p.Port)
}

// ParsePort parses a port string like "1/2/3" into a Port struct.
func ParsePort(s string) (Port, error) {
	parts := strings.Split(strings.TrimSpace(s), "/")
	if len(parts) != 3 {
		return Port{}, fmt.Errorf("invalid port format %q: expected unit/module/port", s)
	}

	unit, err := strconv.Atoi(parts[0])
	if err != nil {
		return Port{}, fmt.Errorf("invalid unit in port %q: %w", s, err)
	}
	module, err := strconv.Atoi(parts[1])
	if err != nil {
		return Port{}, fmt.Errorf("invalid module in port %q: %w", s, err)
	}
	port, err := strconv.Atoi(parts[2])
	if err != nil {
		return Port{}, fmt.Errorf("invalid port number in port %q: %w", s, err)
	}

	return Port{Unit: unit, Module: module, Port: port}, nil
}

// portLess returns true if a sorts before b.
func portLess(a, b Port) bool {
	if a.Unit != b.Unit {
		return a.Unit < b.Unit
	}
	if a.Module != b.Module {
		return a.Module < b.Module
	}
	return a.Port < b.Port
}

// ExpandPortRange parses a FastIron port range string like
// "ethe 1/1/19 to 1/1/20 ethe 1/1/22 to 1/1/24"
// and returns a sorted list of individual port strings.
func ExpandPortRange(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}

	// Tokenize: extract all port references and "to" keywords.
	// Replace "ethe" and "ethernet" prefixes.
	normalized := strings.ReplaceAll(text, "ethernet", "")
	normalized = strings.ReplaceAll(normalized, "ethe", "")

	tokens := strings.Fields(normalized)

	var ports []Port
	i := 0
	for i < len(tokens) {
		token := tokens[i]

		// Should be a port like 1/1/19
		start, err := ParsePort(token)
		if err != nil {
			i++
			continue
		}

		// Check if next token is "to"
		if i+2 < len(tokens) && strings.EqualFold(tokens[i+1], "to") {
			end, err := ParsePort(tokens[i+2])
			if err != nil {
				return nil, fmt.Errorf("invalid end port in range: %w", err)
			}

			if start.Unit != end.Unit || start.Module != end.Module {
				return nil, fmt.Errorf("port range %s to %s spans different unit/module", start, end)
			}

			for p := start.Port; p <= end.Port; p++ {
				ports = append(ports, Port{Unit: start.Unit, Module: start.Module, Port: p})
			}
			i += 3
		} else {
			ports = append(ports, start)
			i++
		}
	}

	// Sort and deduplicate.
	sort.Slice(ports, func(i, j int) bool {
		return portLess(ports[i], ports[j])
	})

	var result []string
	seen := make(map[string]bool)
	for _, p := range ports {
		s := p.String()
		if !seen[s] {
			result = append(result, s)
			seen[s] = true
		}
	}

	return result, nil
}

// CompressPortRange takes a list of individual port strings and compresses them
// into FastIron CLI format: "ethe 1/1/19 to 1/1/20 ethe 1/1/22 to 1/1/24".
func CompressPortRange(portStrs []string) (string, error) {
	if len(portStrs) == 0 {
		return "", nil
	}

	var ports []Port
	for _, s := range portStrs {
		p, err := ParsePort(s)
		if err != nil {
			return "", err
		}
		ports = append(ports, p)
	}

	sort.Slice(ports, func(i, j int) bool {
		return portLess(ports[i], ports[j])
	})

	var parts []string
	i := 0
	for i < len(ports) {
		start := ports[i]
		end := start

		// Extend the range while consecutive ports are on the same unit/module.
		for i+1 < len(ports) &&
			ports[i+1].Unit == start.Unit &&
			ports[i+1].Module == start.Module &&
			ports[i+1].Port == end.Port+1 {
			end = ports[i+1]
			i++
		}

		if start == end {
			parts = append(parts, fmt.Sprintf("ethe %s", start))
		} else {
			parts = append(parts, fmt.Sprintf("ethe %s to %s", start, end))
		}

		i++
	}

	return strings.Join(parts, " "), nil
}
