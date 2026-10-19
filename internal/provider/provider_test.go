package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
)

// native_bulk_tagging is an experimental opt-in: optional, never required,
// and unset means false (the per-VLAN expansion).
func TestProviderSchema_NativeBulkTaggingIsOptIn(t *testing.T) {
	var resp provider.SchemaResponse
	New("test")().Schema(context.Background(), provider.SchemaRequest{}, &resp)
	attr, ok := resp.Schema.Attributes["native_bulk_tagging"].(schema.BoolAttribute)
	if !ok {
		t.Fatalf("native_bulk_tagging missing or not a bool: %T", resp.Schema.Attributes["native_bulk_tagging"])
	}
	if !attr.Optional || attr.Required {
		t.Errorf("native_bulk_tagging must be optional, got Optional=%v Required=%v", attr.Optional, attr.Required)
	}
	var m FastIronICXProviderModel
	if m.NativeBulkTagging.ValueBool() {
		t.Error("an unset native_bulk_tagging must read as false")
	}
}
