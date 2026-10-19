package resource

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
)

// requireCapability records an error diagnostic and returns false when the
// device's dialect positively lacks capability c (see internal/dialect). A
// dialect that could not be detected never blocks — the switch's own CLI
// error still backstops those.
//
// The same helper backs both the plan-time checks (each guarded resource's
// ModifyPlan, so a misconfiguration fails `terraform plan` before any
// resource is applied) and the apply-time CRUD guards, which remain as a
// backstop for paths that skip ModifyPlan. Both therefore emit identical
// diagnostics; for the router-image capabilities that is the
// "Router image required" error.
func requireCapability(d dialect.Dialect, c dialect.Capability, feature string, diags *diag.Diagnostics) bool {
	if d.Has(c) {
		return true
	}
	diags.AddError(
		dialect.Title(c),
		fmt.Sprintf("%s %s. No commands were sent to the switch. %s", feature, d.Why(c), dialect.Docs(c)),
	)
	return false
}
