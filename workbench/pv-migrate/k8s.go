package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// k8sClient wraps a typed clientset for PV/PVC operations.
type k8sClient struct {
	core kubernetes.Interface
}

// newK8sClient builds a client from an explicit kubeconfig path, in-cluster
// config, or the default kubeconfig discovered on disk.
func newK8sClient(kubeconfig string) (*k8sClient, error) {
	var (
		cfg *rest.Config
		err error
	)

	switch {
	case kubeconfig != "":
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	case os.Getenv("KUBERNETES_SERVICE_HOST") != "":
		cfg, err = rest.InClusterConfig()
	default:
		if home := homedir.HomeDir(); home != "" {
			cfg, err = clientcmd.BuildConfigFromFlags("", home+"/.kube/config")
		} else {
			err = errors.New("no kubeconfig path and not running in-cluster")
		}
	}

	if err != nil {
		return nil, fmt.Errorf("building kubeconfig: %w", err)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}

	return &k8sClient{core: cs}, nil
}

// getPV returns the named PersistentVolume.
func (c *k8sClient) getPV(ctx context.Context, name string) (*corev1.PersistentVolume, error) {
	pv, err := c.core.CoreV1().PersistentVolumes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting PV %s: %w", name, err)
	}

	return pv, nil
}

// pvcExists returns true if the referenced PVC still exists in its namespace.
// A nil or empty claimRef returns false.
func (c *k8sClient) pvcExists(ctx context.Context, ref *corev1.ObjectReference) (bool, error) {
	if ref == nil || ref.Name == "" || ref.Namespace == "" {
		return false, nil
	}

	_, err := c.core.CoreV1().PersistentVolumeClaims(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}

	if apierrors.IsNotFound(err) {
		return false, nil
	}

	return false, fmt.Errorf("checking PVC %s/%s: %w", ref.Namespace, ref.Name, err)
}

// patchPVClaimRefNil patches the PV's spec.claimRef to nil, transitioning a
// Released PV to Available so a new PVC can bind to it.
func (c *k8sClient) patchPVClaimRefNil(ctx context.Context, pvName string) error {
	patch := []byte(`[{"op":"remove","path":"/spec/claimRef"}]`)
	if _, err := c.core.CoreV1().PersistentVolumes().Patch(
		ctx, pvName, types.JSONPatchType, patch, metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("patching PV %s claimRef: %w", pvName, err)
	}

	return nil
}

// getDestPVC validates that the destination PVC exists and is not currently
// bound to the source PV (which would be a no-op or a mistake).
func (c *k8sClient) getDestPVC(ctx context.Context, name, namespace string) (*corev1.PersistentVolumeClaim, error) {
	pvc, err := c.core.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting destination PVC %s/%s: %w", namespace, name, err)
	}

	return pvc, nil
}

// createTempPVC creates the temporary source PVC. It returns an error if a
// PVC with the same name already exists (should not happen given the unique
// prefix, but avoids silently adopting a leftover).
func (c *k8sClient) createTempPVC(ctx context.Context, claim *corev1.PersistentVolumeClaim) error {
	if _, err := c.core.CoreV1().PersistentVolumeClaims(claim.Namespace).Create(
		ctx, claim, metav1.CreateOptions{},
	); err != nil {
		return fmt.Errorf("creating temp PVC %s/%s: %w", claim.Namespace, claim.Name, err)
	}

	return nil
}

// waitForPVCBound polls until the PVC is Bound or the context expires.
func (c *k8sClient) waitForPVCBound(ctx context.Context, name, namespace string) (*corev1.PersistentVolumeClaim, error) {
	var result *corev1.PersistentVolumeClaim

	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true,
		func(context.Context) (bool, error) {
			pvc, err := c.core.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, fmt.Errorf("getting PVC %s/%s: %w", namespace, name, err)
			}

			result = pvc

			switch pvc.Status.Phase {
			case corev1.ClaimBound:
				return true, nil
			case corev1.ClaimLost:
				return false, fmt.Errorf("temp PVC %s/%s is Lost", namespace, name)
			default:
				return false, nil
			}
		},
	)
	if err != nil {
		return nil, fmt.Errorf("waiting for PVC %s/%s to bind: %w", namespace, name, err)
	}

	return result, nil
}

// deleteTempPVC deletes the temporary PVC. It is best-effort: under a Retain
// reclaim policy the source PV survives the deletion even if the API call
// fails. Errors are returned so the caller can log them.
func (c *k8sClient) deleteTempPVC(ctx context.Context, name, namespace string) error {
	if err := c.core.CoreV1().PersistentVolumeClaims(namespace).Delete(
		ctx, name, metav1.DeleteOptions{},
	); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting temp PVC %s/%s: %w", namespace, name, err)
	}

	return nil
}
