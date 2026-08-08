package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
)

// RestoreResult holds the outcome of restoring a single cluster.
type RestoreResult struct {
	Info            ClusterInfo
	ClusterName     string
	ObjectStoreName string
	PodName         string
	Error           error
}

func (c *Client) restoreOne(ctx context.Context, targetNS string, ci ClusterInfo) (RestoreResult, error) {
	restoreClusterName := ci.Name + "-restore"
	result := RestoreResult{
		Info:            ci,
		ClusterName:     restoreClusterName,
		ObjectStoreName: ci.ObjectStoreName,
	}

	if err := c.createRestoreObjectStore(ctx, targetNS, ci); err != nil {
		result.Error = fmt.Errorf("creating ObjectStore: %w", err)
		return result, result.Error
	}

	if err := c.waitForObjectStore(ctx, targetNS, ci.ObjectStoreName, 2*time.Minute); err != nil {
		result.Error = fmt.Errorf("waiting for ObjectStore: %w", err)
		return result, result.Error
	}

	if err := c.createRestoreCluster(ctx, targetNS, ci); err != nil {
		result.Error = fmt.Errorf("creating Cluster: %w", err)
		return result, result.Error
	}

	if err := c.waitForClusterReady(ctx, targetNS, restoreClusterName, 10*time.Minute); err != nil {
		result.Error = fmt.Errorf("waiting for Cluster Ready: %w", err)
		return result, result.Error
	}

	podName, err := c.findPrimaryPod(ctx, targetNS, restoreClusterName)
	if err != nil {
		result.Error = fmt.Errorf("finding primary pod: %w", err)
		return result, result.Error
	}

	result.PodName = podName

	slog.Info("cluster restored", "namespace", targetNS, "cluster", restoreClusterName, "pod", podName)

	return result, nil
}

func (c *Client) ensureNamespace(ctx context.Context, name string) error {
	_, err := c.core.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	_, err = c.core.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})

	return err
}

func (c *Client) createRestoreObjectStore(ctx context.Context, targetNS string, ci ClusterInfo) error {
	storeName := ci.ObjectStoreName

	spec := copyMap(ci.ObjectStoreSpec)
	delete(spec, "retentionPolicy")

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "barmancloud.cnpg.io/v1",
			"kind":       "ObjectStore",
			"metadata": map[string]any{
				"name":      storeName,
				"namespace": targetNS,
			},
			"spec": spec,
		},
	}

	_, err := c.dynamic.Resource(objectStoreGVR).Namespace(targetNS).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		// If it already exists from a previous run, delete and recreate
		slog.Info("ObjectStore already exists, replacing", "namespace", targetNS, "name", storeName)

		if delErr := c.dynamic.Resource(objectStoreGVR).Namespace(targetNS).Delete(ctx, storeName, metav1.DeleteOptions{}); delErr != nil {
			return fmt.Errorf("deleting existing ObjectStore %s: %w", storeName, delErr)
		}
		// Wait for deletion
		_ = wait.PollUntilContextTimeout(ctx, 2*time.Second, 1*time.Minute, false, func(ctx context.Context) (bool, error) {
			_, err := c.dynamic.Resource(objectStoreGVR).Namespace(targetNS).Get(ctx, storeName, metav1.GetOptions{})
			return err != nil, nil
		})

		obj.SetResourceVersion("")

		_, err = c.dynamic.Resource(objectStoreGVR).Namespace(targetNS).Create(ctx, obj, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating ObjectStore %s: %w", storeName, err)
		}
	}

	slog.Info("created ObjectStore", "namespace", targetNS, "name", storeName)

	return nil
}

func (c *Client) waitForObjectStore(ctx context.Context, ns, name string, timeout time.Duration) error {
	// The barman-cloud plugin reconciles ObjectStores immediately. We just
	// need to verify the resource exists — it doesn't always write a status
	// section, so we can't rely on that.
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		_, err := c.dynamic.Resource(objectStoreGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		slog.Debug("objectstore ready", "name", name)

		return true, nil
	})
}

func (c *Client) createRestoreCluster(ctx context.Context, targetNS string, ci ClusterInfo) error {
	restoreName := ci.Name + "-restore"

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata": map[string]any{
				"name":      restoreName,
				"namespace": targetNS,
			},
			"spec": map[string]any{
				"instances":   1,
				"postgresUID": int64(26),
				"postgresGID": int64(26),
				"imageCatalogRef": map[string]any{
					"apiGroup": "postgresql.cnpg.io",
					"kind":     "ClusterImageCatalog",
					"major":    ci.MajorVersion,
					"name":     "postgresql",
				},
				"storage": map[string]any{
					"size":         ci.StorageSize,
					"storageClass": "cnpg-restore-test",
				},
				"enableSuperuserAccess": true,
				"bootstrap": map[string]any{
					"recovery": map[string]any{
						"source": "origin",
					},
				},
				"externalClusters": []any{
					map[string]any{
						"name": "origin",
						"plugin": map[string]any{
							"name": "barman-cloud.cloudnative-pg.io",
							"parameters": map[string]any{
								"barmanObjectName": ci.ObjectStoreName,
								"serverName":       ci.Name,
							},
						},
					},
				},
			},
		},
	}

	_, err := c.dynamic.Resource(clusterGVR).Namespace(targetNS).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating Cluster %s: %w", restoreName, err)
	}

	slog.Info("created Cluster", "namespace", targetNS, "name", restoreName)

	return nil
}

func (c *Client) waitForClusterReady(ctx context.Context, ns, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, false, func(ctx context.Context) (bool, error) {
		obj, err := c.dynamic.Resource(clusterGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		status, _ := obj.UnstructuredContent()["status"].(map[string]any)
		if status == nil {
			return false, nil
		}

		phase, _ := status["phase"].(string)
		if phase == "Cluster in healthy state" {
			return true, nil
		}

		slog.Debug("waiting for cluster", "name", name, "phase", phase)

		return false, nil
	})
}

func (c *Client) findPrimaryPod(ctx context.Context, ns, clusterName string) (string, error) {
	pods, err := c.core.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "cnpg.io/cluster=" + clusterName,
	})
	if err != nil {
		return "", err
	}

	for _, pod := range pods.Items {
		role := pod.Labels["cnpg.io/instanceRole"]
		if role == "primary" || role == "ready" {
			return pod.Name, nil
		}
	}

	if len(pods.Items) > 0 {
		return pods.Items[0].Name, nil
	}

	return "", fmt.Errorf("no pods found for cluster %s", clusterName)
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}

	return out
}
