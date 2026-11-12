package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func flow(srcNS, srcPod, dstIP string, mut ...func(*flowpb.Flow)) *flowpb.Flow {
	fl := &flowpb.Flow{
		Time:    timestamppb.New(time.Now()),
		Verdict: flowpb.Verdict_FORWARDED,
		IP:      &flowpb.IP{Source: "10.244.1.1", Destination: dstIP},
		Source:  &flowpb.Endpoint{Namespace: srcNS, PodName: srcPod},
	}
	for _, m := range mut {
		m(fl)
	}

	return fl
}

func withDst(ep *flowpb.Endpoint) func(*flowpb.Flow) {
	return func(f *flowpb.Flow) { f.Destination = ep }
}

func withTCP(dstPort uint32) func(*flowpb.Flow) {
	return func(f *flowpb.Flow) {
		f.L4 = &flowpb.Layer4{Protocol: &flowpb.Layer4_TCP{TCP: &flowpb.TCP{DestinationPort: dstPort}}}
	}
}

func TestFlowPair(t *testing.T) {
	const podCIDR, nodeCIDR = "10.244.0.0/16", "192.168.1.0/24"

	tests := []struct {
		name string
		fl   *flowpb.Flow
		want Pair
		ok   bool
	}{
		{
			"pod to pod",
			flow("arr-stack", "qbittorrent", "10.244.1.212",
				withDst(&flowpb.Endpoint{Namespace: "niks3", PodName: "niks3-abc"}),
				withTCP(5751)),
			Pair{SrcNS: "arr-stack", SrcPod: "qbittorrent", DstKind: "pod", DstNS: "niks3", DstPod: "niks3-abc", DstIP: "10.244.1.212", Proto: "TCP", DstPort: 5751, Verdict: "FORWARDED"},
			true,
		},
		{
			"pod to world by label",
			flow("gitea", "gitea-1", "1.2.3.4", withDst(&flowpb.Endpoint{Labels: []string{"reserved:world"}}), withTCP(443)),
			Pair{SrcNS: "gitea", SrcPod: "gitea-1", DstKind: "world", DstIP: "1.2.3.4", Proto: "TCP", DstPort: 443, Verdict: "FORWARDED"},
			true,
		},
		{
			"pod to node by label",
			flow("monitoring", "grafana", "192.168.1.54", withDst(&flowpb.Endpoint{Labels: []string{"reserved:host"}}), withTCP(9100)),
			Pair{SrcNS: "monitoring", SrcPod: "grafana", DstKind: "host", DstIP: "192.168.1.54", Proto: "TCP", DstPort: 9100, Verdict: "FORWARDED"},
			true,
		},
		{
			"pod to world by ip fallback",
			flow("vaultwarden", "vw-1", "8.8.8.8", withTCP(443)),
			Pair{SrcNS: "vaultwarden", SrcPod: "vw-1", DstKind: "world", DstIP: "8.8.8.8", Proto: "TCP", DstPort: 443, Verdict: "FORWARDED"},
			true,
		},
		{
			"reply direction port is normalized",
			flow("niks3", "niks3-cnpg-cluster-1", "10.244.0.160",
				withDst(&flowpb.Endpoint{Namespace: "home-assistant", PodName: "home-assistant-app"}),
				withTCP(48434)),
			Pair{SrcNS: "niks3", SrcPod: "niks3-cnpg-cluster-1", DstKind: "pod", DstNS: "home-assistant", DstPod: "home-assistant-app", DstIP: "10.244.0.160", Proto: "TCP", DstPort: 0, Verdict: "FORWARDED"},
			true,
		},
		{
			"host source has no identity",
			flow("", "", "10.244.1.5", withTCP(80)),
			Pair{},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FlowPair(tt.fl, podCIDR, nodeCIDR)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}

			if got != tt.want {
				t.Errorf("pair = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFlowPairIsUniquePerTuple(t *testing.T) {
	a := Pair{SrcNS: "a", SrcPod: "p1", DstKind: "world", DstIP: "1.1.1.1", Proto: "TCP", DstPort: 443, Verdict: "FORWARDED"}
	b := a
	b.DstPort = 8443

	if a.key() == b.key() {
		t.Error("different ports must produce different keys")
	}

	if a.key() != (Pair{SrcNS: "a", SrcPod: "p1", DstKind: "world", DstIP: "1.1.1.1", Proto: "TCP", DstPort: 443, Verdict: "FORWARDED"}).key() {
		t.Error("identical tuples must produce identical keys")
	}
}

func TestDropRecord(t *testing.T) {
	const podCIDR, nodeCIDR = "10.244.0.0/16", "192.168.1.0/24"

	remoteNodeDrop := flow("", "", "10.244.0.160",
		withDst(&flowpb.Endpoint{Namespace: "arr-stack", PodName: "arr-stack-jellyfin-abc"}),
		withTCP(8096),
		func(f *flowpb.Flow) {
			f.IP.Source = "192.168.1.141"
			f.Source = &flowpb.Endpoint{Labels: []string{"reserved:remote-node"}}
			f.Verdict = flowpb.Verdict_DROPPED
			f.DropReasonDesc = flowpb.DropReason_POLICY_DENIED
		})

	tests := []struct {
		name string
		fl   *flowpb.Flow
		want Drop
		ok   bool
	}{
		{
			"remote node source",
			remoteNodeDrop,
			Drop{SrcKind: "remote-node", DstNS: "arr-stack", DstPod: "arr-stack-jellyfin-abc", DstKind: "pod", DstIP: "10.244.0.160", Proto: "TCP", DstPort: 8096, Reason: "POLICY_DENIED"},
			true,
		},
		{
			"pod source",
			flow("jellystat", "jellystat-1", "10.244.0.160",
				withDst(&flowpb.Endpoint{Namespace: "arr-stack", PodName: "arr-stack-jellyfin-abc"}),
				withTCP(8096),
				func(f *flowpb.Flow) {
					f.Verdict = flowpb.Verdict_DROPPED
					f.DropReasonDesc = flowpb.DropReason_POLICY_DENIED
				}),
			Drop{SrcNS: "jellystat", SrcPod: "jellystat-1", DstNS: "arr-stack", DstPod: "arr-stack-jellyfin-abc", DstKind: "pod", DstIP: "10.244.0.160", Proto: "TCP", DstPort: 8096, Reason: "POLICY_DENIED"},
			true,
		},
		{
			"node source without reserved label",
			flow("", "", "10.244.0.160",
				withDst(&flowpb.Endpoint{Namespace: "arr-stack", PodName: "arr-stack-jellyfin-abc"}),
				withTCP(8096),
				func(f *flowpb.Flow) {
					f.IP.Source = "192.168.1.141"
					f.Verdict = flowpb.Verdict_DROPPED
					f.DropReasonDesc = flowpb.DropReason_POLICY_DENIED
				}),
			Drop{SrcKind: "node", DstNS: "arr-stack", DstPod: "arr-stack-jellyfin-abc", DstKind: "pod", DstIP: "10.244.0.160", Proto: "TCP", DstPort: 8096, Reason: "POLICY_DENIED"},
			true,
		},
		{
			"forwarded flow",
			flow("jellystat", "jellystat-1", "10.244.0.160", withTCP(8096)),
			Drop{},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DropRecord(tt.fl, podCIDR, nodeCIDR)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}

			if got != tt.want {
				t.Errorf("drop = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDropKeyIsUniquePerTuple(t *testing.T) {
	a := Drop{SrcKind: "remote-node", DstNS: "arr-stack", DstKind: "pod", DstIP: "10.244.0.160", Proto: "TCP", DstPort: 8096, Reason: "POLICY_DENIED"}
	b := a
	b.Reason = "INVALID_SOURCE"

	if a.key() == b.key() {
		t.Error("different reasons must produce different keys")
	}

	if _, ok := DropRecord(flow("jellystat", "jellystat-1", "10.244.0.160", withTCP(8096)), "10.244.0.0/16", "192.168.1.0/24"); ok {
		t.Error("forwarded flow must not yield a drop record")
	}
}

func TestDNSRecord(t *testing.T) {
	fl := flow("niks3", "niks3-1", "10.244.1.2")
	fl.L7 = &flowpb.Layer7{Record: &flowpb.Layer7_Dns{Dns: &flowpb.DNS{
		Query: "api.github.com.",
		Ips:   []string{"140.82.121.6"},
	}}}

	ns, qname, ips, ok := DNSRecord(fl)
	if !ok || ns != "niks3" || qname != "api.github.com." || len(ips) != 1 || ips[0] != "140.82.121.6" {
		t.Errorf("DNSRecord = %q %q %v %v", ns, qname, ips, ok)
	}

	if _, _, _, ok := DNSRecord(flow("niks3", "niks3-1", "10.244.1.2")); ok {
		t.Error("non-DNS flow must not yield a record")
	}
}

func TestSeenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.json")

	reg, err := LoadSeen(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	now := time.Now()
	if !reg.Add("k1", now) {
		t.Fatal("first insert must be novel")
	}

	if reg.Add("k1", now) {
		t.Fatal("second insert must not be novel")
	}

	if err := reg.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reg2, err := LoadSeen(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if reg2.Add("k1", now) {
		t.Error("reloaded seen state must remember persisted keys")
	}

	if !reg2.Add("k2", now) {
		t.Error("new key after reload must be novel")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if len(data) == 0 {
		t.Error("seen state file must not be empty after save")
	}
}
