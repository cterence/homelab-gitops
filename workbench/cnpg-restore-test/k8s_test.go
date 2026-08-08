package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestHomeDir(t *testing.T) {
	tests := []struct {
		name    string
		home    string
		profile string
		want    string
	}{
		{"HOME set", "/Users/test", "", "/Users/test"},
		{"HOME empty, USERPROFILE set", "", "/Users/test", "/Users/test"},
		{"both empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", tt.home)
			t.Setenv("USERPROFILE", tt.profile)

			got := homeDir()
			if got != tt.want {
				t.Errorf("homeDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseGVR(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		version  string
		want     schema.GroupVersionResource
	}{
		{
			name:     "core resource bare name",
			resource: "pods",
			version:  "v1",
			want:     schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
		},
		{
			name:     "CNPG clusters CRD",
			resource: "clusters.postgresql.cnpg.io",
			version:  "v1",
			want:     schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"},
		},
		{
			name:     "barmancloud objectstores CRD",
			resource: "objectstores.barmancloud.cnpg.io",
			version:  "v1",
			want:     schema.GroupVersionResource{Group: "barmancloud.cnpg.io", Version: "v1", Resource: "objectstores"},
		},
		{
			name:     "CNPG backups CRD",
			resource: "backups.postgresql.cnpg.io",
			version:  "v1",
			want:     schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "backups"},
		},
		{
			name:     "core services bare name",
			resource: "services",
			version:  "v1",
			want:     schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGVR(tt.resource, tt.version)
			if got != tt.want {
				t.Errorf("parseGVR(%q, %q) = %+v, want %+v", tt.resource, tt.version, got, tt.want)
			}
		})
	}
}

func TestDynamic_ReturnsNonNil(t *testing.T) {
	t.Helper()

	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClient(scheme)
	c := &Client{dynamic: dyn}

	ri := c.Dynamic("pods", "v1", "default")
	if ri == nil {
		t.Fatal("expected non-nil ResourceInterface")
	}
}

func TestNewClient_InvalidKubeconfigPath(t *testing.T) {
	cfg := Config{KubeconfigPath: "/nonexistent/path/to/kubeconfig"}

	_, err := NewClient(cfg)
	if err == nil {
		t.Fatal("expected error for invalid kubeconfig path")
	}
}
