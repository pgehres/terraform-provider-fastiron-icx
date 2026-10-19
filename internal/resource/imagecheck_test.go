package resource

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
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

// The guard must only ever block a device positively identified as running
// the switch image. Router, unknown, and an unset (empty) image type all pass
// — an undetectable image is never blocked.
func TestRequireRouterImage(t *testing.T) {
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var diags diag.Diagnostics
			ok := requireRouterImage(tt.imageType, "08.0.95rT211", "test feature", &diags)
			if ok != tt.wantOK {
				t.Errorf("requireRouterImage(%q) = %v, want %v", tt.imageType, ok, tt.wantOK)
			}
			if got := hasRouterImageError(diags); got == tt.wantOK {
				t.Errorf("requireRouterImage(%q): error recorded = %v, want %v", tt.imageType, got, !tt.wantOK)
			}
			if tt.wantOK && diags.HasError() {
				t.Errorf("expected no diagnostics, got: %v", diags)
			}
		})
	}
}

func TestRequireRouterImage_UnknownFirmwareVersion(t *testing.T) {
	var diags diag.Diagnostics
	requireRouterImage(parser.ImageTypeSwitch, "", "test feature", &diags)
	if len(diags.Errors()) != 1 {
		t.Fatalf("expected one error, got: %v", diags)
	}
	if !strings.Contains(diags.Errors()[0].Detail(), "firmware unknown") {
		t.Errorf("expected detail to report firmware unknown, got: %q", diags.Errors()[0].Detail())
	}
}
