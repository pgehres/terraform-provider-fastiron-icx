package resource

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestGuardedRawCommand(t *testing.T) {
	tests := []struct {
		cmd     string
		blocked bool
	}{
		{"hostname lab-core", true},
		{"no hostname", true},
		{"  HOSTNAME x", true},
		{"hostn x", true},
		{"ip address 10.0.20.5 255.255.255.0", true},
		{"no ip address 10.0.20.5 255.255.255.0", true},
		{"ip addr 10.0.20.5/24", true},
		{"ip default-gateway 10.0.20.1", true},
		{"no ip default-gateway 10.0.20.1", true},
		{"ip default-g 10.0.20.1", true},
		// Unrelated commands that share a prefix stay allowed.
		{"ip default-network 10.0.0.0", false},
		{"ip access-group 101 in", false},
		{"ip dns server-address 1.1.1.1", false},
		{"ip route 0.0.0.0/0 10.0.20.1", false},
		{"ip dhcp-client enable", false},
		{"spanning-tree", false},
		{"no spanning-tree", false},
		{"port-name uplink", false},
		{"", false},
		{"no", false},
	}
	for _, tt := range tests {
		if got := guardedRawCommand(tt.cmd) != ""; got != tt.blocked {
			t.Errorf("guardedRawCommand(%q) blocked = %v, want %v", tt.cmd, got, tt.blocked)
		}
	}
}

// Every raw command list that is sent to the switch carries the guard (and
// the no-newline check); icx_raw_config's expect_in_config is only compared
// against the running config, so it may mention these lines.
func TestRawConfigListsCarryGuard(t *testing.T) {
	type listAttr interface{ ListValidators() []validator.List }
	cases := []struct {
		res     resource.Resource
		attr    string
		guarded bool
	}{
		{NewVLANResource(), "raw_config", true},
		{NewInterfaceEthernetResource(), "raw_config", true},
		{NewInterfaceVEResource(), "raw_config", true},
		{NewRawConfigResource(), "commands", true},
		{NewRawConfigResource(), "destroy_commands", true},
		{NewRawConfigResource(), "expect_in_config", false},
	}
	run := func(vs []validator.List, elems ...string) bool {
		values := make([]attr.Value, len(elems))
		for i, e := range elems {
			values[i] = types.StringValue(e)
		}
		req := validator.ListRequest{ConfigValue: types.ListValueMust(types.StringType, values)}
		var resp validator.ListResponse
		for _, v := range vs {
			v.ValidateList(context.Background(), req, &resp)
		}
		return !resp.Diagnostics.HasError()
	}
	for _, c := range cases {
		var md resource.MetadataResponse
		c.res.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "icx"}, &md)
		var sr resource.SchemaResponse
		c.res.Schema(context.Background(), resource.SchemaRequest{}, &sr)
		vs := sr.Schema.Attributes[c.attr].(listAttr).ListValidators()
		name := md.TypeName + "." + c.attr
		if !run(vs, "spanning-tree") {
			t.Errorf("%s: an ordinary command must pass", name)
		}
		if run(vs, "spanning-tree\nhostname x") {
			t.Errorf("%s: embedded newline must be rejected", name)
		}
		for _, bad := range []string{"hostname x", "no ip address 10.0.20.5 255.255.255.0", "ip default-gateway 10.0.20.1"} {
			if got := !run(vs, "spanning-tree", bad); got != c.guarded {
				t.Errorf("%s: %q rejected = %v, want %v", name, bad, got, c.guarded)
			}
		}
	}
}
