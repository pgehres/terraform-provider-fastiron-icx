package parser

import (
	"os"
	"reflect"
	"testing"
)

// The ICX 6430 capture (FastIron 08.0.30) is the one fixture with an `ntp`
// stanza and `clock` lines; every other capture has none, so the zero values
// must come back for them.
func TestParseNTPAndClock_AllFixtures(t *testing.T) {
	tests := []struct {
		fixture      string
		present      bool
		servers      []string
		disableServe bool
		timezone     string
		summerTime   bool
	}{
		{"7250-24", false, nil, false, "", false},
		{"7150-24", false, nil, false, "", false},
		{"7150-c12", false, nil, false, "", false},
		{"7150-c10zp", false, nil, false, "", false},
		{"6430-c12", true, []string{"216.239.35.0", "216.239.35.4"}, true, "us Pacific", true},
		{"6450-c12pd", false, nil, false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			data, err := os.ReadFile("../../testdata/config." + tt.fixture + ".txt")
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			config, err := ParseRunningConfig(string(data))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if config.NTP.Present != tt.present {
				t.Errorf("NTP.Present = %v, want %v", config.NTP.Present, tt.present)
			}
			if !reflect.DeepEqual(config.NTP.Servers, tt.servers) {
				t.Errorf("NTP.Servers = %v, want %v", config.NTP.Servers, tt.servers)
			}
			if config.NTP.DisableServe != tt.disableServe {
				t.Errorf("NTP.DisableServe = %v, want %v", config.NTP.DisableServe, tt.disableServe)
			}
			if len(config.NTP.RawLines) != 0 {
				t.Errorf("NTP.RawLines = %v, want none", config.NTP.RawLines)
			}
			if config.Clock.Timezone != tt.timezone {
				t.Errorf("Clock.Timezone = %q, want %q", config.Clock.Timezone, tt.timezone)
			}
			if config.Clock.SummerTime != tt.summerTime {
				t.Errorf("Clock.SummerTime = %v, want %v", config.Clock.SummerTime, tt.summerTime)
			}
			if config.Clock.SummerTimeArgs != "" {
				t.Errorf("Clock.SummerTimeArgs = %q, want empty", config.Clock.SummerTimeArgs)
			}
			// None of these lines may leak into the unparsed remainder.
			for _, l := range config.RawLines {
				switch l {
				case "ntp", "disable serve", "clock summer-time", "clock timezone us Pacific":
					t.Errorf("line %q left in RawLines", l)
				}
			}
			// No capture has SNMP or logging configuration.
			if len(config.SNMP.Communities) != 0 || config.SNMP.Contact != "" || config.SNMP.Location != "" {
				t.Errorf("unexpected SNMP config: %+v", config.SNMP)
			}
			if len(config.Logging.Hosts) != 0 || config.Logging.Facility != "" || config.Logging.BufferedEntries != 0 {
				t.Errorf("unexpected logging config: %+v", config.Logging)
			}
		})
	}
}

func TestParseNTPStanza_OptionsAndOtherLines(t *testing.T) {
	config, err := ParseRunningConfig(`ntp
 authenticate
 server 10.0.0.1 minpoll 5 maxpoll 10
 server 10.0.0.2 key 1
 peer 10.0.0.3
 source-interface ve 1
 disable serve
!
`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := config.NTP.Servers, []string{"10.0.0.1", "10.0.0.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Servers = %v, want %v", got, want)
	}
	if !config.NTP.DisableServe {
		t.Error("DisableServe should be true")
	}
	want := []string{"authenticate", "server 10.0.0.1 minpoll 5 maxpoll 10", "server 10.0.0.2 key 1", "peer 10.0.0.3", "source-interface ve 1"}
	if !reflect.DeepEqual(config.NTP.RawLines, want) {
		t.Errorf("RawLines = %v, want %v", config.NTP.RawLines, want)
	}
}

func TestParseClockSummerTimeWithArgs(t *testing.T) {
	config, err := ParseRunningConfig("clock summer-time zone us mountain start 10-30-16 02:00:00 end 02-27-17 02:00:00 offset 30\nclock timezone gmt gmt+01\n")
	if err != nil {
		t.Fatal(err)
	}
	if !config.Clock.SummerTime || config.Clock.SummerTimeArgs != "zone us mountain start 10-30-16 02:00:00 end 02-27-17 02:00:00 offset 30" {
		t.Errorf("summer-time = %v %q", config.Clock.SummerTime, config.Clock.SummerTimeArgs)
	}
	if config.Clock.Timezone != "gmt gmt+01" {
		t.Errorf("Timezone = %q", config.Clock.Timezone)
	}
}

