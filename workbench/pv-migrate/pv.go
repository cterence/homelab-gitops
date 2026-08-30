package main

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Guards and pure logic for PV state and temp-PVC construction. These have no
// client-go calls and are exercised by pv_test.go.

// errPVClaimed is returned when the source PV is Bound to a live PVC.
var errPVClaimed = errors.New("PV is Bound to a live PVC; delete the PVC first so the PV becomes Released")

// errPVFailed is returned when the source PV is in the Failed phase.
var errPVFailed = errors.New("PV is in the Failed phase")

// errPVDeleteReclaim is returned when the PV uses a Delete reclaim policy and
// the caller has not opted in with --allow-delete-reclaim. Deleting the temp
// PVC would delete the PV and its data, hence the guard.
var errPVDeleteReclaim = errors.New("PV has a Delete reclaim policy; pass --allow-delete-reclaim to proceed " +
	"(deleting the temporary PVC would delete this PV and its data)")

// validatePVState returns nil if the PV is safe to use as a migration source.
// pvcExists reports whether the PV's claimRef still points to a live PVC.
func validatePVState(phase corev1.PersistentVolumePhase,
	reclaimPolicy corev1.PersistentVolumeReclaimPolicy, pvcExists, allowDeleteReclaim bool) error {
	switch phase {
	case corev1.VolumeBound:
		if pvcExists {
			return errPVClaimed
		}
	case corev1.VolumeFailed:
		return errPVFailed
	case corev1.VolumeReleased, corev1.VolumeAvailable, corev1.VolumePending:
		// safe to proceed
	default:
		return fmt.Errorf("PV is in unknown phase %q", phase)
	}

	if reclaimPolicy == corev1.PersistentVolumeReclaimDelete && !allowDeleteReclaim {
		return errPVDeleteReclaim
	}

	return nil
}

// errDestNameMismatch is returned when the dest PVC name does not match the
// source PV's previous claimRef name, suggesting a mismatched migration target.
var errDestNameMismatch = errors.New("dest PVC name does not match the source PV's previous consumer " +
	"(claimRef name); pass --allow-dest-mismatch to proceed anyway")

// validateDestName checks that the dest PVC name matches the source PV's
// previous claimRef name. This catches accidental mismatches when copying
// flag values between runs. When the PV has no claimRef (never bound) or the
// names match, it returns nil. allowMismatch bypasses the check.
func validateDestName(claimRef *corev1.ObjectReference, destPVC string, allowMismatch bool) error {
	if allowMismatch {
		return nil
	}

	if claimRef == nil || claimRef.Name == "" {
		return nil // PV never bound, nothing to compare
	}

	if claimRef.Name != destPVC {
		return fmt.Errorf("%w: dest=%q, claimRef=%q", errDestNameMismatch, destPVC, claimRef.Name)
	}

	return nil
}

// tempPVCName builds a DNS-1123-compatible PVC name from the source PV name.
// The "pvmig-src-" prefix marks it as managed by this tool; the PV name is
// truncated so the total length stays within Kubernetes' 63-char limit.
func tempPVCName(pvName string) string {
	const (
		prefix  = "pvmig-src-"
		maxName = 63
	)
	if len(pvName) <= maxName-len(prefix) {
		return prefix + pvName
	}

	return prefix + pvName[:maxName-len(prefix)]
}

// buildTempPVC constructs the temporary PVC that statically binds to the given
// source PV. It mirrors the PV's capacity, access modes, storage class and
// volume mode so binding matches without relying on dynamic provisioning.
func buildTempPVC(pv *corev1.PersistentVolume, name, namespace string) *corev1.PersistentVolumeClaim {
	requests := corev1.ResourceList{}
	if cap, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
		requests[corev1.ResourceStorage] = cap
	}

	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "pv-migrate",
				"pv-migrate.io/source-pv":      pv.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName:  pv.Name,
			AccessModes: append([]corev1.PersistentVolumeAccessMode(nil), pv.Spec.AccessModes...),
			Resources:   corev1.VolumeResourceRequirements{Requests: requests},
		},
	}

	// PV spec StorageClassName is a value; PVC spec wants a pointer. Copy
	// only when the PV has one — an empty string would set the pointer to a
	// non-nil empty, which selects the default StorageClass instead of none.
	if pv.Spec.StorageClassName != "" {
		sc := pv.Spec.StorageClassName
		claim.Spec.StorageClassName = &sc
	}

	if pv.Spec.VolumeMode != nil {
		mode := *pv.Spec.VolumeMode
		claim.Spec.VolumeMode = &mode
	}

	return claim
}

