package parser

import (
	"os"
	"reflect"
	"testing"
)

// Global hostname and management addressing across every fixture. Only the
// 6430 capture carries a `hostname` line and DNS servers; the two
// switch-image captures carry the global `ip address`/`ip default-gateway`
// pair, and no router-image capture does.
func TestParseGlobalManagement_AllFixtures(t *testing.T) {
	tests := []struct {
		fixture  string
		hostname string
		ip, gw   string
		dns      []string
	}{
		{"7250-24", "", "", "", nil},
		{"7150-24", "", "", "", nil},
		{"7150-c12", "", "", "", nil},
		{"7150-c10zp", "", "10.0.20.5 255.255.255.0", "10.0.20.1", nil},
		{"6430-c12", "lab-icx-6430", "10.0.20.6 255.255.255.0", "10.0.20.9", []string{"10.0.20.1"}},
		{"6450-c12pd", "", "", "", nil},
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
			if config.Global.Hostname != tt.hostname {
				t.Errorf("Hostname = %q, want %q", config.Global.Hostname, tt.hostname)
			}
			if config.Global.IPAddress != tt.ip {
				t.Errorf("IPAddress = %q, want %q", config.Global.IPAddress, tt.ip)
			}
			if config.Global.DefaultGateway != tt.gw {
				t.Errorf("DefaultGateway = %q, want %q", config.Global.DefaultGateway, tt.gw)
			}
			if !reflect.DeepEqual(config.Global.DNSServers, tt.dns) {
				t.Errorf("DNSServers = %v, want %v", config.Global.DNSServers, tt.dns)
			}
			// Nothing captured here may leak into the unparsed lines.
			for _, line := range config.RawLines {
				for _, prefix := range []string{"hostname ", "ip dns server-address ", "ip address ", "ip default-gateway "} {
					if len(line) >= len(prefix) && line[:len(prefix)] == prefix {
						t.Errorf("line %q left in RawLines", line)
					}
				}
			}
		})
	}
}

// Several servers on one line, several lines, and the hostname's whitespace.
func TestParseGlobalManagement_Forms(t *testing.T) {
	config, err := ParseRunningConfig("hostname  core-1 \nip dns server-address 10.0.0.1 10.0.0.2\nip dns server-address 1.1.1.1\n")
	if err != nil {
		t.Fatal(err)
	}
	if config.Global.Hostname != "core-1" {
		t.Errorf("Hostname = %q", config.Global.Hostname)
	}
	if want := []string{"10.0.0.1", "10.0.0.2", "1.1.1.1"}; !reflect.DeepEqual(config.Global.DNSServers, want) {
		t.Errorf("DNSServers = %v, want %v", config.Global.DNSServers, want)
	}

	// Other `ip dns` settings are not swallowed.
	config, err = ParseRunningConfig("ip dns domain-list example.com\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Global.DNSServers) != 0 || len(config.RawLines) != 1 {
		t.Errorf("domain-list must stay raw: dns=%v raw=%v", config.Global.DNSServers, config.RawLines)
	}
}
