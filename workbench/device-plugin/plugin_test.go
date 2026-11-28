package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"

	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// shortTempDir keeps socket paths under the ~104 byte unix socket limit, which
// t.TempDir names exceed on darwin.
func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "dp")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

// useTempPluginDir points the plugin at a writable directory and restores the
// real paths when the test ends.
func useTempPluginDir(t *testing.T) string {
	t.Helper()

	dir := shortTempDir(t)
	devicePluginDir = dir
	kubeletSocket = filepath.Join(dir, "kubelet.sock")

	previousWatch := socketWatchInterval
	socketWatchInterval = 20 * time.Millisecond

	t.Cleanup(func() {
		devicePluginDir = v1beta1.DevicePluginPath
		kubeletSocket = v1beta1.KubeletSocket
		socketWatchInterval = previousWatch
	})

	return dir
}

type fakeKubelet struct {
	v1beta1.UnimplementedRegistrationServer
	registered chan *v1beta1.RegisterRequest
}

func (f *fakeKubelet) Register(_ context.Context, req *v1beta1.RegisterRequest) (*v1beta1.Empty, error) {
	f.registered <- req

	return &v1beta1.Empty{}, nil
}

func startFakeKubelet(t *testing.T, sock string) *fakeKubelet {
	t.Helper()

	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listening on fake kubelet socket: %v", err)
	}

	server := grpc.NewServer()
	fake := &fakeKubelet{registered: make(chan *v1beta1.RegisterRequest, 8)}
	v1beta1.RegisterRegistrationServer(server, fake)

	go func() { _ = server.Serve(listener) }()

	t.Cleanup(server.Stop)

	return fake
}

func waitForRegister(t *testing.T, registered <-chan *v1beta1.RegisterRequest) *v1beta1.RegisterRequest {
	t.Helper()

	select {
	case req := <-registered:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a registration")

		return nil
	}
}

func TestServeDeviceReRegistersAfterSocketRemoval(t *testing.T) {
	dir := useTempPluginDir(t)
	fake := startFakeKubelet(t, kubeletSocket)
	device := Device{Resource: "homelab.io/test", Path: filepath.Join(dir, "dev")}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- serveDevice(ctx, device) }()

	waitForRegister(t, fake.registered)

	sock := socketPath(device.Resource)

	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not created: %v", err)
	}

	// kubelet deletes the plugin sockets when it restarts
	if err := os.Remove(sock); err != nil {
		t.Fatalf("removing socket: %v", err)
	}

	waitForRegister(t, fake.registered)

	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not recreated after re-registration: %v", err)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveDevice returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveDevice did not stop after cancel")
	}
}

func TestProbeDevices(t *testing.T) {
	tests := []struct {
		name    string
		serving bool
		wantErr bool
	}{
		{name: "serving plugin", serving: true},
		{name: "plugin not running", serving: false, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := useTempPluginDir(t)
			device := Device{Resource: "homelab.io/test", Path: filepath.Join(dir, "dev")}

			configPath := filepath.Join(dir, "config.yaml")
			config := "devices:\n  - resource: " + device.Resource + "\n    path: " + device.Path + "\n"

			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatalf("writing config: %v", err)
			}

			if tt.serving {
				fake := startFakeKubelet(t, kubeletSocket)

				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)

				go func() { done <- serveDevice(ctx, device) }()

				waitForRegister(t, fake.registered)

				t.Cleanup(func() {
					cancel()

					select {
					case err := <-done:
						if err != nil {
							t.Errorf("serveDevice returned %v, want nil", err)
						}
					case <-time.After(5 * time.Second):
						t.Error("serveDevice did not stop after cancel")
					}
				})
			}

			err := probeDevices(context.Background(), configPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("probeDevices() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
