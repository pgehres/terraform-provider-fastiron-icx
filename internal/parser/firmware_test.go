package parser

import "testing"

func TestParseFirmwareVersion(t *testing.T) {
	tests := []struct {
		in   string
		want FirmwareVersion
		ok   bool
	}{
		{"08.0.30uT311", FirmwareVersion{8, 0, 30}, true},
		{"08.0.30uT313", FirmwareVersion{8, 0, 30}, true},
		{"08.0.95rT211", FirmwareVersion{8, 0, 95}, true},
		{"08.0.95qT213", FirmwareVersion{8, 0, 95}, true},
		{"09.0.10aT213", FirmwareVersion{9, 0, 10}, true},
		{"08.0.80", FirmwareVersion{8, 0, 80}, true},
		{"", FirmwareVersion{}, false},
		{"garbage", FirmwareVersion{}, false},
		{"8.0", FirmwareVersion{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseFirmwareVersion(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseFirmwareVersion(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// The dual-mode interface command was deprecated in FastIron 08.0.80; every
// earlier release (the 6000-series' 08.0.30 included) has it, and nothing from
// 08.0.80 on does. Unparseable strings must never claim the dialect.
func TestFirmwareHasDualMode(t *testing.T) {
	tests := []struct {
		ver  string
		want bool
	}{
		{"08.0.30uT311", true},
		{"08.0.30uT313", true},
		{"08.0.70bT213", true},
		{"08.0.79T213", true},
		{"08.0.80T213", false},
		{"08.0.90jT213", false},
		{"08.0.95rT211", false},
		{"08.0.95qT213", false},
		{"09.0.10aT213", false},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		if got := FirmwareHasDualMode(tt.ver); got != tt.want {
			t.Errorf("FirmwareHasDualMode(%q) = %v, want %v", tt.ver, got, tt.want)
		}
	}
}

func TestRunningConfigUsesDualMode(t *testing.T) {
	// All four 08.0.95 fixtures: current dialect.
	for _, f := range []string{"7250-24", "7150-24", "7150-c12", "7150-c10zp"} {
		config, err := ParseRunningConfig(loadTestConfig(t, "../../testdata/config."+f+".txt"))
		if err != nil {
			t.Fatalf("%s: parse error: %v", f, err)
		}
		if config.UsesDualMode() {
			t.Errorf("%s: 08.0.95 fixture must not use the dual-mode dialect", f)
		}
	}
	// Both 08.0.30 fixtures: dual-mode dialect.
	for _, f := range []string{"6430-c12", "6450-c12pd"} {
		config, err := ParseRunningConfig(loadTestConfig(t, "../../testdata/config."+f+".txt"))
		if err != nil {
			t.Fatalf("%s: parse error: %v", f, err)
		}
		if !config.UsesDualMode() {
			t.Errorf("%s: 08.0.30 fixture must use the dual-mode dialect", f)
		}
	}

	// Fallback: a config whose version is missing but which already contains a
	// dual-mode line is by definition from firmware that has the command.
	config, err := ParseRunningConfig("interface ethernet 1/1/1\n dual-mode  20\n!\n")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !config.UsesDualMode() {
		t.Error("config containing dual-mode lines must report UsesDualMode")
	}

	// Neither signal: never emit the dialect.
	config, err = ParseRunningConfig("interface ethernet 1/1/1\n port-name x\n!\n")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if config.UsesDualMode() {
		t.Error("config with no version and no dual-mode lines must not report UsesDualMode")
	}
}

func TestParseDualModeLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want *int
		raw  bool // expect the line to fall through to RawLines
	}{
		{"two-space form as printed by the switch", " dual-mode  102", intPtr(102), false},
		{"single space", " dual-mode 20", intPtr(20), false},
		{"bare dual-mode means default VLAN 1", " dual-mode", intPtr(1), false},
		{"unrelated line stays raw", " dual-mode-ish thing", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParseRunningConfig("interface ethernet 1/1/1\n" + tt.line + "\n!\n")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			eth := config.FindEthernetInterface("1/1/1")
			if eth == nil {
				t.Fatal("interface not found")
			}
			if tt.want == nil {
				if eth.DualModeVLAN != nil {
					t.Errorf("expected no dual-mode, got %d", *eth.DualModeVLAN)
				}
			} else if eth.DualModeVLAN == nil || *eth.DualModeVLAN != *tt.want {
				t.Errorf("DualModeVLAN = %v, want %d", eth.DualModeVLAN, *tt.want)
			}
			if tt.raw && len(eth.RawLines) != 1 {
				t.Errorf("expected line to be kept raw, got RawLines %v", eth.RawLines)
			}
			if !tt.raw && len(eth.RawLines) != 0 {
				t.Errorf("dual-mode line must not leak into RawLines, got %v", eth.RawLines)
			}
		})
	}
}

func intPtr(i int) *int { return &i }
