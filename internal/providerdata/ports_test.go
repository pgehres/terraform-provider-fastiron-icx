package providerdata

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func num(v interface{}) tftypes.Value { return tftypes.NewValue(tftypes.Number, v) }
func str(v interface{}) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

func TestPortRegistry_Claim(t *testing.T) {
	r := NewPortRegistry()

	if got := r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(10), "port_name": str("a")}); got != nil {
		t.Fatalf("first claim: %v", got)
	}
	// Identical claim: same resource re-planned.
	if got := r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(10), "port_name": str("a")}); got != nil {
		t.Fatalf("identical claim: %v", got)
	}
	// Unknown on one side matches anything.
	if got := r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(tftypes.UnknownValue), "port_name": str("a")}); got != nil {
		t.Fatalf("unknown claim: %v", got)
	}
	// Null is a known value, distinct from 10.
	if got := r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(nil), "port_name": str("b")}); !reflect.DeepEqual(got, []string{"port_name", "untagged_vlan"}) {
		t.Fatalf("different claim = %v, want both attributes, sorted", got)
	}
	// A conflicting claim leaves the registry's record untouched.
	if got := r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(10), "port_name": str("a")}); got != nil {
		t.Fatalf("original claim after a rejected one: %v", got)
	}
	// Other ports are independent.
	if got := r.Claim("1/1/2", map[string]tftypes.Value{"untagged_vlan": num(99)}); got != nil {
		t.Fatalf("other port: %v", got)
	}
}

// An attribute first seen unknown is upgraded to its known value, so a later
// claim is compared against the resolved value.
func TestPortRegistry_ClaimUpgradesUnknown(t *testing.T) {
	r := NewPortRegistry()
	r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(tftypes.UnknownValue)})
	if got := r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(10)}); got != nil {
		t.Fatalf("resolving claim: %v", got)
	}
	if got := r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(11)}); !reflect.DeepEqual(got, []string{"untagged_vlan"}) {
		t.Fatalf("claim after resolution = %v, want conflict", got)
	}
}

func TestPortRegistry_NilAcceptsEverything(t *testing.T) {
	var r *PortRegistry
	if got := r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(10)}); got != nil {
		t.Fatal(got)
	}
	if got := r.Claim("1/1/1", map[string]tftypes.Value{"untagged_vlan": num(11)}); got != nil {
		t.Fatal(got)
	}
}
