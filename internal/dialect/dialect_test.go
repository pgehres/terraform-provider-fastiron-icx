package dialect

import (
	"os"
	"strings"
	"testing"

	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"08.0.95mT213", Version{8, 0, 95, "08.0.95mT213"}, true},
		{"08.0.95qT213", Version{8, 0, 95, "08.0.95qT213"}, true},
		{"08.0.95rT211", Version{8, 0, 95, "08.0.95rT211"}, true},
		{"08.0.30uT311", Version{8, 0, 30, "08.0.30uT311"}, true},
		{"08.0.30uT313", Version{8, 0, 30, "08.0.30uT313"}, true},
		{"08.0.79T213", Version{8, 0, 79, "08.0.79T213"}, true},
		{"09.0.10aT213", Version{9, 0, 10, "09.0.10aT213"}, true},
		{"08.0.80", Version{8, 0, 80, "08.0.80"}, true},
		{"08.0.95 beta", Version{8, 0, 95, "08.0.95 beta"}, true},
		{"", Version{}, false},
		{"garbage", Version{}, false},
		{"8.0", Version{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseVersion(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseVersion(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// Firmware reports the device's version string verbatim, parseable or not;
// "unknown" is reserved for a device that reported no version at all.
func TestFirmware(t *testing.T) {
	tests := []struct {
		in, want string
		known    bool
	}{
		{"08.0.95mT213", "08.0.95mT213", true},
		{"08.0.30uT311", "08.0.30uT311", true},
		{"FastIron-weird-build", "FastIron-weird-build", false},
		{"8.0", "8.0", false},
		{"", "unknown", false},
	}
	for _, tt := range tests {
		d := Detect(tt.in, parser.ImageTypeSwitch)
		if got := d.Firmware(); got != tt.want || d.Known != tt.known {
			t.Errorf("Detect(%q): Firmware() = %q, Known = %v; want %q, %v", tt.in, got, d.Known, tt.want, tt.known)
		}
	}

	// The router-image guard on an unparseable version names the raw string.
	d := Detect("FastIron-weird-build", parser.ImageTypeSwitch)
	if err := d.Why(CapVEInterfaces); !strings.Contains(err, "firmware FastIron-weird-build") {
		t.Errorf("Why(CapVEInterfaces) = %q, want it to name the raw version", err)
	}

	// Observe keeps an unparseable config version for diagnostics too.
	o := Dialect{}.Observe(&parser.RunningConfig{Global: parser.GlobalSettings{Version: "odd"}})
	if o.Known || o.Firmware() != "odd" {
		t.Errorf("Observe(odd): Known = %v, Firmware() = %q", o.Known, o.Firmware())
	}
}

func TestVersionFamily(t *testing.T) {
	v, _ := ParseVersion("08.0.30uT311")
	if got := v.Family(); got != "08.0.30" {
		t.Errorf("Family() = %q, want 08.0.30", got)
	}
}

// The capability matrix for every real device in testdata, plus an
// unparseable version (permissive for features, 08.0.9x for syntax forms).
func TestDetectHas(t *testing.T) {
	type caps map[Capability]bool
	tests := []struct {
		name    string
		version string
		image   parser.ImageType
		known   bool
		want    caps
	}{
		{"7250-24 router 08.0.95", "08.0.95mT213", parser.ImageTypeRouter, true, caps{
			CapVEInterfaces: true, CapRouterInterface: true, CapGlobalSTP: true, CapManager: true,
			CapGlobalManagementIP: false, CapOpticalMonitor: true, CapDualMode: false, CapLegacyDHCPClient: false, CapVlanConfigAllTagged: true,
		}},
		{"7150-24/C12P router 08.0.95", "08.0.95qT213", parser.ImageTypeRouter, true, caps{
			CapVEInterfaces: true, CapRouterInterface: true, CapGlobalSTP: true, CapManager: true,
			CapGlobalManagementIP: false, CapOpticalMonitor: true, CapDualMode: false, CapLegacyDHCPClient: false, CapVlanConfigAllTagged: true,
		}},
		{"C10ZP switch 08.0.95", "08.0.95rT211", parser.ImageTypeSwitch, true, caps{
			CapVEInterfaces: false, CapRouterInterface: false, CapGlobalSTP: false, CapManager: true,
			CapGlobalManagementIP: true, CapOpticalMonitor: true, CapDualMode: false, CapLegacyDHCPClient: false, CapVlanConfigAllTagged: true,
		}},
		{"6430 switch 08.0.30", "08.0.30uT311", parser.ImageTypeSwitch, true, caps{
			CapVEInterfaces: false, CapRouterInterface: false, CapGlobalSTP: false, CapManager: false,
			CapGlobalManagementIP: true, CapOpticalMonitor: true, CapDualMode: true, CapLegacyDHCPClient: true, CapVlanConfigAllTagged: false,
		}},
		{"6450 router 08.0.30", "08.0.30uT313", parser.ImageTypeRouter, true, caps{
			CapVEInterfaces: true, CapRouterInterface: true, CapGlobalSTP: true, CapManager: false,
			CapGlobalManagementIP: false, CapOpticalMonitor: true, CapDualMode: true, CapLegacyDHCPClient: false, CapVlanConfigAllTagged: false,
		}},
		{"unparseable version, unknown image", "garbage", parser.ImageTypeUnknown, false, caps{
			CapVEInterfaces: true, CapRouterInterface: true, CapGlobalSTP: true, CapManager: true,
			CapGlobalManagementIP: true, CapOpticalMonitor: true, CapDualMode: false, CapLegacyDHCPClient: false, CapVlanConfigAllTagged: false,
		}},
		{"empty version, empty image", "", "", false, caps{
			CapVEInterfaces: true, CapRouterInterface: true, CapGlobalSTP: true, CapManager: true,
			CapGlobalManagementIP: true, CapOpticalMonitor: true, CapDualMode: false, CapLegacyDHCPClient: false, CapVlanConfigAllTagged: false,
		}},
		// The two axes are independent: a structurally detected switch image
		// still blocks router-only features when the version is unparseable.
		{"unparseable version, switch image", "", parser.ImageTypeSwitch, false, caps{
			CapVEInterfaces: false, CapRouterInterface: false, CapGlobalSTP: false, CapManager: true,
			CapGlobalManagementIP: true, CapOpticalMonitor: true, CapDualMode: false, CapLegacyDHCPClient: false, CapVlanConfigAllTagged: false,
		}},
		// And an old version with an undetected image does not guess the
		// image-dependent DHCP spelling.
		{"08.0.30 unknown image", "08.0.30uT31x", parser.ImageTypeUnknown, true, caps{
			CapVEInterfaces: true, CapGlobalManagementIP: true, CapManager: false, CapDualMode: true, CapLegacyDHCPClient: false, CapVlanConfigAllTagged: false,
		}},
		// The bulk tagging command arrived in 08.0.50 and does not depend on
		// the image.
		{"08.0.50 first vlan-config release", "08.0.50T211", parser.ImageTypeUnknown, true, caps{
			CapVlanConfigAllTagged: true, CapDualMode: true,
		}},
		{"08.0.40 last release without vlan-config", "08.0.40T311", parser.ImageTypeSwitch, true, caps{
			CapVlanConfigAllTagged: false, CapDualMode: true,
		}},
		{"09.0.10 router", "09.0.10aT213", parser.ImageTypeRouter, true, caps{
			CapVlanConfigAllTagged: true, CapDualMode: false,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Detect(tt.version, tt.image)
			if d.Known != tt.known {
				t.Errorf("Known = %v, want %v", d.Known, tt.known)
			}
			for c, want := range tt.want {
				if got := d.Has(c); got != want {
					t.Errorf("Has(%s) = %v, want %v", c, got, want)
				}
				if IsSyntaxForm(c) {
					// Forms select a spelling; they are never "missing".
					continue
				}
				if why := d.Why(c); (why == "") != want {
					t.Errorf("Why(%s) = %q; expected empty iff capability present", c, why)
				}
			}
		})
	}
}

func TestEveryCapabilityRegistered(t *testing.T) {
	for _, c := range Capabilities() {
		if _, ok := table[c]; !ok {
			t.Errorf("capability %s listed but not in table", c)
		}
	}
	if len(Capabilities()) != len(table) {
		t.Errorf("Capabilities() lists %d, table has %d", len(Capabilities()), len(table))
	}
	if !(Dialect{}).Has(Capability("not_registered")) {
		t.Error("unregistered capability must be permissive")
	}
	// Syntax forms must be false and feature capabilities true for an
	// unknown dialect — the never-block rule.
	var unknown Dialect
	for _, c := range Capabilities() {
		if got, want := unknown.Has(c), !IsSyntaxForm(c); got != want {
			t.Errorf("unknown dialect Has(%s) = %v, want %v", c, got, want)
		}
	}
}

func TestWhy(t *testing.T) {
	d := Detect("08.0.95rT211", parser.ImageTypeSwitch)
	want := "is only available on the FastIron router image, but this device runs the switch image (firmware 08.0.95rT211)"
	if got := d.Why(CapVEInterfaces); got != want {
		t.Errorf("Why(VE) = %q, want %q", got, want)
	}
	if Title(CapVEInterfaces) != "Router image required" {
		t.Errorf("Title(VE) = %q", Title(CapVEInterfaces))
	}
	if !strings.Contains(Docs(CapGlobalSTP), "#router-image-vs-switch-image") {
		t.Errorf("Docs(GlobalSTP) = %q", Docs(CapGlobalSTP))
	}

	// The mirror image: the global management address exists only on the
	// switch image, and the explanation names the router-image alternative.
	router := Detect("08.0.95mT213", parser.ImageTypeRouter)
	if got := router.Why(CapGlobalManagementIP); !strings.Contains(got, "router image (firmware 08.0.95mT213)") ||
		!strings.Contains(got, "icx_interface_ve") {
		t.Errorf("Why(GlobalManagementIP) = %q", got)
	}
	if Title(CapGlobalManagementIP) != "Switch image required" {
		t.Errorf("Title(GlobalManagementIP) = %q", Title(CapGlobalManagementIP))
	}
	if !strings.Contains(Docs(CapGlobalManagementIP), "#router-image-vs-switch-image") {
		t.Errorf("Docs(GlobalManagementIP) = %q", Docs(CapGlobalManagementIP))
	}
	if d.Why(CapGlobalManagementIP) != "" {
		t.Errorf("switch image must have CapGlobalManagementIP, got Why = %q", d.Why(CapGlobalManagementIP))
	}

	if got := Detect("", parser.ImageTypeSwitch).Why(CapGlobalSTP); !strings.Contains(got, "firmware unknown") {
		t.Errorf("unknown firmware should be reported as such, got %q", got)
	}

	old := Detect("08.0.30uT311", parser.ImageTypeSwitch)
	if got := old.Why(CapManager); !strings.Contains(got, "FastIron 08.0.30") || !strings.Contains(got, "08.0.80") {
		t.Errorf("Why(Manager) = %q", got)
	}
	if !strings.Contains(Docs(CapManager), "#firmware-dialects-and-capabilities") {
		t.Errorf("Docs(Manager) = %q", Docs(CapManager))
	}
}

func TestDHCPClientDisable(t *testing.T) {
	tests := []struct {
		name            string
		version         string
		image           parser.ImageType
		disable, enable string
	}{
		{"08.0.95 router", "08.0.95mT213", parser.ImageTypeRouter, "ip dhcp-client disable", "no ip dhcp-client disable"},
		{"08.0.95 switch", "08.0.95rT211", parser.ImageTypeSwitch, "ip dhcp-client disable", "no ip dhcp-client disable"},
		{"08.0.30 router (6450)", "08.0.30uT313", parser.ImageTypeRouter, "ip dhcp-client disable", "no ip dhcp-client disable"},
		{"08.0.30 switch (6430)", "08.0.30uT311", parser.ImageTypeSwitch, "no ip dhcp-client enable", "ip dhcp-client enable"},
		{"unknown", "garbage", parser.ImageTypeUnknown, "ip dhcp-client disable", "no ip dhcp-client disable"},
	}
	for _, tt := range tests {
		d := Detect(tt.version, tt.image)
		if got := d.DHCPClientDisable(true); got != tt.disable {
			t.Errorf("%s: DHCPClientDisable(true) = %q, want %q", tt.name, got, tt.disable)
		}
		if got := d.DHCPClientDisable(false); got != tt.enable {
			t.Errorf("%s: DHCPClientDisable(false) = %q, want %q", tt.name, got, tt.enable)
		}
	}
}

func TestDualModeRendering(t *testing.T) {
	d := Detect("08.0.30uT311", parser.ImageTypeSwitch)
	if got := d.DualMode(102); got != "dual-mode 102" {
		t.Errorf("DualMode(102) = %q", got)
	}
	if got := d.DualMode(1); got != "dual-mode" {
		t.Errorf("DualMode(1) = %q", got)
	}
	if got := d.NoDualMode(); got != "no dual-mode" {
		t.Errorf("NoDualMode() = %q", got)
	}
}

// The dual-mode boundary: every release before 08.0.80 has it; nothing from
// 08.0.80 on does; unparseable strings never claim it.
func TestDualModeBoundary(t *testing.T) {
	tests := []struct {
		ver  string
		want bool
	}{
		{"08.0.30uT311", true},
		{"08.0.70bT213", true},
		{"08.0.79T213", true},
		{"08.0.80T213", false},
		{"08.0.90jT213", false},
		{"08.0.95qT213", false},
		{"09.0.10aT213", false},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		if got := Detect(tt.ver, parser.ImageTypeUnknown).Has(CapDualMode); got != tt.want {
			t.Errorf("Has(CapDualMode) for %q = %v, want %v", tt.ver, got, tt.want)
		}
	}
}

func loadFixture(t *testing.T, name string) *parser.RunningConfig {
	t.Helper()
	b, err := os.ReadFile("../../testdata/config." + name + ".txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	c, err := parser.ParseRunningConfig(string(b))
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return c
}

func TestFromConfigFixtures(t *testing.T) {
	tests := []struct {
		fixture  string
		image    parser.ImageType
		dualMode bool
		dhcp     string
	}{
		{"7250-24", parser.ImageTypeRouter, false, "ip dhcp-client disable"},
		{"7150-24", parser.ImageTypeRouter, false, "ip dhcp-client disable"},
		{"7150-c12", parser.ImageTypeRouter, false, "ip dhcp-client disable"},
		{"7150-c10zp", parser.ImageTypeSwitch, false, "ip dhcp-client disable"},
		{"6430-c12", parser.ImageTypeSwitch, true, "no ip dhcp-client enable"},
		{"6450-c12pd", parser.ImageTypeRouter, true, "ip dhcp-client disable"},
	}
	for _, tt := range tests {
		d := FromConfig(loadFixture(t, tt.fixture))
		if !d.Known {
			t.Errorf("%s: expected a known dialect", tt.fixture)
		}
		if d.Image != tt.image {
			t.Errorf("%s: Image = %q, want %q", tt.fixture, d.Image, tt.image)
		}
		if got := d.Has(CapDualMode); got != tt.dualMode {
			t.Errorf("%s: Has(CapDualMode) = %v, want %v", tt.fixture, got, tt.dualMode)
		}
		if got := d.DHCPClientDisable(true); got != tt.dhcp {
			t.Errorf("%s: DHCPClientDisable(true) = %q, want %q", tt.fixture, got, tt.dhcp)
		}
	}
}

// A config with no parseable version but existing dual-mode lines is from
// firmware that has the command; Observe must keep a Known Dialect's version.
func TestObserve(t *testing.T) {
	c, err := parser.ParseRunningConfig("interface ethernet 1/1/1\n dual-mode  20\n!\n")
	if err != nil {
		t.Fatal(err)
	}
	if !FromConfig(c).Has(CapDualMode) {
		t.Error("dual-mode lines must prove CapDualMode")
	}

	c, err = parser.ParseRunningConfig("interface ethernet 1/1/1\n port-name x\n!\n")
	if err != nil {
		t.Fatal(err)
	}
	if FromConfig(c).Has(CapDualMode) {
		t.Error("no version and no dual-mode lines must not claim CapDualMode")
	}

	known := Detect("08.0.95qT213", parser.ImageTypeRouter)
	obs := known.Observe(loadFixture(t, "6430-c12"))
	if obs.Version.Raw != "08.0.95qT213" || obs.Image != parser.ImageTypeRouter {
		t.Errorf("Observe must not override a known dialect, got %+v", obs)
	}

	unknown := Detect("", parser.ImageTypeUnknown).Observe(loadFixture(t, "6430-c12"))
	if !unknown.Known || unknown.Version.Raw != "08.0.30uT311" || unknown.Image != parser.ImageTypeSwitch {
		t.Errorf("Observe must fill an unknown dialect from the config, got %+v", unknown)
	}
	if Detect("", "").Observe(nil).Known {
		t.Error("Observe(nil) must be a no-op")
	}
}

// The bulk tagging renderer is a fixed spelling; the capability decides
// whether it may be used at all.
func TestVlanConfigAddAllTagged(t *testing.T) {
	d := Detect("08.0.95mT213", parser.ImageTypeRouter)
	if got := d.VlanConfigAddAllTagged(); got != "vlan-config add all-tagged" {
		t.Errorf("VlanConfigAddAllTagged() = %q", got)
	}
	if !IsSyntaxForm(CapVlanConfigAllTagged) {
		t.Error("CapVlanConfigAllTagged must be a syntax form: unknown firmware falls back to per-VLAN tagging")
	}
	// A running config with a parseable version proves the capability even
	// when the provider could not detect one at connect time.
	o := Dialect{}.Observe(&parser.RunningConfig{Global: parser.GlobalSettings{Version: "08.0.95qT213"}})
	if !o.Has(CapVlanConfigAllTagged) {
		t.Error("Observe(08.0.95) must enable CapVlanConfigAllTagged")
	}
}
