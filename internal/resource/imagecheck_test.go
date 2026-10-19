package resource

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

// hasRouterImageError reports whether diags contains the guard's error.
func hasRouterImageError(diags diag.Diagnostics) bool {
	for _, d := range diags.Errors() {
		if strings.Contains(d.Summary(), "Router image required") {
			return true
		}
	}
	return false
}

// The router-image capabilities must only ever block a device positively
// identified as running the switch image. Router, unknown, and an unset
// (empty) image type all pass — an undetectable image is never blocked.
func TestRequireCapability_RouterImage(t *testing.T) {
	tests := []struct {
		name      string
		imageType parser.ImageType
		wantOK    bool
	}{
		{"router passes", parser.ImageTypeRouter, true},
		{"unknown passes", parser.ImageTypeUnknown, true},
		{"empty passes", parser.ImageType(""), true},
		{"switch fails", parser.ImageTypeSwitch, false},
	}

	for _, c := range []dialect.Capability{dialect.CapVEInterfaces, dialect.CapRouterInterface, dialect.CapGlobalSTP} {
		for _, tt := range tests {
			t.Run(string(c)+"/"+tt.name, func(t *testing.T) {
				var diags diag.Diagnostics
				ok := requireCapability(dialect.Detect("08.0.95rT211", tt.imageType), c, "test feature", &diags)
				if ok != tt.wantOK {
					t.Errorf("requireCapability(%q) = %v, want %v", tt.imageType, ok, tt.wantOK)
				}
				if got := hasRouterImageError(diags); got == tt.wantOK {
					t.Errorf("requireCapability(%q): error recorded = %v, want %v", tt.imageType, got, !tt.wantOK)
				}
				if tt.wantOK && diags.HasError() {
					t.Errorf("expected no diagnostics, got: %v", diags)
				}
			})
		}
	}
}

// The router-image wording is unchanged from the pre-dialect guard.
func TestRequireCapability_RouterImageWording(t *testing.T) {
	var diags diag.Diagnostics
	requireCapability(dialect.Detect("08.0.95rT211", parser.ImageTypeSwitch), dialect.CapVEInterfaces, "icx_interface_ve", &diags)
	if len(diags.Errors()) != 1 {
		t.Fatalf("expected one error, got: %v", diags)
	}
	want := "icx_interface_ve is only available on the FastIron router image, but this device runs " +
		"the switch image (firmware 08.0.95rT211). No commands were sent to the switch. " +
		"See the \"Router Image vs. Switch Image\" section of the provider documentation " +
		"(docs/index.md#router-image-vs-switch-image)."
	if got := diags.Errors()[0].Detail(); got != want {
		t.Errorf("detail:\n got %q\nwant %q", got, want)
	}
}

func TestRequireCapability_UnknownFirmwareVersion(t *testing.T) {
	var diags diag.Diagnostics
	requireCapability(dialect.Detect("", parser.ImageTypeSwitch), dialect.CapGlobalSTP, "test feature", &diags)
	if len(diags.Errors()) != 1 {
		t.Fatalf("expected one error, got: %v", diags)
	}
	if !strings.Contains(diags.Errors()[0].Detail(), "firmware unknown") {
		t.Errorf("expected detail to report firmware unknown, got: %q", diags.Errors()[0].Detail())
	}
}

// Firmware-version capabilities: manager commands are refused on 08.0.30 and
// pass on 08.0.9x and on an unparseable version.
func TestRequireCapability_Manager(t *testing.T) {
	tests := []struct {
		version string
		wantOK  bool
	}{
		{"08.0.95qT213", true},
		{"08.0.95rT211", true},
		{"08.0.30uT311", false},
		{"08.0.30uT313", false},
		{"garbage", true},
	}
	for _, tt := range tests {
		var diags diag.Diagnostics
		ok := requireCapability(dialect.Detect(tt.version, parser.ImageTypeUnknown), dialect.CapManager, "The manager_registrar attribute of icx_system", &diags)
		if ok != tt.wantOK {
			t.Errorf("%s: requireCapability = %v, want %v", tt.version, ok, tt.wantOK)
		}
		if !tt.wantOK {
			e := diags.Errors()[0]
			if e.Summary() != "Not supported by this firmware" ||
				!strings.Contains(e.Detail(), "The manager_registrar attribute of icx_system is not available on FastIron 08.0.30") ||
				!strings.Contains(e.Detail(), "#firmware-dialects-and-capabilities") {
				t.Errorf("%s: unexpected diagnostic %q / %q", tt.version, e.Summary(), e.Detail())
			}
		}
	}
}
