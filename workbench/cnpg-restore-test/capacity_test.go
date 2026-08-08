package main

import (
	"slices"
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
