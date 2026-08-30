package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidatePVState(t *testing.T) {
	tests := []struct {
		name               string
		phase              corev1.PersistentVolumePhase
		reclaim            corev1.PersistentVolumeReclaimPolicy
		pvcExists          bool
		allowDeleteReclaim bool
		wantErr            error
	}{
		{
			name:    "Released + Retain, no live PVC",
			phase:   corev1.VolumeReleased,
			reclaim: corev1.PersistentVolumeReclaimRetain,
			wantErr: nil,
		},
		{
			name:    "Available + Retain",
			phase:   corev1.VolumeAvailable,
			reclaim: corev1.PersistentVolumeReclaimRetain,
			wantErr: nil,
		},
		{
			name:    "Pending + Retain",
			phase:   corev1.VolumePending,
			reclaim: corev1.PersistentVolumeReclaimRetain,
			wantErr: nil,
		},
		{
			name:      "Bound with live PVC",
			phase:     corev1.VolumeBound,
			reclaim:   corev1.PersistentVolumeReclaimRetain,
			pvcExists: true,
			wantErr:   errPVClaimed,
		},
		{
			name:      "Bound but PVC already deleted (stale claimRef)",
			phase:     corev1.VolumeBound,
			reclaim:   corev1.PersistentVolumeReclaimRetain,
			pvcExists: false,
			wantErr:   nil,
		},
		{
			name:    "Failed phase",
			phase:   corev1.VolumeFailed,
			reclaim: corev1.PersistentVolumeReclaimRetain,
			wantErr: errPVFailed,
		},
		{
			name:    "Delete reclaim, not allowed",
			phase:   corev1.VolumeReleased,
			reclaim: corev1.PersistentVolumeReclaimDelete,
			wantErr: errPVDeleteReclaim,
		},
		{
			name:               "Delete reclaim, allowed",
			phase:              corev1.VolumeReleased,
			reclaim:            corev1.PersistentVolumeReclaimDelete,
			allowDeleteReclaim: true,
			wantErr:            nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePVState(tt.phase, tt.reclaim, tt.pvcExists, tt.allowDeleteReclaim)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validatePVState() err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateDestName(t *testing.T) {
	tests := []struct {
		name          string
		claimRef      *corev1.ObjectReference
		destPVC       string
		allowMismatch bool
		wantErr       error
	}{
		{
			name:     "names match",
			claimRef: &corev1.ObjectReference{Name: "registry"},
			destPVC:  "registry",
			wantErr:  nil,
		},
		{
			name:     "names mismatch",
			claimRef: &corev1.ObjectReference{Name: "registry"},
			destPVC:  "other-pvc",
			wantErr:  errDestNameMismatch,
		},
		{
			name:     "mismatch allowed",
			claimRef: &corev1.ObjectReference{Name: "registry"},
			destPVC:  "other-pvc",
			allowMismatch: true,
			wantErr:  nil,
		},
		{
			name:     "nil claimRef, no check",
			claimRef: nil,
			destPVC:  "anything",
			wantErr:  nil,
		},
		{
			name:     "empty claimRef name, no check",
			claimRef: &corev1.ObjectReference{Name: ""},
			destPVC:  "anything",
			wantErr:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDestName(tt.claimRef, tt.destPVC, tt.allowMismatch)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validateDestName() err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestTempPVCName(t *testing.T) {
	tests := []struct {
		name string
		pv   string
		want string
	}{
		{name: "short name", pv: "pvc-abc123", want: "pvmig-src-pvc-abc123"},
		{name: "empty", pv: "", want: "pvmig-src-"},
		{
			name: "truncates to 63 chars",
			pv:   strings.Repeat("a", 100),
			want: "pvmig-src-" + strings.Repeat("a", 53),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tempPVCName(tt.pv)
			if got != tt.want {
				t.Fatalf("tempPVCName(%q) = %q, want %q", tt.pv, got, tt.want)
			}

			if len(got) > 63 {
				t.Fatalf("tempPVCName(%q) = %q exceeds 63 chars (len %d)", tt.pv, got, len(got))
			}
		})
	}
}

func TestBuildTempPVC(t *testing.T) {
	sc := "local-path"
	cap := resource.MustParse("10Gi")

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-test"},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:         corev1.ResourceList{corev1.ResourceStorage: cap},
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: sc,
			VolumeMode:       ptrVolumeMode(corev1.PersistentVolumeFilesystem),
		},
	}

	claim := buildTempPVC(pv, "pvmig-src-pvc-test", "default")

	if claim.Name != "pvmig-src-pvc-test" {
		t.Fatalf("name = %q, want pvmig-src-pvc-test", claim.Name)
	}

	if claim.Namespace != "default" {
		t.Fatalf("namespace = %q, want default", claim.Namespace)
	}

	if claim.Spec.VolumeName != "pvc-test" {
		t.Fatalf("volumeName = %q, want pvc-test", claim.Spec.VolumeName)
	}

	if got := claim.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(cap) != 0 {
		t.Fatalf("storage request = %s, want %s", got.String(), cap.String())
	}

	if !contains(claim.Spec.AccessModes, corev1.ReadWriteOnce) {
		t.Fatalf("accessModes = %v, want [ReadWriteOnce]", claim.Spec.AccessModes)
	}

	if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != sc {
		t.Fatalf("storageClassName = %v, want %s", claim.Spec.StorageClassName, sc)
	}

	if claim.Spec.VolumeMode == nil || *claim.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
		t.Fatalf("volumeMode = %v, want Filesystem", claim.Spec.VolumeMode)
	}

	if got := claim.Labels["pv-migrate.io/source-pv"]; got != "pvc-test" {
		t.Fatalf("source-pv label = %q, want pvc-test", got)
	}

	// Mutating the claim's slices must not affect the PV's.
	claim.Spec.AccessModes[0] = corev1.ReadWriteMany
	if pv.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("buildTempPVC shared the AccessModes backing array")
	}
}

func TestTempNamespace(t *testing.T) {
	fallback := "default"

	tests := []struct {
		name   string
		pv     *corev1.PersistentVolume
		tempNS string
		want   string
	}{
		{
			name: "explicit tempNS wins",
			pv: &corev1.PersistentVolume{Spec: corev1.PersistentVolumeSpec{
				ClaimRef: &corev1.ObjectReference{Namespace: "old-ns"},
			}},
			tempNS: "custom",
			want:   "custom",
		},
		{
			name: "falls back to claimRef namespace",
			pv: &corev1.PersistentVolume{Spec: corev1.PersistentVolumeSpec{
				ClaimRef: &corev1.ObjectReference{Namespace: "old-ns"},
			}},
			want: "old-ns",
		},
		{
			name: "no claimRef, uses fallback",
			pv:   &corev1.PersistentVolume{},
			want: fallback,
		},
		{
			name: "claimRef with empty namespace, uses fallback",
			pv: &corev1.PersistentVolume{Spec: corev1.PersistentVolumeSpec{
				ClaimRef: &corev1.ObjectReference{},
			}},
			want: fallback,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tempNamespace(tt.pv, tt.tempNS, fallback)
			if got != tt.want {
				t.Fatalf("tempNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveStrategies(t *testing.T) {
	tests := []struct {
		name       string
		strategies []string
		sourceNode string
		destNode   string
		want       []string
	}{
		{
			name:       "empty list, same nodes, stays empty",
			strategies: nil,
			sourceNode: "node1",
			destNode:   "node1",
			want:       nil,
		},
		{
			name:       "empty list, different nodes, mount stripped from default",
			strategies: nil,
			sourceNode: "homelab2",
			destNode:   "homelab3",
			want:       []string{"clusterip", "loadbalancer"},
		},
		{
			name:       "default list, same nodes, unchanged",
			strategies: []string{"mount", "clusterip", "loadbalancer"},
			sourceNode: "node1",
			destNode:   "node1",
			want:       []string{"mount", "clusterip", "loadbalancer"},
		},
		{
			name:       "default list, different nodes, mount stripped",
			strategies: []string{"mount", "clusterip", "loadbalancer"},
			sourceNode: "homelab2",
			destNode:   "homelab3",
			want:       []string{"clusterip", "loadbalancer"},
		},
		{
			name:       "only mount, different nodes, fallback added",
			strategies: []string{"mount"},
			sourceNode: "node1",
			destNode:   "node2",
			want:       []string{"clusterip", "loadbalancer"},
		},
		{
			name:       "no mount in list, different nodes, unchanged",
			strategies: []string{"clusterip", "loadbalancer"},
			sourceNode: "node1",
			destNode:   "node2",
			want:       []string{"clusterip", "loadbalancer"},
		},
		{
			name:       "one node unset, unchanged",
			strategies: []string{"mount", "clusterip"},
			sourceNode: "node1",
			destNode:   "",
			want:       []string{"mount", "clusterip"},
		},
		{
			name:       "both nodes unset, unchanged",
			strategies: []string{"mount", "clusterip"},
			want:       []string{"mount", "clusterip"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveStrategies(tt.strategies, tt.sourceNode, tt.destNode)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("resolveStrategies() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPvmigrateArgs(t *testing.T) {
	tests := []struct {
		name       string
		srcPVC     string
		srcNS      string
		destPVC    string
		destNS     string
		kubeconfig string
		opts       migrationOpts
		wantSubs   []string
		wantAbsent []string
	}{
		{
			name:    "minimal",
			srcPVC:  "src", srcNS: "s-ns", destPVC: "dst", destNS: "d-ns",
			opts:     migrationOpts{},
			wantSubs: []string{"--source=src", "--source-namespace=s-ns", "--dest=dst", "--dest-namespace=d-ns"},
		},
		{
			name:       "with kubeconfig",
			srcPVC:     "src", srcNS: "s-ns", destPVC: "dst", destNS: "d-ns",
			kubeconfig: "/home/u/.kube/config",
			opts:       migrationOpts{},
			wantSubs:   []string{"--source-kubeconfig=/home/u/.kube/config", "--dest-kubeconfig=/home/u/.kube/config"},
		},
		{
			name:    "all bool flags on",
			srcPVC:  "src", srcNS: "s-ns", destPVC: "dst", destNS: "d-ns",
			opts: migrationOpts{
				deleteExtraneous:     true,
				ignoreMounted:        true,
				nonRoot:              true,
				noChown:              true,
				sourceMountReadWrite: true,
				noCompress:           true,
			},
			wantSubs: []string{
				"--dest-delete-extraneous-files", "--ignore-mounted", "--non-root",
				"--no-chown", "--source-mount-read-write", "--no-compress",
			},
		},
		{
			name:    "strategies and durations",
			srcPVC:  "src", srcNS: "s-ns", destPVC: "dst", destNS: "d-ns",
			opts: migrationOpts{
				strategies:  []string{"mount", "clusterip"},
				helmTimeout: "2m",
				logLevel:    "DEBUG",
			},
			wantSubs: []string{"--strategies=mount,clusterip", "--helm-timeout=2m", "--log-level=DEBUG"},
		},
		{
			name:    "source and dest node pins",
			srcPVC:  "src", srcNS: "s-ns", destPVC: "dst", destNS: "d-ns",
			opts:     migrationOpts{sourceNode: "homelab2", destNode: "homelab3"},
			wantSubs: []string{
				"--helm-set=sshd.nodeSelector.kubernetes\\.io/hostname=homelab2",
				"--helm-set=rsync.nodeSelector.kubernetes\\.io/hostname=homelab3",
			},
		},
		{
			name:    "source node only",
			srcPVC:  "src", srcNS: "s-ns", destPVC: "dst", destNS: "d-ns",
			opts:       migrationOpts{sourceNode: "homelab2"},
			wantSubs:   []string{"--helm-set=sshd.nodeSelector.kubernetes\\.io/hostname=homelab2"},
			wantAbsent: []string{"--helm-set=rsync.nodeSelector"},
		},
		{
			name:    "bool flags off must not appear",
			srcPVC:  "src", srcNS: "s-ns", destPVC: "dst", destNS: "d-ns",
			opts:       migrationOpts{},
			wantAbsent: []string{
				"--non-root", "--no-chown", "--no-compress",
				"--helm-set=rsync.nodeSelector", "--helm-set=sshd.nodeSelector",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := pvmigrateArgs(tt.srcPVC, tt.srcNS, tt.destPVC, tt.destNS, tt.kubeconfig, tt.opts)
			joined := strings.Join(args, "\x00")

			for _, s := range tt.wantSubs {
				if !strings.Contains(joined, s) {
					t.Errorf("missing arg %q in %v", s, args)
				}
			}

			for _, s := range tt.wantAbsent {
				if strings.Contains(joined, s) {
					t.Errorf("unexpected arg %q in %v", s, args)
				}
			}
		})
	}
}

// helpers

func contains(modes []corev1.PersistentVolumeAccessMode, want corev1.PersistentVolumeAccessMode) bool {
	for _, m := range modes {
		if m == want {
			return true
		}
	}

	return false
}

func ptrVolumeMode(m corev1.PersistentVolumeMode) *corev1.PersistentVolumeMode { return &m }
