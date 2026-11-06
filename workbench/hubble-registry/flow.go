package main

import (
	"fmt"
	"slices"

	flowpb "github.com/cilium/cilium/api/v1/flow"
)

// Pair is one observed source->destination tuple at the granularity netpols
// are authored: namespace-level source, destination classified as pod, node,
// or world, plus protocol, port and verdict.
type Pair struct {
	SrcNS   string
	SrcPod  string
	DstKind string
	DstNS   string
	DstPod  string
	DstIP   string
	Proto   string
	DstPort uint32
	Verdict string
}

func (p Pair) key() string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s",
		p.SrcNS, p.SrcPod, p.DstKind, p.DstNS, p.DstPod, p.DstIP, p.Proto, p.DstPort, p.Verdict)
}

func (p Pair) LogAttrs() []any {
	return []any{
		"src_ns", p.SrcNS,
		"src_pod", p.SrcPod,
		"dst_kind", p.DstKind,
		"dst_ns", p.DstNS,
		"dst_pod", p.DstPod,
		"dst_ip", p.DstIP,
		"proto", p.Proto,
		"dst_port", p.DstPort,
		"verdict", p.Verdict,
	}
}

func mask(bits int) uint32 {
	if bits == 0 {
		return 0
	}

	return ^uint32(0) << (32 - bits) //nolint:mnd
}

func inCIDR(ip, podCIDR, nodeCIDR string) string {
	if inCIDRRange(ip, podCIDR) {
		return "pod"
	}

	if inCIDRRange(ip, nodeCIDR) {
		return "node"
	}

	return "world"
}

func inCIDRRange(ip, cidr string) bool {
	base, bits, ok := splitCIDR(cidr)
	if !ok {
		return false
	}

	addr, ok := parseIPv4(ip)

	return ok && addr&mask(bits) == base&mask(bits)
}

func splitCIDR(cidr string) (uint32, int, bool) {
	for i := range len(cidr) {
		if cidr[i] != '/' {
			continue
		}

		addr, ok := parseIPv4(cidr[:i])
		if !ok {
			return 0, 0, false
		}

		var bits int
		if _, err := fmt.Sscanf(cidr[i+1:], "%d", &bits); err != nil || bits < 0 || bits > 32 {
			return 0, 0, false
		}

		return addr, bits, true
	}

	return 0, 0, false
}

func parseIPv4(s string) (uint32, bool) {
	var a, b, c, d int

	n, err := fmt.Sscanf(s, "%d.%d.%d.%d", &a, &b, &c, &d)
	if err != nil || n != 4 || a > 255 || b > 255 || c > 255 || d > 255 {
		return 0, false
	}

	return uint32(a)<<24 | uint32(b)<<16 | uint32(c)<<8 | uint32(d), true //nolint:mnd
}

func l4(fl *flowpb.Flow) (proto string, port uint32) {
	switch l4 := fl.GetL4().GetProtocol().(type) {
	case *flowpb.Layer4_TCP:
		return "TCP", l4.TCP.GetDestinationPort()
	case *flowpb.Layer4_UDP:
		return "UDP", l4.UDP.GetDestinationPort()
	case *flowpb.Layer4_SCTP:
		return "SCTP", l4.SCTP.GetDestinationPort()
	default:
		return "ICMP", 0
	}
}

// ephemeralPort is where reply-direction flows carry the client's port; the
// cluster's service ports all live below it. Normalizing to 0 keeps one pair
// per server->client class instead of one per connection.
const ephemeralPort = 32768

func servicePort(port uint32) uint32 {
	if port >= ephemeralPort {
		return 0
	}

	return port
}

func hasReservedLabel(ep *flowpb.Endpoint, label string) bool {
	return slices.Contains(ep.GetLabels(), label)
}

// FlowPair classifies a flow into a Pair. ok is false when the flow has no
// source pod identity (e.g. host or world chatter).
func FlowPair(fl *flowpb.Flow, podCIDR, nodeCIDR string) (Pair, bool) {
	src := fl.GetSource()
	if src.GetNamespace() == "" || src.GetPodName() == "" {
		return Pair{}, false
	}

	dstKind := "pod"
	dst := fl.GetDestination()
	dstNs := dst.GetNamespace()

	dstPod := dst.GetPodName()
	switch {
	case dstNs != "":
	case hasReservedLabel(dst, "reserved:world"):
		dstKind = "world"
	case hasReservedLabel(dst, "reserved:host"):
		dstKind = "host"
	case hasReservedLabel(dst, "reserved:remote-node"):
		dstKind = "remote-node"
	default:
		dstKind = inCIDR(fl.GetIP().GetDestination(), podCIDR, nodeCIDR)
	}

	proto, port := l4(fl)

	return Pair{
		SrcNS:   src.GetNamespace(),
		SrcPod:  src.GetPodName(),
		DstKind: dstKind,
		DstNS:   dstNs,
		DstPod:  dstPod,
		DstIP:   fl.GetIP().GetDestination(),
		Proto:   proto,
		DstPort: servicePort(port),
		Verdict: fl.GetVerdict().String(),
	}, true
}

// DNSRecord returns the source namespace, query name and answer IPs of a
// DNS L7 flow, or ok=false when the flow carries no DNS payload.
func DNSRecord(fl *flowpb.Flow) (srcNS, qname string, ips []string, ok bool) {
	dns := fl.GetL7().GetDns()
	if dns == nil || dns.GetQuery() == "" {
		return "", "", nil, false
	}

	return fl.GetSource().GetNamespace(), dns.GetQuery(), dns.GetIps(), true
}
