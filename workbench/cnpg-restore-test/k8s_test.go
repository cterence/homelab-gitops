package main

import (
	"testing"
)

func TestNewClient_InvalidKubeconfigPath(t *testing.T) {
	cfg := Config{KubeconfigPath: "/nonexistent/path/to/kubeconfig"}

	_, err := NewClient(cfg)
	if err == nil {
		t.Fatal("expected error for invalid kubeconfig path")
	}
}
