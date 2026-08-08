package main

import (
	"testing"
)

func TestCompareDBSizes(t *testing.T) {
	tests := []struct {
		name      string
		source    map[string]int64
		restored  map[string]int64
		tolerance float64
		want      bool
	}{
		{
			name:      "exact match",
			source:    map[string]int64{"db1": 1000},
			restored:  map[string]int64{"db1": 1000},
			tolerance: 0.1,
			want:      true,
		},
		{
			name:      "within 10%",
			source:    map[string]int64{"db1": 1000},
			restored:  map[string]int64{"db1": 950},
			tolerance: 0.1,
			want:      true,
		},
		{
			name:      "outside 10%",
			source:    map[string]int64{"db1": 1000},
			restored:  map[string]int64{"db1": 800},
			tolerance: 0.1,
			want:      false,
		},
		{
			name:      "missing db in restored",
			source:    map[string]int64{"db1": 1000, "db2": 500},
			restored:  map[string]int64{"db1": 1000},
			tolerance: 0.1,
			want:      false,
		},
		{
			name:      "empty source",
			source:    map[string]int64{},
			restored:  map[string]int64{"db1": 1000},
			tolerance: 0.1,
			want:      true,
		},
		{
			name:      "source has zero size db",
			source:    map[string]int64{"db1": 0, "db2": 1000},
			restored:  map[string]int64{"db1": 500, "db2": 950},
			tolerance: 0.1,
			want:      true,
		},
		{
			name:      "multiple dbs all pass",
			source:    map[string]int64{"temporal": 10000, "temporal_visibility": 5000},
			restored:  map[string]int64{"temporal": 9800, "temporal_visibility": 4900},
			tolerance: 0.1,
			want:      true,
		},
		{
			name:      "multiple dbs one fails",
			source:    map[string]int64{"temporal": 10000, "temporal_visibility": 5000},
			restored:  map[string]int64{"temporal": 9800, "temporal_visibility": 3000},
			tolerance: 0.1,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareDBSizes(tt.source, tt.restored, tt.tolerance)
			if got != tt.want {
				t.Errorf("compareDBSizes() = %v, want %v", got, tt.want)
			}
		})
	}
}
