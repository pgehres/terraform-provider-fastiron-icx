package resource

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// setToStringSlice converts a Terraform Set to a Go string slice.
func setToStringSlice(ctx context.Context, set types.Set, diags *diag.Diagnostics) []string {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	var result []string
	diags.Append(set.ElementsAs(ctx, &result, false)...)
	return result
}

// listToStringSlice converts a Terraform List to a Go string slice.
func listToStringSlice(ctx context.Context, list types.List, diags *diag.Diagnostics) []string {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	var result []string
	diags.Append(list.ElementsAs(ctx, &result, false)...)
	return result
}

// stringSliceToSet converts a Go string slice to a Terraform Set.
func stringSliceToSet(ctx context.Context, slice []string, diags *diag.Diagnostics) types.Set {
	elements := make([]attr.Value, len(slice))
	for i, s := range slice {
		elements[i] = types.StringValue(s)
	}
	set, d := types.SetValue(types.StringType, elements)
	diags.Append(d...)
	return set
}

// stringSliceToList converts a Go string slice to a Terraform List.
func stringSliceToList(ctx context.Context, slice []string, diags *diag.Diagnostics) types.List {
	elements := make([]attr.Value, len(slice))
	for i, s := range slice {
		elements[i] = types.StringValue(s)
	}
	list, d := types.ListValue(types.StringType, elements)
	diags.Append(d...)
	return list
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

// difference returns elements in a that are not in b.
func difference(a, b []string) []string {
	bSet := make(map[string]bool, len(b))
	for _, s := range b {
		bSet[s] = true
	}
	var result []string
	for _, s := range a {
		if !bSet[s] {
			result = append(result, s)
		}
	}
	return result
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
func quotePortName(name string) string {
	if name == "" {
		return name
	}
	for _, c := range name {
		if c == ' ' {
			return "\"" + name + "\""
		}
	}
	return name
}
