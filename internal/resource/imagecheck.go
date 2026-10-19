package resource

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

// requireRouterImage records an error diagnostic and returns false when the
// device is known to run the FastIron switch image, which lacks the Layer 3
// commands behind router-image-only features. ImageTypeUnknown passes, so a
// device whose image could not be detected is never blocked — the switch's
// own CLI error still backstops those.
//
// The same helper backs both the plan-time checks (each guarded resource's
// ModifyPlan, so a misconfiguration against a detected switch image fails
// `terraform plan` before any resource is applied) and the apply-time CRUD
// guards, which remain as a backstop for paths that skip ModifyPlan. Both
// therefore emit identical "Router image required" diagnostics.
func requireRouterImage(imageType parser.ImageType, firmwareVersion, feature string, diags *diag.Diagnostics) bool {
	if imageType != parser.ImageTypeSwitch {
		return true
	}

	version := firmwareVersion
	if version == "" {
		version = "unknown"
	}
	diags.AddError(
		"Router image required",
		fmt.Sprintf("%s is only available on the FastIron router image, but this device runs "+
			"the switch image (firmware %s). No commands were sent to the switch. "+
			"See the \"Router Image vs. Switch Image\" section of the provider documentation "+
			"(docs/index.md#router-image-vs-switch-image).", feature, version),
	)
	return false
}
