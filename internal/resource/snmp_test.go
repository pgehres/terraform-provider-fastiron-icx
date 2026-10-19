package resource

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

func community(str, mode, acl string) SNMPCommunityModel {
	c := SNMPCommunityModel{String: types.StringValue(str), Mode: types.StringValue(mode), ACL: types.StringNull()}
	if acl != "" {
		c.ACL = types.StringValue(acl)
	}
	return c
}

// snmpModel builds a model; comms == nil is a null list, "" strings are null.
func snmpModel(comms []SNMPCommunityModel, contact, location string) *SNMPResourceModel {
	m := &SNMPResourceModel{
		ID:          types.StringValue("snmp"),
		Communities: types.ListNull(types.ObjectType{AttrTypes: snmpCommunityAttrTypes}),
		Contact:     types.StringNull(),
		Location:    types.StringNull(),
	}
	if comms != nil {
		list, diags := types.ListValueFrom(context.Background(), types.ObjectType{AttrTypes: snmpCommunityAttrTypes}, comms)
		if diags.HasError() {
			panic(diags)
		}
		m.Communities = list
	}
	if contact != "" {
		m.Contact = types.StringValue(contact)
	}
	if location != "" {
		m.Location = types.StringValue(location)
	}
	return m
}

func snmpStateAndPlan(t *testing.T, r *SNMPResource, state, plan *SNMPResourceModel) (tfsdk.State, tfsdk.Plan) {
	t.Helper()
	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema error: %v", schemaResp.Diagnostics)
	}
	s := tfsdk.State{Schema: schemaResp.Schema}
	if state != nil {
		if diags := s.Set(ctx, state); diags.HasError() {
			t.Fatalf("state: %v", diags)
		}
	}
	p := tfsdk.Plan{Schema: schemaResp.Schema}
	if plan != nil {
		if diags := p.Set(ctx, plan); diags.HasError() {
			t.Fatalf("plan: %v", diags)
		}
	}
	return s, p
}

func TestSNMPCreate(t *testing.T) {
	mock := sshclient.NewMockClient("")
	r := &SNMPResource{client: mock}
	plan := snmpModel([]SNMPCommunityModel{community("r3ad0nly", "ro", ""), community("wr1te", "rw", "25")},
		"NOC <noc@example.com>", "Rack 12, Row 3")
	state, p := snmpStateAndPlan(t, r, nil, plan)
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: p}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	assertCommands(t, "create", mock.Commands, []string{
		"snmp-server community r3ad0nly ro",
		"snmp-server community wr1te rw 25",
		"snmp-server contact NOC <noc@example.com>",
		"snmp-server location Rack 12, Row 3",
		"write memory",
	})
}

func TestSNMPUpdate(t *testing.T) {
	tests := []struct {
		name        string
		state, plan *SNMPResourceModel
		want        []string
	}{
		{
			"rotate a string, keep the other",
			snmpModel([]SNMPCommunityModel{community("old", "ro", ""), community("keep", "rw", "25")}, "", ""),
			snmpModel([]SNMPCommunityModel{community("new", "ro", ""), community("keep", "rw", "25")}, "", ""),
			[]string{"no snmp-server community old ro", "snmp-server community new ro", "write memory"},
		},
		{
			"change the ACL of a community",
			snmpModel([]SNMPCommunityModel{community("c", "ro", "")}, "", ""),
			snmpModel([]SNMPCommunityModel{community("c", "ro", "myacl")}, "", ""),
			[]string{"no snmp-server community c ro", "snmp-server community c ro myacl", "write memory"},
		},
		{
			"contact and location",
			snmpModel(nil, "old contact", "here"),
			snmpModel(nil, "new contact", ""),
			[]string{"snmp-server contact new contact", "no snmp-server location", "write memory"},
		},
		{
			"no change",
			snmpModel([]SNMPCommunityModel{community("c", "ro", "")}, "x", "y"),
			snmpModel([]SNMPCommunityModel{community("c", "ro", "")}, "x", "y"),
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := sshclient.NewMockClient("")
			r := &SNMPResource{client: mock}
			state, plan := snmpStateAndPlan(t, r, tt.state, tt.plan)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			assertCommands(t, "update", mock.Commands, tt.want)
		})
	}
}