// Every spelling FastIron uses for a community line: masked (the default
// display on 08.0.9x), encrypted with the `1` prefix, plaintext with `0`,
// plaintext without prefix, and with view and ACL suffixes.
func TestParseSNMP(t *testing.T) {
	config, err := ParseRunningConfig(`snmp-server community ..... ro
snmp-server community ..... rw 25
snmp-server community 1 $U2kyKQ== ro view sysview myACL
snmp-server community 0 public ro
snmp-server community secret rw view all
snmp-server contact Network Operations <noc@example.com>
snmp-server location Rack 12, Row 3
snmp-server host 10.0.0.9 version v2c .....
snmp-server enable traps link-change
no snmp-server
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []SNMPCommunity{
		{String: ".....", Encrypted: true, Mode: "ro"},
		{String: ".....", Encrypted: true, Mode: "rw", ACL: "25"},
		{String: "$U2kyKQ==", Encrypted: true, Mode: "ro", View: "sysview", ACL: "myACL"},
		{String: "public", Encrypted: false, Mode: "ro"},
		{String: "secret", Encrypted: false, Mode: "rw", View: "all"},
	}
	if !reflect.DeepEqual(config.SNMP.Communities, want) {
		t.Errorf("Communities = %+v, want %+v", config.SNMP.Communities, want)
	}
	if config.SNMP.Contact != "Network Operations <noc@example.com>" {
		t.Errorf("Contact = %q", config.SNMP.Contact)
	}
	if config.SNMP.Location != "Rack 12, Row 3" {
		t.Errorf("Location = %q", config.SNMP.Location)
	}
	wantRaw := []string{"snmp-server host 10.0.0.9 version v2c .....", "snmp-server enable traps link-change"}
	if !reflect.DeepEqual(config.SNMP.RawLines, wantRaw) {
		t.Errorf("SNMP.RawLines = %v, want %v", config.SNMP.RawLines, wantRaw)
	}
	// `no snmp-server` is not an snmp-server line; it stays global.
	if !containsString(config.RawLines, "no snmp-server") {
		t.Errorf("global RawLines = %v, want `no snmp-server` kept", config.RawLines)
	}
}

func TestParseSNMPCommunity_Malformed(t *testing.T) {
	config, err := ParseRunningConfig("snmp-server community public\nsnmp-server community public rx\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.SNMP.Communities) != 0 {
		t.Errorf("malformed lines parsed as communities: %+v", config.SNMP.Communities)
	}
	if len(config.SNMP.RawLines) != 2 {
		t.Errorf("malformed lines should be kept raw, got %v", config.SNMP.RawLines)
	}
}

func TestParseLogging(t *testing.T) {
	config, err := ParseRunningConfig(`logging host 10.0.0.5
logging host 10.0.0.6 udp-port 5514
logging host ipv6 2001:db8::1
logging buffered 1000
logging buffered debugging
no logging buffered informational
no logging buffered notifications
logging facility local7
logging console
no logging on
`)
	if err != nil {
		t.Fatal(err)
	}
	l := config.Logging
	if got, want := l.Hosts, []string{"10.0.0.5", "10.0.0.6"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts = %v, want %v", got, want)
	}
	if l.BufferedEntries != 1000 {
		t.Errorf("BufferedEntries = %d", l.BufferedEntries)
	}
	if got, want := l.BufferedEnabledLevels, []string{"debugging"}; !reflect.DeepEqual(got, want) {
		t.Errorf("BufferedEnabledLevels = %v, want %v", got, want)
	}
	if got, want := l.BufferedDisabledLevels, []string{"informational", "notifications"}; !reflect.DeepEqual(got, want) {
		t.Errorf("BufferedDisabledLevels = %v, want %v", got, want)
	}
	if l.Facility != "local7" {
		t.Errorf("Facility = %q", l.Facility)
	}
	wantRaw := []string{"logging host ipv6 2001:db8::1", "logging console", "no logging on"}
	if !reflect.DeepEqual(l.RawLines, wantRaw) {
		t.Errorf("Logging.RawLines = %v, want %v", l.RawLines, wantRaw)
	}
	if containsString(config.RawLines, "logging console") || containsString(config.RawLines, "no logging on") {
		t.Errorf("logging lines leaked into global RawLines: %v", config.RawLines)
	}
}

// A ` disable` line marks an administratively-down port; no fixture has one.
func TestParseInterfaceDisabled(t *testing.T) {
	config, err := ParseRunningConfig(`interface ethernet 1/1/3
 port-name spare
 disable
!
interface ethernet 1/1/4
 disable
 no optical-monitor
!
interface ethernet 1/1/5
 port-name "disable me later"
!
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		port string
		want bool
	}{{"1/1/3", true}, {"1/1/4", true}, {"1/1/5", false}} {
		iface := config.FindEthernetInterface(tt.port)
		if iface == nil {
			t.Fatalf("%s not parsed", tt.port)
		}
		if iface.Disabled != tt.want {
			t.Errorf("%s Disabled = %v, want %v", tt.port, iface.Disabled, tt.want)
		}
		if len(iface.RawLines) != 0 {
			t.Errorf("%s RawLines = %v, want none", tt.port, iface.RawLines)
		}
	}
	if config.FindEthernetInterface("1/1/4").OpticalMonitorDisable != true {
		t.Error("1/1/4 optical-monitor should still parse")
	}
	for _, f := range []string{"7250-24", "7150-24", "7150-c12", "7150-c10zp", "6430-c12", "6450-c12pd"} {
		data, err := os.ReadFile("../../testdata/config." + f + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := ParseRunningConfig(string(data))
		if err != nil {
			t.Fatal(err)
		}
		for _, iface := range cfg.EthernetInterfaces {
			if iface.Disabled {
				t.Errorf("%s: %s unexpectedly disabled", f, iface.Port)
			}
		}
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
