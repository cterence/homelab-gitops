package main

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ClusterInfo holds everything we need to restore a cluster.
type ClusterInfo struct {
	Namespace       string
	Name            string
	StorageSize     string
	StorageBytes    int64
	MajorVersion    int64
	Database        string
	ObjectStoreName string
	ObjectStoreSpec map[string]any
}

var (
	clusterGVR = schema.GroupVersionResource{
		Group:    "postgresql.cnpg.io",
		Version:  "v1",
		Resource: "clusters",
	}
	objectStoreGVR = schema.GroupVersionResource{
		Group:    "barmancloud.cnpg.io",
		Version:  "v1",
		Resource: "objectstores",
	}
	backupGVR = schema.GroupVersionResource{
		Group:    "postgresql.cnpg.io",
		Version:  "v1",
		Resource: "backups",
	}
)

// DiscoverClusters finds all CNPG clusters using the barman-cloud plugin
// and collects their ObjectStore specs. If cfg.ClusterFilter is set, only
// clusters whose full name (namespace/name) matches one of the comma-separated
// glob patterns are returned.
func (c *Client) DiscoverClusters(ctx context.Context, cfg Config) ([]ClusterInfo, error) {
	clusters, err := c.dynamic.Resource(clusterGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing clusters: %w", err)
	}

	var infos []ClusterInfo

	for _, cluster := range clusters.Items {
		fullName := cluster.GetNamespace() + "/" + cluster.GetName()
		if cfg.ClusterFilter != "" && !matchClusterFilter(cfg.ClusterFilter, fullName) {
			slog.Debug("cluster filtered out", "cluster", fullName)
			continue
		}

		info, err := c.parseCluster(ctx, cluster.UnstructuredContent())
		if err != nil {
			slog.Warn("skipping cluster", "namespace", cluster.GetNamespace(), "name", cluster.GetName(), "error", err)
			continue
		}

		if info == nil {
			continue
		}

		infos = append(infos, *info)
	}

	slog.Info("discovered clusters", "count", len(infos), "filter", cfg.ClusterFilter)

	return infos, nil
}

func (c *Client) parseCluster(ctx context.Context, obj map[string]any) (*ClusterInfo, error) {
	spec, _ := obj["spec"].(map[string]any)
	metadata, _ := obj["metadata"].(map[string]any)

	ns, _ := metadata["namespace"].(string)
	name, _ := metadata["name"].(string)

	// Find the barman-cloud plugin and its barmanObjectName
	plugins, _ := spec["plugins"].([]any)

	var objectStoreName string

	for _, p := range plugins {
		plugin, _ := p.(map[string]any)
		if plugin["name"] != "barman-cloud.cloudnative-pg.io" {
			continue
		}

		params, _ := plugin["parameters"].(map[string]any)
		objectStoreName, _ = params["barmanObjectName"].(string)
	}

	if objectStoreName == "" {
		slog.Info("skipping non-plugin cluster", "namespace", ns, "name", name)
		return nil, nil
	}

	// Get storage size
	storage, _ := spec["storage"].(map[string]any)
	storageSize, _ := storage["size"].(string)

	storageBytes, err := parseStorageBytes(storageSize)
	if err != nil {
		return nil, fmt.Errorf("parsing storage size: %w", err)
	}

	// Get postgres major version
	imageCatalogRef, _ := spec["imageCatalogRef"].(map[string]any)
	major, _ := imageCatalogRef["major"].(int64)

	// Get database name from bootstrap.initdb.database
	bootstrap, _ := spec["bootstrap"].(map[string]any)
	initdb, _ := bootstrap["initdb"].(map[string]any)
	database, _ := initdb["database"].(string)

	// Fetch the ObjectStore spec
	objStore, err := c.dynamic.Resource(objectStoreGVR).Namespace(ns).Get(ctx, objectStoreName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting ObjectStore %s/%s: %w", ns, objectStoreName, err)
	}

	objStoreSpec, _ := objStore.UnstructuredContent()["spec"].(map[string]any)

	// Verify at least one completed backup exists
	backups, err := c.dynamic.Resource(backupGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		slog.Warn("failed to list backups, proceeding anyway", "namespace", ns, "error", err)
	} else {
		hasCompleted := false

		for _, b := range backups.Items {
			status, _ := b.UnstructuredContent()["status"].(map[string]any)
			if phase, _ := status["phase"].(string); phase == "completed" {
				hasCompleted = true
				break
			}
		}

		if !hasCompleted {
			slog.Info("skipping cluster with no completed backup", "namespace", ns, "name", name)
			return nil, nil
		}
	}

	return &ClusterInfo{
		Namespace:       ns,
		Name:            name,
		StorageSize:     storageSize,
		StorageBytes:    storageBytes,
		MajorVersion:    major,
		Database:        database,
		ObjectStoreName: objectStoreName,
		ObjectStoreSpec: objStoreSpec,
	}, nil
}

// parseStorageBytes converts a Kubernetes quantity string (e.g., "5Gi") to bytes.
func parseStorageBytes(s string) (int64, error) {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, err
	}

	return q.Value(), nil
}

// parseAllDBSizes parses newline-separated "dbname|size" output from psql.
func parseAllDBSizes(output string) map[string]int64 {
	sizes := map[string]int64{}

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) == 2 {
			size, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
			if err == nil {
				sizes[strings.TrimSpace(parts[0])] = size
			}
		}
	}

	return sizes
}

// matchClusterFilter checks if a cluster's full name (namespace/name)
// matches any of the comma-separated glob patterns. Each pattern supports
// standard path.Match glob syntax (* and ? wildcards). Examples:
//   - "pocket-id/*" matches all clusters in the pocket-id namespace
//   - "*/vaultwarden-cnpg-cluster" matches that cluster name in any namespace
//   - "pocket-id/pocket-id-cnpg-cluster,vaultwarden/vaultwarden-cnpg-cluster"
func matchClusterFilter(filter, fullName string) bool {
	for _, pattern := range strings.Split(filter, ",") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		matched, err := path.Match(pattern, fullName)
		if err != nil {
			slog.Warn("invalid glob pattern", "pattern", pattern, "error", err)
			continue
		}

		if matched {
			return true
		}
	}

	return false
}
