package resource

import (
	"regexp"
	"testing"
)

// These regexes are load-bearing security controls: they are what stands
// between a Terraform config value and CLI command injection on the switch.
// Each table pins both the accept and reject sets so a future edit cannot
// silently loosen one (e.g. a \s that admits newlines).
func TestValidatorRegexes(t *testing.T) {
	tests := []struct {
		name string
		re   *regexp.Regexp
		ok   []string
		bad  []string
	}{
		{
			name: "rePort",
			re:   rePort,
			ok:   []string{"1/1/1", "1/2/24", "10/1/48"},
			bad:  []string{"", "1/1", "ethernet1", "1/1/1 ", "1/1/1\nexit", "1/1/x"},
		},
		{
			name: "reVLANName",
			re:   reVLANName,
			ok:   []string{"servers", "ISP-primary", "mgmt_20"},
			bad:  []string{"", "has space", `has"quote`, "has'quote", "new\nline", "tab\there"},
		},
		{
			name: "rePortName",
			re:   rePortName,
			ok:   []string{"uplink-router", "trunk to core", "node-0 mgmt b"},
			bad:  []string{"", `has"quote`, "new\nline", "cr\rhere", "bell\x07"},
		},
		{
			name: "reNoNewlines",
			re:   reNoNewlines,
			ok:   []string{"", "tagged ethe 1/1/1 to 1/1/4", "ip address 10.0.0.1/24"},
			bad:  []string{"a\nb", "a\rb", "exit\nno vlan 10"},
		},
		{
			name: "reManagerPortList",
			re:   reManagerPortList,
			ok:   []string{"987", "1/1/1 to 1/1/24", "1/1/1,1/1/2"},
			bad:  []string{"", "987\nno vlan 10", "abc", "1/1/1;reload", "1/1/1\tto 1/1/2"},
		},
		{
			name: "reAAAMethodList",
			re:   reAAAMethodList,
			ok:   []string{"default local", "radius local", "tacacs-server"},
			bad:  []string{"", "local\nno username admin", `local"`, "local\r"},
		},
		{
			name: "reUsernameValid",
			re:   reUsernameValid,
			ok:   []string{"admin", "svc-terraform", "user_2"},
			bad:  []string{"", "ad min", "a\nb", `a"b`, "a'b", "tab\tuser"},
		},
		{
			name: "rePasswordNoNewlines",
			re:   rePasswordNoNewlines,
			ok:   []string{"hunter2", "correct horse battery staple", `p@ss"word'!`},
			bad:  []string{"", "a\nb", "a\rb"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range tt.ok {
				if !tt.re.MatchString(s) {
					t.Errorf("%q should be accepted", s)
				}
			}
			for _, s := range tt.bad {
				if tt.re.MatchString(s) {
					t.Errorf("%q should be rejected", s)
				}
			}
		})
	}
}

func TestHashCommandsCollisionResistance(t *testing.T) {
	// The pre-fix scheme truncated to the first 40 chars of the first command,
	// so these two collided.
	a := hashCommands([]string{"snmp-server community public ro 1234567890"})
	b := hashCommands([]string{"snmp-server community public ro 1234567890 extra"})
	if a == b {
		t.Errorf("shared-prefix command lists collided: %s", a)
	}

	// Same commands must stay deterministic.
	if hashCommands([]string{"x", "y"}) != hashCommands([]string{"x", "y"}) {
		t.Error("hashCommands is not deterministic")
	}

	// Multi-command lists differing only in later elements must differ.
	if hashCommands([]string{"x", "y"}) == hashCommands([]string{"x", "z"}) {
		t.Error("lists differing after the first command collided")
	}

	if got := hashCommands(nil); got != "rawcfg-empty" {
		t.Errorf("empty list: got %q", got)
	}
}

func TestQuotePortName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"uplink", "uplink"},
		{"trunk to core", `"trunk to core"`},
		{`tru"nk to core`, `"trunk to core"`}, // embedded quotes stripped defensively
		{`up"link`, "uplink"},
	}
	for _, tt := range tests {
		if got := quotePortName(tt.in); got != tt.want {
			t.Errorf("quotePortName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
