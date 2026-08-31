package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
)

func TestCheckCapacity_Logic(t *testing.T) {
	// The capacity check uses the sum of the N largest cluster sizes
	// (where N = concurrency), multiplied by the margin.
	tests := []struct {
		name        string
		sizes       []string
		concurrency int
		margin      float64
		availBytes  int64
		wantOK      bool
	}{
		{
			name:        "enough space for top 3",
			sizes:       []string{"5Gi", "10Gi", "5Gi", "8Gi", "10Gi"},
			concurrency: 3,
			margin:      1.5,
			availBytes:  100 * 1024 * 1024 * 1024,
			wantOK:      true,
		},
		{
			name:        "not enough for top 3",
			sizes:       []string{"10Gi", "10Gi", "10Gi", "5Gi"},
			concurrency: 3,
			margin:      1.5,
			availBytes:  40 * 1024 * 1024 * 1024,
			wantOK:      false,
		},
		{
			name:        "concurrency > clusters, uses all",
			sizes:       []string{"5Gi", "10Gi"},
			concurrency: 5,
			margin:      1.5,
			availBytes:  100 * 1024 * 1024 * 1024,
			wantOK:      true,
		},
		{
			name:        "concurrency 1, only largest",
			sizes:       []string{"5Gi", "10Gi", "8Gi"},
			concurrency: 1,
			margin:      1.0,
			availBytes:  10 * 1024 * 1024 * 1024,
			wantOK:      true,
		},
		{
			name:        "concurrency 1, largest doesn't fit",
			sizes:       []string{"5Gi", "10Gi", "8Gi"},
			concurrency: 1,
			margin:      1.0,
			availBytes:  9 * 1024 * 1024 * 1024,
			wantOK:      false,
		},
		{
			name:        "empty clusters",
			sizes:       []string{},
			concurrency: 3,
			margin:      1.5,
			availBytes:  0,
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sizes := make([]int64, len(tt.sizes))
			for i, s := range tt.sizes {
				b, err := parseStorageBytes(s)
				if err != nil {
					t.Fatalf("parseStorageBytes(%q): %v", s, err)
				}

				sizes[i] = b
			}

			slices.Sort(sizes)
			slices.Reverse(sizes)

			n := tt.concurrency
			if n > len(sizes) {
				n = len(sizes)
			}

			var totalNeeded int64
			for _, s := range sizes[:n] {
				totalNeeded += s
			}

			totalNeeded = int64(float64(totalNeeded) * tt.margin)

			gotOK := totalNeeded <= tt.availBytes
			if gotOK != tt.wantOK {
				t.Errorf("capacity check: needed=%d, avail=%d, margin=%.1f, n=%d -> got %v, want %v",
					totalNeeded, tt.availBytes, tt.margin, n, gotOK, tt.wantOK)
			}
		})
	}
}

// fakePrometheus returns a JSON response simulating Prometheus API output
// for node_filesystem_avail_bytes with multiple series (one per node).
func fakePrometheus(t *testing.T, series []struct {
	instance   string
	mountpoint string
	value      int64
}) *httptest.Server {
	t.Helper()

	results := make([]struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`
	}, len(series))

	for i, s := range series {
		results[i] = struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}{
			Metric: map[string]string{
				"instance":   s.instance,
				"mountpoint": s.mountpoint,
				"fstype":     "ext4",
			},
			Value: [2]any{1788175007, formatInt64(s.value)},
		}
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": results,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func formatInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestQueryPrometheusMin_MultipleNodes(t *testing.T) {
	series := []struct {
		instance   string
		mountpoint string
		value      int64
	}{
		{"192.168.1.141:9100", "/mnt/mx500-02", 1451956625408}, // homelab3 ~1.35 TiB
		{"192.168.1.54:9100", "/mnt/mx500-01", 1451947438080},  // homelab2 ~1.35 TiB
	}

	srv := fakePrometheus(t, series)
	defer srv.Close()

	got, err := queryPrometheusMin(srv.URL, "/mnt/mx500-0.")
	if err != nil {
		t.Fatalf("queryPrometheusMin: %v", err)
	}

	want := int64(1451947438080) // homelab2 has less
	if got != want {
		t.Errorf("got %d, want %d (min of the two nodes)", got, want)
	}
}

func TestQueryPrometheusMin_SingleNode(t *testing.T) {
	series := []struct {
		instance   string
		mountpoint string
		value      int64
	}{
		{"192.168.1.141:9100", "/mnt/mx500-02", 1451956625408},
	}

	srv := fakePrometheus(t, series)
	defer srv.Close()

	got, err := queryPrometheusMin(srv.URL, "/mnt/mx500-0.")
	if err != nil {
		t.Fatalf("queryPrometheusMin: %v", err)
	}

	if got != 1451956625408 {
		t.Errorf("got %d, want 1451956625408", got)
	}
}

func TestQueryPrometheusMin_EmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"status": "success",
			"data":   map[string]any{"result": []any{}},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	_, err := queryPrometheusMin(srv.URL, "/mnt/mx500-0.")
	if err == nil {
		t.Fatal("expected error for empty result, got nil")
	}
}

func TestQueryPrometheusMin_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := queryPrometheusMin(srv.URL, "/mnt/mx500-0.")
	if err == nil {
		t.Fatal("expected error for HTTP 503, got nil")
	}
}
