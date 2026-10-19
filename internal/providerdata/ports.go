package providerdata

import (
	"sort"
	"sync"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// PortRegistry records which ethernet ports the icx_interface_ethernet
// resources planned through one provider instance have claimed, so that a
// second resource claiming the same port is rejected at plan time instead of
// the two clobbering each other on every apply.
//
// Lifetime: a registry is created in the provider's Configure, and Terraform
// configures a fresh provider instance for every graph walk (plan, apply, and
// the re-plan it runs before each apply), so claims never leak from one
// operation into the next and a refresh-only run — which plans nothing —
// registers nothing.
//
// Identity: the plugin protocol does not tell a resource its own address, so
// the registry cannot tell "the same resource planned again" from "a second
// resource" by name. Within one walk Terraform plans each resource instance
// once, except that a replacement is planned twice (against the prior state and
// against a null prior state) with the same configuration. Entries therefore
// record the claimant's configuration, and a later claim on the same port
// conflicts only when the two configurations are *definitely* different: at
// least one attribute whose value is fully known on both sides and unequal.
// Unknown values (references resolved only at apply) match anything, so the
// same resource planned again, or planned later with more of its values known,
// is never a conflict. The price is that two resources with byte-identical
// configuration on one port go undetected; those also converge to the same
// device state and do not churn.
type PortRegistry struct {
	mu    sync.Mutex
	ports map[string]map[string]tftypes.Value
}

// NewPortRegistry returns an empty registry.
func NewPortRegistry() *PortRegistry {
	return &PortRegistry{ports: make(map[string]map[string]tftypes.Value)}
}

// Claim records that a resource whose configuration attributes are attrs
// manages port. It returns the sorted names of the attributes on which an
// earlier claim for the same port definitely differs, or nil when the port was
// free or the earlier claim is compatible — in which case the registry keeps,
// per attribute, whichever value is more fully known. A nil registry accepts
// every claim.
func (r *PortRegistry) Claim(port string, attrs map[string]tftypes.Value) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	prev, seen := r.ports[port]
	if !seen {
		merged := make(map[string]tftypes.Value, len(attrs))
		for k, v := range attrs {
			merged[k] = v
		}
		r.ports[port] = merged
		return nil
	}

	var conflicts []string
	for name, v := range attrs {
		old, ok := prev[name]
		if !ok {
			continue
		}
		if !old.IsFullyKnown() || !v.IsFullyKnown() {
			continue
		}
		if !old.Equal(v) {
			conflicts = append(conflicts, name)
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return conflicts
	}

	// Compatible: remember the more fully known value of each attribute so a
	// later, genuinely different claim is still caught.
	for name, v := range attrs {
		if old, ok := prev[name]; !ok || (!old.IsFullyKnown() && v.IsFullyKnown()) {
			prev[name] = v
		}
	}
	return nil
}
