package main

import (
	"testing"
)

func TestParseStorageBytes(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"5Gi", "5Gi", 5 * 1024 * 1024 * 1024, false},
		{"10Gi", "10Gi", 10 * 1024 * 1024 * 1024, false},
		{"1Mi", "1Mi", 1024 * 1024, false},
		{"empty", "", 0, true},
		{"invalid", "abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStorageBytes(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseStorageBytes(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}

			if got != tt.want {
				t.Errorf("parseStorageBytes(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseAllDBSizes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]int64
	}{
		{
			name:  "single db",
			input: "temporal|12345678",
			want:  map[string]int64{"temporal": 12345678},
		},
		{
			name:  "multiple dbs",
			input: "temporal|12345678\ntemporal_visibility|9876543",
			want:  map[string]int64{"temporal": 12345678, "temporal_visibility": 9876543},
		},
		{
			name:  "with whitespace",
			input: "  temporal  |  12345678  \n  firefly  |  9876  ",
			want:  map[string]int64{"temporal": 12345678, "firefly": 9876},
		},
		{
			name:  "empty output",
			input: "",
			want:  map[string]int64{},
		},
		{
			name:  "malformed lines skipped",
			input: "temporal|12345678\nbadline\ntemporal_visibility|9876",
			want:  map[string]int64{"temporal": 12345678, "temporal_visibility": 9876},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAllDBSizes(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseAllDBSizes() got %d entries, want %d", len(got), len(tt.want))
			}

			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseAllDBSizes()[%q] = %d, want %d", k, got[k], v)
				}
			}
		})
	}
}

func TestMatchClusterFilter(t *testing.T) {
	tests := []struct {
		name     string
		filter   string
		fullName string
		want     bool
	}{
		{"exact match", "pocket-id/pocket-id-cnpg-cluster", "pocket-id/pocket-id-cnpg-cluster", true},
		{"no match", "pocket-id/*", "vaultwarden/vaultwarden-cnpg-cluster", false},
		{"namespace wildcard", "pocket-id/*", "pocket-id/pocket-id-cnpg-cluster", true},
		{"name wildcard", "*/vaultwarden-cnpg-cluster", "vaultwarden/vaultwarden-cnpg-cluster", true},
		{"comma separated match first", "pocket-id/*,vaultwarden/*", "pocket-id/pocket-id-cnpg-cluster", true},
		{"comma separated match second", "pocket-id/*,vaultwarden/*", "vaultwarden/vaultwarden-cnpg-cluster", true},
		{"comma separated no match", "pocket-id/*,vaultwarden/*", "immich/immich-cnpg-cluster-blue", false},
		{"empty filter matches nothing", "", "any/cluster", false},
		{"partial glob", "pocket-id/pocket*", "pocket-id/pocket-id-cnpg-cluster", true},
		{"question mark wildcard", "pocket-id/pocket-id-cnpg-cluste?", "pocket-id/pocket-id-cnpg-cluster", true},
		{"whitespace trimmed", " pocket-id/* , vaultwarden/* ", "vaultwarden/vaultwarden-cnpg-cluster", true},
		{"invalid glob ignored", "[invalid, pocket-id/*", "pocket-id/pocket-id-cnpg-cluster", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchClusterFilter(tt.filter, tt.fullName)
			if got != tt.want {
				t.Errorf("matchClusterFilter(%q, %q) = %v, want %v", tt.filter, tt.fullName, got, tt.want)
			}
		})
	}
}
