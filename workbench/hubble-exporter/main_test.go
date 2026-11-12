package main

import (
	"path/filepath"
	"testing"

	flowpb "github.com/cilium/cilium/api/v1/flow"
)

func TestRecordEvent(t *testing.T) {
	const podCIDR, nodeCIDR = "10.244.0.0/16", "192.168.1.0/24"

	dropped := func() *flowpb.Flow {
		return flow("jellystat", "jellystat-1", "10.244.0.160",
			withDst(&flowpb.Endpoint{Namespace: "arr-stack", PodName: "arr-stack-jellyfin-abc"}),
			withTCP(8096),
			func(f *flowpb.Flow) {
				f.Verdict = flowpb.Verdict_DROPPED
				f.DropReasonDesc = flowpb.DropReason_POLICY_DENIED
			})
	}

	forwarded := func() *flowpb.Flow {
		return flow("niks3", "niks3-1", "10.244.0.160",
			withDst(&flowpb.Endpoint{Namespace: "arr-stack", PodName: "arr-stack-jellyfin-abc"}),
			withTCP(5751))
	}

	tests := []struct {
		name  string
		flows []*flowpb.Flow
		want  []string
	}{
		{
			"seen drop emits neither drop nor pair",
			[]*flowpb.Flow{dropped(), dropped()},
			[]string{"drop", ""},
		},
		{
			"novel forwarded flow emits a pair",
			[]*flowpb.Flow{forwarded()},
			[]string{"pair"},
		},
		{
			"seen pair emits nothing",
			[]*flowpb.Flow{forwarded(), forwarded()},
			[]string{"pair", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen, err := LoadSeen(filepath.Join(t.TempDir(), "seen.json"))
			if err != nil {
				t.Fatal(err)
			}

			for i, fl := range tt.flows {
				event, _ := recordEvent(fl, seen, podCIDR, nodeCIDR)
				if event != tt.want[i] {
					t.Errorf("flow %d: event = %q, want %q", i, event, tt.want[i])
				}
			}
		})
	}
}
