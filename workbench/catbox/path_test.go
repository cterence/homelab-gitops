package main

import (
	"testing"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func TestPathOf(t *testing.T) {
	peer := key.NewNode().Public()

	tests := []struct {
		name string
		st   *ipnstate.Status
		want string
	}{
		{
			name: "nil status",
			st:   nil,
			want: "",
		},
		{
			name: "peer absent",
			st:   &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{}},
			want: "",
		},
		{
			name: "direct path",
			st: &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{
				peer: {CurAddr: "192.168.1.5:41941"},
			}},
			want: "direct 192.168.1.5:41941",
		},
		{
			name: "relayed path",
			st: &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{
				peer: {Relay: "par"},
			}},
			want: "DERP relay par",
		},
		{
			name: "no path yet",
			st: &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{
				peer: {},
			}},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathOf(tt.st, peer); got != tt.want {
				t.Fatalf("pathOf() = %q, want %q", got, tt.want)
			}
		})
	}
}
