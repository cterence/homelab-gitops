package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

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
