package resource

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// listToStringSlice converts a Terraform List to a Go string slice.
func listToStringSlice(ctx context.Context, list types.List, diags *diag.Diagnostics) []string {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	var result []string
	diags.Append(list.ElementsAs(ctx, &result, false)...)
	return result
}

// setToInt64Slice converts a Terraform Set of Int64 to a Go int64 slice.
func setToInt64Slice(ctx context.Context, set types.Set, diags *diag.Diagnostics) []int64 {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	var result []int64
	diags.Append(set.ElementsAs(ctx, &result, false)...)
	return result
}

// int64SliceToSet converts a Go int64 slice to a Terraform Set of Int64.
func int64SliceToSet(ctx context.Context, slice []int64, diags *diag.Diagnostics) types.Set {
	elements := make([]attr.Value, len(slice))
	for i, v := range slice {
		elements[i] = types.Int64Value(v)
	}
	set, d := types.SetValue(types.Int64Type, elements)
	diags.Append(d...)
	return set
}

// stringSliceContains returns true if the slice contains the string.
func stringSliceContains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// quotePortName returns the port name quoted if it contains spaces.
//
// Schema validation on port_name already rejects double-quote characters, so
// under normal operation this function will never see an embedded quote.
// As a defensive measure, any double-quote characters that slip through
// (e.g. from unvalidated call sites) are stripped before the name is
// interpolated into the CLI command.
func quotePortName(name string) string {
	if name == "" {
		return name
	}

	// Strip embedded double quotes — they would break the quoted form of the
	// command. The schema validator rejects them at plan time, so this path
	// should never be reached in normal use.
	hasQuote := false
	for _, c := range name {
		if c == '"' {
			hasQuote = true
			break
		}
	}
	if hasQuote {
		clean := make([]byte, 0, len(name))
		for i := 0; i < len(name); i++ {
			if name[i] != '"' {
				clean = append(clean, name[i])
			}
		}
		name = string(clean)
	}

	for _, c := range name {
		if c == ' ' {
			return "\"" + name + "\""
		}
	}
	return name
}
