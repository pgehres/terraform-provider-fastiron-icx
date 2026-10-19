package parser

import (
	"strconv"
	"strings"
)

// NTPConfig is the parsed `ntp` stanza.
type NTPConfig struct {
	// Present is true when the running config has an `ntp` stanza at all.
	Present bool
	// Servers lists the address of every ` server <addr> [options]` line, in
	// device order. Options (minpoll, maxpoll, key, version, burst) are not
	// modelled; the full line is kept in RawLines when any are present.
	Servers []string
	// DisableServe is true when the stanza contains ` disable serve` — the
	// device acts as an NTP client only and does not serve time downstream.
	DisableServe bool
	// RawLines holds the stanza's other lines (peer, authenticate,
	// source-interface, ...), trimmed.
	RawLines []string
}

// ClockConfig holds the global `clock` lines.
type ClockConfig struct {
	// Timezone is the text after `clock timezone ` exactly as the device
	// prints it, e.g. "us Pacific" or "gmt gmt+01". Empty means the default
	// (GMT).
	Timezone string
	// SummerTime is true when any `clock summer-time` line is present.
	SummerTime bool
	// SummerTimeArgs is whatever follows `clock summer-time` (zone, start,
	// end, offset), or "" for the bare form.
	SummerTimeArgs string
}

// SNMPCommunity is one `snmp-server community` line.
type SNMPCommunity struct {
	// String is the community string token as printed. FastIron never prints
	// the plaintext unless the line was entered with the `0` prefix: the
	// default is an encrypted form (`1 $...`) or, on most 08.0.x builds, a
	// mask of dots (`.....`). Encrypted is true in both of those cases.
	String    string
	Encrypted bool
	Mode      string // "ro" or "rw"
	View      string // `view <name>`, if any
	ACL       string // trailing standard ACL name or number, if any
}

// SNMPConfig holds the global `snmp-server` lines the provider models.
type SNMPConfig struct {
	Communities []SNMPCommunity
	Contact     string
	Location    string
	// RawLines holds other `snmp-server` lines (host, view, group, user,
	// enable traps, ...), untrimmed of their `snmp-server ` prefix.
	RawLines []string
}

// LoggingConfig holds the global `logging` lines the provider models.
type LoggingConfig struct {
	// Hosts lists the IPv4 address or name of every `logging host <addr>`
	// line, in device order. IPv6 hosts (`logging host ipv6 ...`) are left in
	// RawLines.
	Hosts []string
	// BufferedEntries is N from `logging buffered <N>`; 0 when absent (the
	// device default is 50).
	BufferedEntries int
	// BufferedDisabledLevels lists levels from `no logging buffered <level>`
	// lines, in device order.
	BufferedDisabledLevels []string
	// BufferedEnabledLevels lists levels from `logging buffered <level>`
	// lines (levels off by default that were switched on), in device order.
	BufferedEnabledLevels []string
	// Facility is the name from `logging facility <name>`; "" when absent
	// (the device default is user).
	Facility string
	// RawLines holds other logging lines (console, on, persistence, ...)
	// including any leading `no `.
	RawLines []string
}

func (c *RunningConfig) parseNTPStanza(lines []string) {
	c.NTP.Present = true
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "disable serve":
			c.NTP.DisableServe = true
		case strings.HasPrefix(trimmed, "server "):
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				c.NTP.Servers = append(c.NTP.Servers, fields[1])
			}
			if len(fields) > 2 {
				c.NTP.RawLines = append(c.NTP.RawLines, trimmed)
			}
		default:
			c.NTP.RawLines = append(c.NTP.RawLines, trimmed)
		}
	}
}

func (c *RunningConfig) parseClockLine(line string) {
	switch {
	case strings.HasPrefix(line, "clock timezone "):
		c.Clock.Timezone = strings.TrimSpace(strings.TrimPrefix(line, "clock timezone "))
	case line == "clock summer-time":
		c.Clock.SummerTime = true
	case strings.HasPrefix(line, "clock summer-time "):
		c.Clock.SummerTime = true
		c.Clock.SummerTimeArgs = strings.TrimSpace(strings.TrimPrefix(line, "clock summer-time "))
	default:
		c.RawLines = append(c.RawLines, line)
	}
}

func (c *RunningConfig) parseSNMPLine(line string) {
	switch {
	case strings.HasPrefix(line, "snmp-server community "):
		if comm, ok := parseSNMPCommunity(strings.Fields(strings.TrimPrefix(line, "snmp-server community "))); ok {
			c.SNMP.Communities = append(c.SNMP.Communities, comm)
		} else {
			c.SNMP.RawLines = append(c.SNMP.RawLines, line)
		}
	case strings.HasPrefix(line, "snmp-server contact "):
		c.SNMP.Contact = strings.TrimSpace(strings.TrimPrefix(line, "snmp-server contact "))
	case strings.HasPrefix(line, "snmp-server location "):
		c.SNMP.Location = strings.TrimSpace(strings.TrimPrefix(line, "snmp-server location "))
	default:
		c.SNMP.RawLines = append(c.SNMP.RawLines, line)
	}
}

// parseSNMPCommunity parses the fields after `snmp-server community`:
//
//	[0|1] <string> {ro|rw} [view <name>] [<acl-name-or-number>]
func parseSNMPCommunity(fields []string) (SNMPCommunity, bool) {
	var comm SNMPCommunity
	if len(fields) > 0 && (fields[0] == "0" || fields[0] == "1") {
		comm.Encrypted = fields[0] == "1"
		fields = fields[1:]
	}
	if len(fields) < 2 {
		return comm, false
	}
	comm.String = fields[0]
	if strings.Trim(comm.String, ".") == "" {
		comm.Encrypted = true // masked: `.....`
	}
	comm.Mode = fields[1]
	if comm.Mode != "ro" && comm.Mode != "rw" {
		return comm, false
	}
	rest := fields[2:]
	if len(rest) >= 2 && rest[0] == "view" {
		comm.View = rest[1]
		rest = rest[2:]
	}
	if len(rest) >= 1 {
		comm.ACL = rest[0]
	}
	return comm, true
}

func (c *RunningConfig) parseLoggingLine(line string) {
	negated := strings.HasPrefix(line, "no ")
	body := strings.TrimPrefix(line, "no ")
	fields := strings.Fields(body)
	switch {
	case !negated && len(fields) == 3 && fields[1] == "host" && fields[2] != "ipv6",
		!negated && len(fields) > 3 && fields[1] == "host" && fields[2] != "ipv6":
		c.Logging.Hosts = append(c.Logging.Hosts, fields[2])
	case len(fields) == 3 && fields[1] == "buffered":
		if n, err := strconv.Atoi(fields[2]); err == nil {
			if !negated {
				c.Logging.BufferedEntries = n
			} else {
				c.Logging.RawLines = append(c.Logging.RawLines, line)
			}
		} else if negated {
			c.Logging.BufferedDisabledLevels = append(c.Logging.BufferedDisabledLevels, fields[2])
		} else {
			c.Logging.BufferedEnabledLevels = append(c.Logging.BufferedEnabledLevels, fields[2])
		}
	case !negated && len(fields) == 3 && fields[1] == "facility":
		c.Logging.Facility = fields[2]
	default:
		c.Logging.RawLines = append(c.Logging.RawLines, line)
	}
}
