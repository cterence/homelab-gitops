package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	rescanInterval = 15 * time.Second
	probeTimeout   = 2 * time.Second
)

// socketWatchInterval is how often a registered plugin checks that kubelet has
// not removed its socket. Variable so tests can shorten it.
var socketWatchInterval = 2 * time.Second

// Overridable in tests, which cannot write to /var/lib/kubelet.
var (
	devicePluginDir = v1beta1.DevicePluginPath
	kubeletSocket   = v1beta1.KubeletSocket
)

func socketPath(resource string) string {
	return filepath.Join(devicePluginDir, socketName(resource))
}

func advertisedDevices(count int, health string) []*v1beta1.Device {
	devices := make([]*v1beta1.Device, count)
	for i := range devices {
		devices[i] = &v1beta1.Device{ID: strconv.Itoa(i), Health: health}
	}

	return devices
}

type rawPlugin struct {
	v1beta1.UnimplementedDevicePluginServer
	device Device
}

func (p *rawPlugin) GetDevicePluginOptions(context.Context, *v1beta1.Empty) (*v1beta1.DevicePluginOptions, error) {
	return &v1beta1.DevicePluginOptions{}, nil
}

func (p *rawPlugin) ListAndWatch(_ *v1beta1.Empty, stream v1beta1.DevicePlugin_ListAndWatchServer) error {
	health := ""

	for {
		h := healthOf(p.device.Path)
		if h != health {
			health = h

			if err := stream.Send(&v1beta1.ListAndWatchResponse{Devices: advertisedDevices(p.device.Count, h)}); err != nil {
				return fmt.Errorf("sending device list: %w", err)
			}

			slog.Info("device health updated", "resource", p.device.Resource, "health", h)
		}

		time.Sleep(rescanInterval)
	}
}

func (p *rawPlugin) Allocate(ctx context.Context, req *v1beta1.AllocateRequest) (*v1beta1.AllocateResponse, error) {
	resp := &v1beta1.AllocateResponse{}

	for range req.ContainerRequests {
		mount := &v1beta1.Mount{
			HostPath:      p.device.Path,
			ContainerPath: p.device.Path,
			ReadOnly:      false,
		}
		// Devices (not Mounts) is what kubelet turns into the devices-cgroup
		// allowlist on containerd; Mounts alone only bind-mount the node.
		devSpec := &v1beta1.DeviceSpec{
			HostPath:      p.device.Path,
			ContainerPath: p.device.Path,
			Permissions:   "rwm",
		}
		resp.ContainerResponses = append(resp.ContainerResponses,
			&v1beta1.ContainerAllocateResponse{
				Mounts:  []*v1beta1.Mount{mount},
				Devices: []*v1beta1.DeviceSpec{devSpec},
			})
	}

	return resp, nil
}

func (p *rawPlugin) GetPreferredAllocation(context.Context, *v1beta1.PreferredAllocationRequest) (*v1beta1.PreferredAllocationResponse, error) {
	return &v1beta1.PreferredAllocationResponse{}, nil
}

func (p *rawPlugin) PreStartContainer(context.Context, *v1beta1.PreStartContainerRequest) (*v1beta1.PreStartContainerResponse, error) {
	return &v1beta1.PreStartContainerResponse{}, nil
}

// serveDevice keeps one device registered until ctx is cancelled.
func serveDevice(ctx context.Context, device Device) error {
	for {
		removed, err := serveOnce(ctx, device)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return err
		}

		if !removed {
			return nil
		}

		slog.Warn("device plugin socket removed by kubelet, re-registering", "resource", device.Resource)
	}
}

// serveOnce registers and serves the plugin. It reports removed=true when
// kubelet deleted the socket, which needs a fresh registration.
func serveOnce(ctx context.Context, device Device) (bool, error) {
	sockPath := socketPath(device.Resource)
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("removing stale socket %s: %w", sockPath, err)
	}

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return false, fmt.Errorf("listening on %s: %w", sockPath, err)
	}
	defer func() { _ = os.Remove(sockPath) }()

	server := grpc.NewServer()

	v1beta1.RegisterDevicePluginServer(server, &rawPlugin{device: device})
	defer server.Stop()

	// Serve first: kubelet dials the plugin socket as part of handling the
	// Register RPC, so the gRPC server must accept before registration.
	serveErr := make(chan error, 1)

	go func() {
		if err := server.Serve(listener); err != nil {
			serveErr <- err
		}
	}()

	if err := registerWithKubelet(ctx, device); err != nil {
		return false, err
	}

	slog.Info("device plugin registered", "resource", device.Resource, "path", device.Path)

	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()

	removed := make(chan struct{})

	go func() {
		ticker := time.NewTicker(socketWatchInterval)
		defer ticker.Stop()

		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(sockPath); err != nil {
					close(removed)

					return
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		return false, nil
	case err := <-serveErr:
		return false, fmt.Errorf("serving %s: %w", device.Resource, err)
	case <-removed:
		return true, nil
	}
}

// probeDevices fails unless every configured plugin answers on its socket.
func probeDevices(ctx context.Context, configPath string) error {
	devices, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	var errs []error

	for _, device := range devices {
		if err := probeDevice(ctx, device); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func probeDevice(ctx context.Context, device Device) error {
	conn, err := grpc.NewClient("unix://"+socketPath(device.Resource),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("probing %s: %w", device.Resource, err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if _, err := v1beta1.NewDevicePluginClient(conn).GetDevicePluginOptions(ctx, &v1beta1.Empty{}); err != nil {
		return fmt.Errorf("probing %s: %w", device.Resource, err)
	}

	return nil
}

func registerWithKubelet(ctx context.Context, device Device) error {
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}

		conn, err := grpc.NewClient("unix://"+kubeletSocket,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("dialing kubelet: %w", err)
		}

		client := v1beta1.NewRegistrationClient(conn)
		req := &v1beta1.RegisterRequest{
			Version:      v1beta1.Version,
			Endpoint:     socketName(device.Resource),
			ResourceName: device.Resource,
		}
		rpcCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err = client.Register(rpcCtx, req)

		cancel()

		_ = conn.Close()

		if err == nil {
			return nil
		}

		slog.Warn("kubelet registration failed, retrying", "resource", device.Resource, "error", err)
	}
}
