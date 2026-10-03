package main

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// cleanupOne deletes a single cluster, its PVCs, and ObjectStore.
func (c *Client) cleanupOne(ctx context.Context, cfg Config, vr VerifyResult) {
	clusterName := vr.ClusterName
	if clusterName == "" {
		return
	}

	if err := c.dynamic.Resource(clusterGVR).Namespace(cfg.Namespace).Delete(ctx, clusterName, metav1.DeleteOptions{}); err != nil {
		slog.Debug("cluster delete result", "cluster", clusterName, "error", err)
	}

	_ = wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, false, func(ctx context.Context) (bool, error) {
		_, err := c.dynamic.Resource(clusterGVR).Namespace(cfg.Namespace).Get(ctx, clusterName, metav1.GetOptions{})
		return err != nil, nil
	})

	pvcs, err := c.core.CoreV1().PersistentVolumeClaims(cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "cnpg.io/cluster=" + clusterName,
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
		slog.Debug("deleting ObjectStore", "name", storeName, "error", err)
	}

	slog.Info("cleanup complete", "cluster", clusterName)
}

// Cleanup runs cleanupOne for every result. Best-effort.
func (c *Client) Cleanup(ctx context.Context, cfg Config, results []VerifyResult) {
	for _, vr := range results {
		c.cleanupOne(ctx, cfg, vr)
	}
}
