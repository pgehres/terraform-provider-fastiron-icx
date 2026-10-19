package parser

import "testing"

func TestImageType_Fixtures(t *testing.T) {
	tests := []struct {
		file     string
		expected ImageType
	}{
		{"../../testdata/config.7250-24.txt", ImageTypeRouter},
		{"../../testdata/config.7150-24.txt", ImageTypeRouter},
		{"../../testdata/config.7150-c12.txt", ImageTypeRouter},
		{"../../testdata/config.7150-c10zp.txt", ImageTypeSwitch},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			text := loadTestConfig(t, tt.file)
			config, err := ParseRunningConfig(text)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if got := config.ImageType(); got != tt.expected {
				t.Errorf("image type: expected %q, got %q (version %q)", tt.expected, got, config.Global.Version)
			}
		})
	}
}

func TestImageType_VersionSuffix(t *testing.T) {
	tests := []struct {
		version  string
		expected ImageType
	}{
		{"08.0.95qT213", ImageTypeRouter},
		{"08.0.95mT213", ImageTypeRouter},
		{"08.0.95rT211", ImageTypeSwitch},
		{"08.0.40q017T213", ImageTypeRouter},
		{"08.0.40T211", ImageTypeSwitch},
		// ICX 6000-series T31x codes (observed on a live ICX 6430-C12
		// switch image reporting 08.0.30uT311).
		{"08.0.30uT311", ImageTypeSwitch},
		{"08.0.30uT313", ImageTypeRouter},
		// Unrecognized T-codes and malformed versions must not be guessed at.
		{"10.1.05T215", ImageTypeUnknown},
		{"08.0.95", ImageTypeUnknown},
		{"", ImageTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			config := &RunningConfig{}
			config.Global.Version = tt.version
			if got := config.ImageType(); got != tt.expected {
				t.Errorf("version %q: expected %q, got %q", tt.version, tt.expected, got)
			}
		})
	}
}

func TestImageType_StructuralFallback(t *testing.T) {
	veID := 10

	tests := []struct {
		name     string
		config   RunningConfig
		expected ImageType
	}{
		{
			name:     "VE interface means router image",
			config:   RunningConfig{VEInterfaces: []VEInterface{{ID: 1}}},
			expected: ImageTypeRouter,
		},
		{
			name: "static route means router image",
			config: RunningConfig{
				Global: GlobalSettings{StaticRoutes: []string{"ip route 0.0.0.0 0.0.0.0 10.0.20.1"}},
			},
			expected: ImageTypeRouter,
		},
		{
			name:     "global-stp means router image",
			config:   RunningConfig{Global: GlobalSettings{GlobalSTP: true}},
			expected: ImageTypeRouter,
		},
		{
			name:     "VLAN router-interface means router image",
			config:   RunningConfig{VLANs: []VLAN{{ID: 20, RouterInterface: &veID}}},
			expected: ImageTypeRouter,
		},
		{
			name:     "global ip address means switch image",
			config:   RunningConfig{Global: GlobalSettings{IPAddress: "10.0.20.5 255.255.255.0"}},
			expected: ImageTypeSwitch,
		},
		{
			name:     "global default-gateway means switch image",
			config:   RunningConfig{Global: GlobalSettings{DefaultGateway: "10.0.20.1"}},
			expected: ImageTypeSwitch,
		},
		{
			name:     "no signal is unknown",
			config:   RunningConfig{},
			expected: ImageTypeUnknown,
		},
		{
			// Switch images cannot carry L3 artifacts, so a router signal is
			// definitive even alongside a switch-style global ip address.
			name: "router signal wins over switch signal",
			config: RunningConfig{
				VEInterfaces: []VEInterface{{ID: 1}},
				Global: GlobalSettings{
					IPAddress:      "10.0.20.5 255.255.255.0",
					DefaultGateway: "10.0.20.1",
				},
			},
			expected: ImageTypeRouter,
		},
		{
			name: "VLAN router-interface wins over global ip address",
			config: RunningConfig{
				VLANs:  []VLAN{{ID: 20, RouterInterface: &veID}},
				Global: GlobalSettings{IPAddress: "10.0.20.5 255.255.255.0"},
			},
			expected: ImageTypeRouter,
		},
		{
			name: "recognized version suffix wins over structure",
			config: RunningConfig{
				Global: GlobalSettings{
					Version:      "08.0.95rT211",
					StaticRoutes: []string{"ip route 0.0.0.0 0.0.0.0 10.0.20.1"},
				},
			},
			expected: ImageTypeSwitch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.config.ImageType(); got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}
