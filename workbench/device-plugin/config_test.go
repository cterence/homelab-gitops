package main

import (
	"os"
	"strings"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		devices []Device
		wantErr string
	}{
		{
			name: "valid device",
			devices: []Device{
				{Resource: "homelab.io/render", Path: "/dev/dri/renderD128"},
			},
			wantErr: "",
		},
		{
			name:    "empty config",
			devices: nil,
			wantErr: "no devices configured",
		},
		{
			name: "resource without slash",
			devices: []Device{
				{Resource: "render", Path: "/dev/dri/renderD128"},
			},
			wantErr: `resource "render"`,
		},
		{
			name: "resource with empty domain or name",
			devices: []Device{
				{Resource: "/render", Path: "/dev/dri/renderD128"},
			},
			wantErr: `resource "/render"`,
		},
		{
			name: "invalid domain label",
			devices: []Device{
				{Resource: "Home_Lab.io/render", Path: "/dev/dri/renderD128"},
			},
			wantErr: `resource "Home_Lab.io/render"`,
		},
		{
			name: "relative path",
			devices: []Device{
				{Resource: "homelab.io/render", Path: "dev/dri/renderD128"},
			},
			wantErr: `path "dev/dri/renderD128"`,
		},
		{
			name: "duplicate resource",
			devices: []Device{
				{Resource: "homelab.io/render", Path: "/dev/dri/renderD128"},
				{Resource: "homelab.io/render", Path: "/dev/dri/card0"},
			},
			wantErr: "duplicate resource",
		},
		{
			name: "duplicate path",
			devices: []Device{
				{Resource: "homelab.io/render", Path: "/dev/dri/renderD128"},
				{Resource: "homelab.io/render2", Path: "/dev/dri/renderD128"},
			},
			wantErr: "duplicate path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.devices)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	f := t.TempDir() + "/config.yaml"

	content := "devices:\n  - resource: homelab.io/render\n    path: /dev/dri/renderD128\n  - resource: homelab.io/conbee\n    path: /dev/ttyACM0\n"
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	devices, err := loadConfig(f)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if len(devices) != 2 {
		t.Fatalf("want 2 devices, got %d", len(devices))
	}

	want := Device{Resource: "homelab.io/render", Path: "/dev/dri/renderD128"}
	if devices[0] != want {
		t.Fatalf("want %+v, got %+v", want, devices[0])
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := loadConfig(t.TempDir() + "/missing.yaml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestSocketName(t *testing.T) {
	tests := []struct {
		resource string
		want     string
	}{
		{"homelab.io/render", "homelab-io-render.sock"},
		{"example.com/usb-serial", "example-com-usb-serial.sock"},
	}
	for _, tt := range tests {
		t.Run(tt.resource, func(t *testing.T) {
			if got := socketName(tt.resource); got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestHealthString(t *testing.T) {
	if healthOf(t.TempDir()+"/nope-does-not-exist") != "Unhealthy" {
		t.Fatal("missing device must be Unhealthy")
	}

	f := t.TempDir() + "/exists"
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if healthOf(f) != "Healthy" {
		t.Fatal("existing device must be Healthy")
	}
}
