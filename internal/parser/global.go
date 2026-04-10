package parser

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// GlobalSettings holds global configuration values.
type GlobalSettings struct {
	Version                 string
	GlobalSTP               bool
	TelnetServer            bool
	TelnetServerSet         bool // true if explicitly configured (vs default)
	DHCPClientDisable       bool
	OpticalMonitor          bool
	OpticalMonitorNonRuckus bool
	ManagerRegistrar        bool
	ManagerDisable          bool
	ManagerPortList         string
	StaticRoutes            []string
	DefaultNetwork          string
	DefaultGateway          string
	IPAddress               string // Global IP (used on some models like c10zp)
}

// AAAConfig holds AAA authentication settings.
type AAAConfig struct {
	WebServerAuth   string // e.g., "default local"
	LoginAuth       string // e.g., "default local"
	EnableAAAConsole bool
}

// User represents a local user account.
type User struct {
	Username string
	Password string // Hashed/encrypted as it appears in config.
}

// StackUnit represents a stack unit and its modules.
type StackUnit struct {
	ID         int
	Modules    []StackModule
	StackPorts []string
}

// StackModule represents a module within a stack unit.
type StackModule struct {
	ID   int
	Type string
}

var stackUnitRe = regexp.MustCompile(`^stack unit (\d+)`)
var moduleRe = regexp.MustCompile(`^\s*module\s+(\d+)\s+(\S+)`)
var stackPortRe = regexp.MustCompile(`^\s*stack-port\s+(\S+)`)

func (c *RunningConfig) parseStackUnitStanza(lines []string) error {
	first := strings.TrimSpace(lines[0])

	m := stackUnitRe.FindStringSubmatch(first)
	if m == nil {
		return fmt.Errorf("cannot parse stack unit: %q", first)
	}

	id, err := strconv.Atoi(m[1])
	if err != nil {
		return fmt.Errorf("invalid stack unit ID: %w", err)
	}

	unit := StackUnit{ID: id}

	for _, line := range lines[1:] {
		if mm := moduleRe.FindStringSubmatch(line); mm != nil {
			modID, _ := strconv.Atoi(mm[1])
			unit.Modules = append(unit.Modules, StackModule{
				ID:   modID,
				Type: mm[2],
			})
		} else if sm := stackPortRe.FindStringSubmatch(line); sm != nil {
			unit.StackPorts = append(unit.StackPorts, sm[1])
		}
	}

	c.StackUnits = append(c.StackUnits, unit)
	return nil
}

func (c *RunningConfig) parseUserLine(line string) error {
	// Format: "username NAME password HASH"
	parts := strings.Fields(line)
	if len(parts) < 4 || parts[2] != "password" {
		return fmt.Errorf("cannot parse user line: %q", line)
	}

	c.Users = append(c.Users, User{
		Username: parts[1],
		Password: strings.Join(parts[3:], " "),
	})
	return nil
}

func (c *RunningConfig) parseAAALine(line string) {
	switch {
	case strings.HasPrefix(line, "aaa authentication web-server "):
		c.AAA.WebServerAuth = strings.TrimPrefix(line, "aaa authentication web-server ")
	case strings.HasPrefix(line, "aaa authentication login "):
		c.AAA.LoginAuth = strings.TrimPrefix(line, "aaa authentication login ")
	case line == "enable aaa console":
		c.AAA.EnableAAAConsole = true
	}
}
