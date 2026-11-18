package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const rescanInterval = 15 * time.Second

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

			dev := v1beta1.Device{ID: "0", Health: h}
			if err := stream.Send(&v1beta1.ListAndWatchResponse{Devices: []*v1beta1.Device{&dev}}); err != nil {
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

// serveDevice runs the plugin for one device until ctx is cancelled.
func serveDevice(ctx context.Context, device Device) error {
	sockPath := v1beta1.DevicePluginPath + socketName(device.Resource)
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale socket %s: %w", sockPath, err)
	}

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", sockPath, err)
	}
	defer func() { _ = os.Remove(sockPath) }()

	server := grpc.NewServer()
	v1beta1.RegisterDevicePluginServer(server, &rawPlugin{device: device})

	go func() {
		<-ctx.Done()
		server.Stop()
	}()

	// Serve first: kubelet dials the plugin socket as part of handling the
	// Register RPC, so the gRPC server must accept before registration.
	serveErr := make(chan error, 1)

	go func() {
		if err := server.Serve(listener); err != nil {
			serveErr <- err
		}
	}()

	if err := registerWithKubelet(ctx, device); err != nil {
		server.Stop()
		return err
	}

	slog.Info("device plugin registered", "resource", device.Resource, "path", device.Path)

	select {
	case <-ctx.Done():
		server.Stop()
		return nil
	case err := <-serveErr:
		return fmt.Errorf("serving %s: %w", device.Resource, err)
	}
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

		conn, err := grpc.NewClient("unix://"+v1beta1.KubeletSocket,
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
