package parser

import (
	"testing"
)

func TestExpandPortRange(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
		wantErr  bool
	}{
		{
			name:     "single port",
			input:    "ethe 1/1/15",
			expected: []string{"1/1/15"},
		},
		{
			name:     "simple range",
			input:    "ethe 1/1/19 to 1/1/20",
			expected: []string{"1/1/19", "1/1/20"},
		},
		{
			name:     "larger range",
			input:    "ethe 1/1/22 to 1/1/24",
			expected: []string{"1/1/22", "1/1/23", "1/1/24"},
		},
		{
			name:  "mixed single and ranges",
			input: "ethe 1/1/15 ethe 1/1/19 to 1/1/20 ethe 1/1/22 to 1/1/24",
			expected: []string{
				"1/1/15", "1/1/19", "1/1/20", "1/1/22", "1/1/23", "1/1/24",
			},
		},
		{
			name:  "multiple modules",
			input: "ethe 1/1/1 to 1/1/18 ethe 1/2/1 to 1/2/2 ethe 1/3/1 to 1/3/2",
			expected: []string{
				"1/1/1", "1/1/2", "1/1/3", "1/1/4", "1/1/5", "1/1/6",
				"1/1/7", "1/1/8", "1/1/9", "1/1/10", "1/1/11", "1/1/12",
				"1/1/13", "1/1/14", "1/1/15", "1/1/16", "1/1/17", "1/1/18",
				"1/2/1", "1/2/2",
				"1/3/1", "1/3/2",
			},
		},
		{
			name:  "real config: 7150-24 vlan 101 tagged",
			input: "ethe 1/1/15 ethe 1/1/19 to 1/1/20 ethe 1/1/22 to 1/1/24 ethe 1/2/1 to 1/2/2 ethe 1/3/1 to 1/3/2 ethe 1/3/4",
			expected: []string{
				"1/1/15", "1/1/19", "1/1/20", "1/1/22", "1/1/23", "1/1/24",
				"1/2/1", "1/2/2",
				"1/3/1", "1/3/2", "1/3/4",
			},
		},
		{
			name:     "empty input",
			input:    "",
			expected: nil,
		},
		{
			name:    "cross-module range is error",
			input:   "ethe 1/1/24 to 1/2/1",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExpandPortRange(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d ports, got %d: %v", len(tt.expected), len(result), result)
			}
			for i, port := range result {
				if port != tt.expected[i] {
					t.Errorf("port[%d]: expected %q, got %q", i, tt.expected[i], port)
				}
			}
		})
	}
}

func TestCompressPortRange(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected string
		wantErr  bool
	}{
		{
			name:     "single port",
			input:    []string{"1/1/15"},
			expected: "ethe 1/1/15",
		},
		{
			name:     "two consecutive ports",
			input:    []string{"1/1/19", "1/1/20"},
			expected: "ethe 1/1/19 to 1/1/20",
		},
		{
			name:     "non-consecutive ports",
			input:    []string{"1/1/15", "1/1/19"},
			expected: "ethe 1/1/15 ethe 1/1/19",
		},
		{
			name:     "mixed consecutive and non-consecutive",
			input:    []string{"1/1/15", "1/1/19", "1/1/20", "1/1/22", "1/1/23", "1/1/24"},
			expected: "ethe 1/1/15 ethe 1/1/19 to 1/1/20 ethe 1/1/22 to 1/1/24",
		},
		{
			name: "multiple modules",
			input: []string{
				"1/1/15", "1/1/19", "1/1/20", "1/1/22", "1/1/23", "1/1/24",
				"1/2/1", "1/2/2",
				"1/3/1", "1/3/2", "1/3/4",
			},
			expected: "ethe 1/1/15 ethe 1/1/19 to 1/1/20 ethe 1/1/22 to 1/1/24 ethe 1/2/1 to 1/2/2 ethe 1/3/1 to 1/3/2 ethe 1/3/4",
		},
		{
			name:     "empty input",
			input:    []string{},
			expected: "",
		},
		{
			name:     "unsorted input gets sorted",
			input:    []string{"1/1/24", "1/1/22", "1/1/15", "1/1/23"},
			expected: "ethe 1/1/15 ethe 1/1/22 to 1/1/24",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := CompressPortRange(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	// Real port range strings from the config files should round-trip.
	inputs := []string{
		"ethe 1/1/15 ethe 1/1/19 to 1/1/20 ethe 1/1/22 to 1/1/24 ethe 1/2/1 to 1/2/2 ethe 1/3/1 to 1/3/2 ethe 1/3/4",
		"ethe 1/1/1 to 1/1/18 ethe 1/2/1 to 1/2/2 ethe 1/3/1 to 1/3/2",
		"ethe 1/2/1 to 1/2/2 ethe 1/3/1 to 1/3/2",
		"ethe 1/1/1 to 1/1/7",
		"ethe 1/1/8 ethe 1/3/1",
		"ethe 1/3/1 to 1/3/2",
		"ethe 1/1/19 to 1/1/20 ethe 1/2/1 ethe 1/2/3 to 1/2/8",
		"ethe 1/1/1 to 1/1/18 ethe 1/1/24 ethe 1/2/1 ethe 1/2/4 to 1/2/6",
	}

	for _, input := range inputs {
		ports, err := ExpandPortRange(input)
		if err != nil {
			t.Errorf("expand %q: %v", input, err)
			continue
		}
		compressed, err := CompressPortRange(ports)
		if err != nil {
			t.Errorf("compress ports from %q: %v", input, err)
			continue
		}
		if compressed != input {
			t.Errorf("round-trip failed:\n  input:  %q\n  output: %q", input, compressed)
		}
	}
}
