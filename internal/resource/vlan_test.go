package resource

import (
	"reflect"
	"testing"

	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

func TestFindTrunkAllPorts(t *testing.T) {
	tests := []struct {
		name      string
		vlans     []parser.VLAN
		newVLANID int
		want      []string
	}{
		{
			name: "trunk ports tagged on all vlans are detected; access ports are not",
			vlans: []parser.VLAN{
				{ID: 1, TaggedPorts: []string{"1/1/5"}}, // default VLAN ignored
				{ID: 10, TaggedPorts: []string{"1/2/1"}, UntaggedPorts: []string{"1/2/2", "1/1/5"}},
				{ID: 20, TaggedPorts: []string{"1/2/1", "1/2/2"}},
				{ID: 30, TaggedPorts: []string{"1/2/1", "1/2/2"}},
				{ID: 40, TaggedPorts: []string{"1/2/1", "1/2/2"}},
			},
			newVLANID: 50,
			// 1/2/1: tagged on 10,20,30,40 -> trunk.
			// 1/2/2: untagged on 10, tagged on 20,30,40 -> trunk (untagged exception).
			// 1/1/5: untagged on 10 only, tagged on nothing else -> not a trunk.
			want: []string{"1/2/1", "1/2/2"},
		},
		{
			name: "the newly-created vlan is excluded from the membership test",
			vlans: []parser.VLAN{
				{ID: 10, TaggedPorts: []string{"1/2/1"}},
				{ID: 20, TaggedPorts: []string{"1/2/1"}},
				{ID: 30, TaggedPorts: []string{"1/2/1"}},
				// 1/2/1 is NOT yet a member of 50 (it's being created) — must not disqualify.
				{ID: 50},
			},
			newVLANID: 50,
			want:      []string{"1/2/1"},
		},
		{
			name: "below the minimum VLAN count, nothing qualifies",
			vlans: []parser.VLAN{
				{ID: 10, TaggedPorts: []string{"1/2/1"}},
				{ID: 20, TaggedPorts: []string{"1/2/1"}},
			},
			newVLANID: 30,
			want:      nil,
		},
		{
			name: "a port missing from one vlan does not qualify",
			vlans: []parser.VLAN{
				{ID: 10, TaggedPorts: []string{"1/2/1"}},
				{ID: 20, TaggedPorts: []string{"1/2/1"}},
				{ID: 30, TaggedPorts: []string{"1/2/1"}},
				{ID: 40, TaggedPorts: []string{}}, // 1/2/1 absent here
			},
			newVLANID: 50,
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &parser.RunningConfig{VLANs: tt.vlans}
			got := findTrunkAllPorts(config, tt.newVLANID)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("findTrunkAllPorts() = %v, want %v", got, tt.want)
			}
		})
	}
}
