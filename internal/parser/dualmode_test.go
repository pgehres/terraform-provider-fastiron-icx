package parser

import "testing"

// Version-based dual-mode detection lives in internal/dialect (CapDualMode).
// The parser only reports the direct evidence: whether dual-mode lines exist.
func TestHasDualModeLines(t *testing.T) {
	tests := []struct {
		fixture string
		want    bool
	}{
		{"7250-24", false},
		{"7150-24", false},
		{"7150-c12", false},
		{"7150-c10zp", false},
		{"6430-c12", true},
		{"6450-c12pd", true},
	}
	for _, tt := range tests {
		config, err := ParseRunningConfig(loadTestConfig(t, "../../testdata/config."+tt.fixture+".txt"))
		if err != nil {
			t.Fatalf("%s: parse error: %v", tt.fixture, err)
		}
		if got := config.HasDualModeLines(); got != tt.want {
			t.Errorf("%s: HasDualModeLines() = %v, want %v", tt.fixture, got, tt.want)
		}
	}

	config, err := ParseRunningConfig("interface ethernet 1/1/1\n port-name x\n!\n")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if config.HasDualModeLines() {
		t.Error("config without dual-mode lines must not report them")
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
