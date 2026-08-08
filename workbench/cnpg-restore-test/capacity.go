package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
)

// promResponse is the subset of Prometheus API response we need.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Value [2]any `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// checkCapacity queries Prometheus for available disk space and computes
// the maximum concurrency that fits. Returns the max concurrency and error.
// If fixed concurrency is set (cfg.Concurrency > 0), returns that value
// or 0 if it doesn't fit.
func computeMaxConcurrency(cfg Config, clusters []ClusterInfo) (int, error) {
	avail, err := queryPrometheus(cfg.PrometheusURL, `node_filesystem_avail_bytes{mountpoint="/",fstype!~"tmpfs|overlay"}`)
	if err != nil {
		return 0, fmt.Errorf("querying prometheus for disk space: %w", err)
	}

	// Sort cluster sizes descending
	sizes := make([]int64, len(clusters))
	for i, c := range clusters {
		sizes[i] = c.StorageBytes
	}
	slices.Sort(sizes)
	slices.Reverse(sizes)

	// If fixed concurrency, check if it fits
	if cfg.Concurrency > 0 {
		n := cfg.Concurrency
		if n > len(sizes) {
			n = len(sizes)
		}
		var totalNeeded int64
		for _, s := range sizes[:n] {
			totalNeeded += s
		}
		totalNeeded = int64(float64(totalNeeded) * cfg.CapacityMargin)

		slog.Info("capacity check",
			"needed_bytes", totalNeeded,
			"needed_human_readable", humanBytes(totalNeeded),
			"available_bytes", avail,
			"available_human_readable", humanBytes(avail),
			"margin", cfg.CapacityMargin,
			"concurrent_clusters", n,
			"total_clusters", len(clusters),
		)

		if totalNeeded > avail {
			slog.Warn("insufficient disk space for requested concurrency",
				"needed", humanBytes(totalNeeded),
				"available", humanBytes(avail),
				"concurrency", n,
			)
			return 0, nil
		}
		return n, nil
	}

	// Auto-detect: try from max down to 1
	maxConc := len(sizes)
	for n := maxConc; n >= 1; n-- {
		var totalNeeded int64
		for _, s := range sizes[:n] {
			totalNeeded += s
		}
		totalNeeded = int64(float64(totalNeeded) * cfg.CapacityMargin)

		if totalNeeded <= avail {
			slog.Info("capacity check",
				"needed_bytes", totalNeeded,
				"needed_human_readable", humanBytes(totalNeeded),
				"available_bytes", avail,
				"available_human_readable", humanBytes(avail),
				"margin", cfg.CapacityMargin,
				"concurrent_clusters", n,
				"total_clusters", len(clusters),
			)
			return n, nil
		}
	}

	// Even 1 doesn't fit
	oneNeeded := int64(float64(sizes[0]) * cfg.CapacityMargin)
	slog.Error("insufficient disk space for even 1 cluster",
		"needed", humanBytes(oneNeeded),
		"available", humanBytes(avail),
	)
	return 0, nil
}

// humanBytes formats a byte count as a human-readable string (e.g., "45.0 GiB").
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// queryPrometheus runs an instant query and returns the first scalar value.
func queryPrometheus(promURL, query string) (int64, error) {
	u, err := url.Parse(promURL)
	if err != nil {
		return 0, err
	}
	u.Path = "/api/v1/query"
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	resp, err := http.Get(u.String())
	if err != nil {
		return 0, fmt.Errorf("prometheus query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("prometheus returned %s", resp.Status)
	}

	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return 0, fmt.Errorf("decoding prometheus response: %w", err)
	}
	if pr.Status != "success" || len(pr.Data.Result) == 0 {
		return 0, fmt.Errorf("prometheus query unsuccessful or empty: %s", pr.Status)
	}

	valStr, ok := pr.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected prometheus value type: %T", pr.Data.Result[0].Value[1])
	}

	var val int64
	if _, err := fmt.Sscanf(valStr, "%d", &val); err != nil {
		return 0, fmt.Errorf("parsing prometheus value %q: %w", valStr, err)
	}
	return val, nil
}