func TestSNMPDelete(t *testing.T) {
	mock := sshclient.NewMockClient("")
	r := &SNMPResource{client: mock}
	state, _ := snmpStateAndPlan(t, r, snmpModel([]SNMPCommunityModel{community("a", "ro", ""), community("b", "rw", "9")}, "c", "l"), nil)
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	assertCommands(t, "delete", mock.Commands, []string{
		"no snmp-server community a ro", "no snmp-server community b rw",
		"no snmp-server contact", "no snmp-server location", "write memory",
	})
}

// Read: contact/location from the device; communities kept from state while
// the device shows any (masked) community, dropped when it shows none.
func TestSNMPRead(t *testing.T) {
	ctx := context.Background()
	read := func(cfg string, state *SNMPResourceModel) SNMPResourceModel {
		mock := sshclient.NewMockClient(cfg)
		r := &SNMPResource{client: mock}
		s, _ := snmpStateAndPlan(t, r, state, nil)
		resp := resource.ReadResponse{State: s}
		r.Read(ctx, resource.ReadRequest{State: s}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		var got SNMPResourceModel
		resp.State.Get(ctx, &got)
		return got
	}
	comms := []SNMPCommunityModel{community("secret", "ro", "")}
	got := read("snmp-server community ..... ro\nsnmp-server contact Ops\n", snmpModel(comms, "stale", "stale"))
	if got.Contact.ValueString() != "Ops" || !got.Location.IsNull() {
		t.Errorf("contact/location = %v/%v", got.Contact, got.Location)
	}
	var d diag.Diagnostics
	if kept := communitiesOf(ctx, got.Communities, &d); len(kept) != 1 || kept[0].String.ValueString() != "secret" {
		t.Errorf("communities = %v, want kept from state", got.Communities)
	}
	got = read(loadFixture(t, "7250-24"), snmpModel(comms, "", ""))
	if !got.Communities.IsNull() {
		t.Errorf("device without communities: state should be cleared, got %v", got.Communities)
	}
}

// A CLI error never carries a community string into the diagnostics.
func TestSNMP_RedactsCommunityStrings(t *testing.T) {
	mock := sshclient.NewMockClient("")
	mock.ErrorOnCommand["snmp-server community s3cr3t! rw"] = errors.New("Invalid input -> s3cr3t! rw")
	r := &SNMPResource{client: mock}
	state, plan := snmpStateAndPlan(t, r, nil, snmpModel([]SNMPCommunityModel{community("s3cr3t!", "rw", "")}, "", ""))
	resp := resource.CreateResponse{State: state}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Detail()+d.Summary(), "s3cr3t!") {
			t.Errorf("community string leaked: %s", d.Detail())
		}
		if !strings.Contains(d.Detail(), "(redacted)") {
			t.Errorf("no redaction marker: %s", d.Detail())
		}
	}

	// Update errors redact both old and new strings.
	mock = sshclient.NewMockClient("")
	mock.ErrorOnCommand["snmp-server community newpw ro"] = errors.New("Error: oldpw newpw")
	r = &SNMPResource{client: mock}
	state, plan = snmpStateAndPlan(t, r,
		snmpModel([]SNMPCommunityModel{community("oldpw", "ro", "")}, "", ""),
		snmpModel([]SNMPCommunityModel{community("newpw", "ro", "")}, "", ""))
	uresp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &uresp)
	for _, d := range uresp.Diagnostics.Errors() {
		if strings.Contains(d.Detail(), "oldpw") || strings.Contains(d.Detail(), "newpw") {
			t.Errorf("community string leaked: %s", d.Detail())
		}
	}
}

// The community string is marked Sensitive so Terraform never prints it in
// plans or state output.
func TestSNMPSchema_CommunityStringSensitive(t *testing.T) {
	var resp resource.SchemaResponse
	(&SNMPResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	list, ok := resp.Schema.Attributes["communities"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("communities is %T", resp.Schema.Attributes["communities"])
	}
	str, ok := list.NestedObject.Attributes["string"].(schema.StringAttribute)
	if !ok || !str.Sensitive {
		t.Errorf("communities[*].string must be Sensitive, got %+v", list.NestedObject.Attributes["string"])
	}
}