// tempNamespace derives the namespace for the temp PVC. When tempNS is set it
// wins; otherwise the PV's claimRef namespace is used; otherwise fallback.
func tempNamespace(pv *corev1.PersistentVolume, tempNS, fallback string) string {
	if tempNS != "" {
		return tempNS
	}

	if pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.Namespace != "" {
		return pv.Spec.ClaimRef.Namespace
	}

	return fallback
}

// pvmigrateArgs builds the argument list for the `pv-migrate` CLI invocation.
// kubeconfig may be empty to let pv-migrate use its default discovery.
func pvmigrateArgs(srcPVC, srcNS, destPVC, destNS, kubeconfig string, opts migrationOpts) []string {
	args := []string{
		"--source=" + srcPVC,
		"--source-namespace=" + srcNS,
		"--dest=" + destPVC,
		"--dest-namespace=" + destNS,
	}

	if kubeconfig != "" {
		args = append(args, "--source-kubeconfig="+kubeconfig, "--dest-kubeconfig="+kubeconfig)
	}

	if opts.deleteExtraneous {
		args = append(args, "--dest-delete-extraneous-files")
	}

	if opts.ignoreMounted {
		args = append(args, "--ignore-mounted")
	}

	if opts.nonRoot {
		args = append(args, "--non-root")
	}

	if opts.noChown {
		args = append(args, "--no-chown")
	}

	if opts.sourceMountReadWrite {
		args = append(args, "--source-mount-read-write")
	}

	if opts.noCompress {
		args = append(args, "--no-compress")
	}

	if len(opts.strategies) > 0 {
		args = append(args, "--strategies="+strings.Join(opts.strategies, ","))
	}

	if opts.helmTimeout != "" {
		args = append(args, "--helm-timeout="+opts.helmTimeout)
	}

	if opts.logLevel != "" {
		args = append(args, "--log-level="+opts.logLevel)
	}

	if opts.sourceNode != "" {
		// Use nodeSelector, not nodeName: nodeName bypasses the scheduler,
		// which prevents WaitForFirstConsumer PVC binding (the scheduler's
		// VolumeBinding plugin does that). nodeSelector goes through the
		// scheduler so PVC binding works.
		args = append(args,
			"--helm-set=sshd.nodeSelector.kubernetes\\.io/hostname="+opts.sourceNode)
	}

	if opts.destNode != "" {
		args = append(args,
			"--helm-set=rsync.nodeSelector.kubernetes\\.io/hostname="+opts.destNode)
	}

	return args
}

// resolveStrategies filters the strategies list based on the source and dest
// nodes. The mount strategy uses a single pod that mounts both PVCs, so it
// cannot work when the PVs are on different nodes — it gets stuck in
// ContainerCreating. When sourceNode and destNode are both set and differ,
// mount is stripped from the list. If the user passed no strategies (empty),
// pv-migrate's default (mount,clusterip,loadbalancer) is used as the starting
// point so mount can be removed.
func resolveStrategies(strategies []string, sourceNode, destNode string) []string {
	if sourceNode == "" || destNode == "" || sourceNode == destNode {
		return strategies
	}

	// User passed nothing: start from pv-migrate's default so we can strip
	// mount from it. Returning nil here would leave mount in play.
	if len(strategies) == 0 {
		strategies = []string{"mount", "clusterip", "loadbalancer"}
	}

	filtered := slices.DeleteFunc(slices.Clone(strategies), func(s string) bool {
		return s == "mount"
	})

	if len(filtered) == len(strategies) {
		return strategies // mount wasn't in the list
	}

	if len(filtered) == 0 {
		slog.Warn("all strategies were mount; adding clusterip as fallback",
			"source_node", sourceNode, "dest_node", destNode)

		return []string{"clusterip", "loadbalancer"}
	}

	slog.Info("stripped mount strategy (source and dest on different nodes)",
		"strategies", filtered)

	return filtered
}

// migrationOpts holds the passthrough flags for the pv-migrate CLI.
type migrationOpts struct {
	deleteExtraneous     bool
	ignoreMounted        bool
	nonRoot              bool
	noChown              bool
	sourceMountReadWrite bool
	noCompress           bool
	strategies           []string
	helmTimeout          string // duration string, e.g. "1m"
	logLevel             string
	sourceNode           string // pin source-side pod (sshd in pull mode) to this node
	destNode             string // pin dest-side pod (rsync in pull mode) to this node
}
