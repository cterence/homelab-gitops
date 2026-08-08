package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"k8s.io/client-go/tools/remotecommand"
)

// VerifyResult holds the outcome of verifying a single restored cluster.
type VerifyResult struct {
	RestoreResult
	Passed        bool
	DBSizes       map[string]int64
	SourceDBSizes map[string]int64
	Error         error
}

// VerifyClusters execs into each restored pod and compares database sizes
// against the source cluster.
func (c *Client) VerifyClusters(ctx context.Context, cfg Config, results []RestoreResult) []VerifyResult {
	vr := make([]VerifyResult, len(results))
	for i, r := range results {
		vr[i] = c.verifyOne(ctx, cfg, r)
	}
	return vr
}

// verifyOne verifies a single restored cluster.
func (c *Client) verifyOne(ctx context.Context, cfg Config, r RestoreResult) VerifyResult {
	vr := VerifyResult{RestoreResult: r}
	if r.Error != nil {
		vr.Error = r.Error
		return vr
	}
	if r.PodName == "" {
		vr.Error = fmt.Errorf("no pod name")
		return vr
	}

	restoredSizes, err := c.getDatabaseSizes(ctx, cfg.Namespace, r.PodName)
	if err != nil {
		vr.Error = fmt.Errorf("querying restored DB sizes: %w", err)
		return vr
	}
	vr.DBSizes = restoredSizes

	sourcePod, err := c.findPrimaryPod(ctx, r.Info.Namespace, r.Info.Name)
	if err != nil {
		slog.Warn("could not find source pod, skipping size comparison", "cluster", r.Info.Name, "error", err)
		vr.Passed = len(restoredSizes) > 0
		for _, size := range restoredSizes {
			if size == 0 {
				vr.Passed = false
			}
		}
		return vr
	}

	sourceSizes, err := c.getDatabaseSizes(ctx, r.Info.Namespace, sourcePod)
	if err != nil {
		slog.Warn("could not query source DB sizes", "cluster", r.Info.Name, "error", err)
		vr.Passed = len(restoredSizes) > 0
		return vr
	}
	vr.SourceDBSizes = sourceSizes

	vr.Passed = compareDBSizes(sourceSizes, restoredSizes, 0.1)
	return vr
}

// getDatabaseSizes execs into the postgres container and returns a map of
// database name to size in bytes (excluding postgres, template0, template1).
func (c *Client) getDatabaseSizes(ctx context.Context, ns, podName string) (map[string]int64, error) {
	query := `SELECT datname || '|' || pg_database_size(datname) FROM pg_database WHERE datname NOT IN ('postgres','template0','template1');`

	output, err := c.execPSQL(ctx, ns, podName, query)
	if err != nil {
		return nil, err
	}

	return parseAllDBSizes(output), nil
}

// execPSQL runs a psql command in the postgres container of a pod.
func (c *Client) execPSQL(ctx context.Context, ns, podName, query string) (string, error) {
	req := c.core.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(ns).
		SubResource("exec").
		Param("container", "postgres").
		Param("command", "psql").
		Param("command", "-U").
		Param("command", "postgres").
		Param("command", "-t").
		Param("command", "-A").
		Param("command", "-c").
		Param("command", query).
		Param("stderr", "true").
		Param("stdout", "true")

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("creating executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return "", fmt.Errorf("exec failed: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}

// compareDBSizes checks that every source database has a matching restored
// database within the given tolerance (fraction, e.g., 0.1 for 10%).
func compareDBSizes(source, restored map[string]int64, tolerance float64) bool {
	for db, srcSize := range source {
		rstSize, ok := restored[db]
		if !ok {
			slog.Warn("database missing in restored cluster", "database", db)
			return false
		}
		if srcSize == 0 {
			continue
		}
		ratio := float64(rstSize) / float64(srcSize)
		diff := 1 - ratio
		if diff < 0 {
			diff = -diff
		}
		if diff > tolerance {
			slog.Warn("database size mismatch", "database", db, "source", srcSize, "restored", rstSize, "ratio", ratio)
			return false
		}
	}
	return true
}
