package main

import (
	"testing"

	"tailscale.com/types/key"
)

func TestRemoveMember(t *testing.T) {
	mk := func(names ...string) []member {
		var ms []member
		for _, n := range names {
			ms = append(ms, member{Name: n, Key: key.NewNode().Public(), DialKey: key.NewNode().Public()})
		}

		return ms
	}

	tests := []struct {
		name    string
		members []member
		target  string
		want    []string
		removed bool
	}{
		{"middle", mk("laptop", "nas", "emu"), "nas", []string{"laptop", "emu"}, true},
		{"only", mk("nas"), "nas", nil, true},
		{"missing", mk("laptop"), "nas", []string{"laptop"}, false},
		{"empty", nil, "nas", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, removed := removeMember(tt.members, tt.target)
			if removed != tt.removed {
				t.Fatalf("removed = %v, want %v", removed, tt.removed)
			}

			var names []string
			for _, m := range got {
				names = append(names, m.Name)
			}

			if len(names) != len(tt.want) {
				t.Fatalf("members = %v, want %v", names, tt.want)
			}

			for i := range names {
				if names[i] != tt.want[i] {
					t.Fatalf("members = %v, want %v", names, tt.want)
				}
			}
		})
	}
}
