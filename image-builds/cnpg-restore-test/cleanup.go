package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// CleanupError records a cleanup failure for a specific cluster.
type CleanupError struct {
	ClusterName string
	Error       error
}

// cleanupOne deletes a single cluster, its PVCs, and ObjectStore.
func (c *Client) cleanupOne(ctx context.Context, cfg Config, vr VerifyResult) []CleanupError {
	var errs []CleanupError
	clusterName := vr.ClusterName
	if clusterName == "" {
		return errs
	}

	if err := c.dynamic.Resource(clusterGVR).Namespace(cfg.Namespace).Delete(ctx, clusterName, metav1.DeleteOptions{}); err != nil {
		slog.Debug("cluster delete result", "cluster", clusterName, "error", err)
	}

	_ = wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, false, func(ctx context.Context) (bool, error) {
		_, err := c.dynamic.Resource(clusterGVR).Namespace(cfg.Namespace).Get(ctx, clusterName, metav1.GetOptions{})
		return err != nil, nil
	})

	pvcs, err := c.core.CoreV1().PersistentVolumeClaims(cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("cnpg.io/cluster=%s", clusterName),
	})
	if err != nil {
		slog.Warn("listing PVCs", "cluster", clusterName, "error", err)
	} else {
		for _, pvc := range pvcs.Items {
			if err := c.core.CoreV1().PersistentVolumeClaims(cfg.Namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil {
				slog.Warn("deleting PVC", "pvc", pvc.Name, "error", err)
			}
		}
	}

	storeName := vr.ObjectStoreName
	if storeName == "" {
		storeName = vr.Info.ObjectStoreName
	}
	if err := c.dynamic.Resource(objectStoreGVR).Namespace(cfg.Namespace).Delete(ctx, storeName, metav1.DeleteOptions{}); err != nil {
		slog.Warn("deleting ObjectStore", "name", storeName, "error", err)
	}

	slog.Info("cleanup complete", "cluster", clusterName)
	return errs
}

// Cleanup deletes all restore clusters, their PVCs, ObjectStores, and
// the shared S3 credentials secret. Runs best-effort.
func (c *Client) Cleanup(ctx context.Context, cfg Config, results []VerifyResult) []CleanupError {
	var errs []CleanupError

	for _, vr := range results {
		clusterName := vr.ClusterName
		if clusterName == "" {
			continue
		}

		if err := c.dynamic.Resource(clusterGVR).Namespace(cfg.Namespace).Delete(ctx, clusterName, metav1.DeleteOptions{}); err != nil {
			slog.Debug("cluster delete result", "cluster", clusterName, "error", err)
		}

		_ = wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, false, func(ctx context.Context) (bool, error) {
			_, err := c.dynamic.Resource(clusterGVR).Namespace(cfg.Namespace).Get(ctx, clusterName, metav1.GetOptions{})
			return err != nil, nil
		})

		// Also check for PVCs without the cluster label — they may exist
		// even if the Cluster CR was never created (leftover from a crash)
		pvcs, err := c.core.CoreV1().PersistentVolumeClaims(cfg.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("cnpg.io/cluster=%s", clusterName),
		})
		if err != nil {
			slog.Warn("listing PVCs", "cluster", clusterName, "error", err)
		} else {
			for _, pvc := range pvcs.Items {
				if err := c.core.CoreV1().PersistentVolumeClaims(cfg.Namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil {
					slog.Warn("deleting PVC", "pvc", pvc.Name, "error", err)
				}
			}
		}

		storeName := vr.ObjectStoreName
		if storeName == "" {
			storeName = vr.Info.ObjectStoreName
		}
		if err := c.dynamic.Resource(objectStoreGVR).Namespace(cfg.Namespace).Delete(ctx, storeName, metav1.DeleteOptions{}); err != nil {
			slog.Warn("deleting ObjectStore", "name", storeName, "error", err)
		}

		slog.Info("cleanup complete", "cluster", clusterName)
	}

	if err := c.core.CoreV1().Secrets(cfg.Namespace).Delete(ctx, "cnpg-backup-s3-creds", metav1.DeleteOptions{}); err != nil {
		slog.Warn("deleting shared secret", "error", err)
	}

	return errs
}
