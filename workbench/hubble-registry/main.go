package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	cli "github.com/urfave/cli/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := &cli.Command{
		Name:  "hubble-registry",
		Usage: "record novel flow pairs from a hubble relay",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "server", Value: "hubble-relay.kube-system.svc.cluster.local:80", Usage: "hubble relay address"},
			&cli.StringFlag{Name: "pod-cidr", Value: "10.244.0.0/16", Usage: "cluster pod CIDR, used to classify destinations"},
			&cli.StringFlag{Name: "node-cidr", Value: "192.168.1.0/24", Usage: "node LAN CIDR, used to classify destinations"},
			&cli.StringFlag{Name: "state", Value: "/data/registry.json", Usage: "path of the persisted seen-pairs state"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return run(ctx, c.String("server"), c.String("pod-cidr"), c.String("node-cidr"), c.String("state"))
		},
	}

	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "hubble-registry: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, server, podCIDR, nodeCIDR, statePath string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	reg, err := LoadRegistry(statePath)
	if err != nil {
		return err
	}

	logger.Info("registry loaded", "pairs", len(reg.seen), "path", statePath)

	conn, err := grpc.NewClient(server, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dialing hubble relay: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			logger.Warn("closing relay connection", "err", err)
		}
	}()

	client := observerpb.NewObserverClient(conn)

	events := make(chan *observerpb.GetFlowsResponse)
	errs := make(chan error, 1)

	go func() {
		defer close(events)

		for {
			// follow from now; a fresh stream on reconnect re-emits pairs the
			// registry drops as already seen
			stream, err := client.GetFlows(ctx, &observerpb.GetFlowsRequest{
				Follow:    true,
				Whitelist: []*flowpb.FlowFilter{{SourceIp: []string{podCIDR}}},
			})
			if err != nil {
				errs <- fmt.Errorf("opening flow stream: %w", err)

				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}

				continue
			}

			for {
				resp, err := stream.Recv()
				if err != nil {
					errs <- fmt.Errorf("receiving flows: %w", err)
					break
				}

				select {
				case events <- resp:
				case <-ctx.Done():
					return
				}
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return save(reg, logger)
		case err := <-errs:
			logger.Warn("stream error, reconnecting", "err", err)
		case <-ticker.C:
			if err := reg.Save(); err != nil {
				logger.Error("saving registry", "err", err)
			}
		case resp, ok := <-events:
			if !ok {
				return save(reg, logger)
			}

			fl := resp.GetFlow()
			if p, ok := FlowPair(fl, podCIDR, nodeCIDR); ok && reg.Add(p.key(), fl.GetTime().AsTime()) {
				logger.Info("pair", p.LogAttrs()...)
			}

			if ns, qname, ips, ok := DNSRecord(fl); ok {
				if reg.Add("dns\x00"+ns+"\x00"+qname, fl.GetTime().AsTime()) {
					logger.Info("dns", "src_ns", ns, "qname", qname, "ips", ips)
				}
			}
		}
	}
}

func save(reg *Registry, logger *slog.Logger) error {
	if err := reg.Save(); err != nil {
		logger.Error("saving registry on shutdown", "err", err)
		return err
	}

	logger.Info("registry saved", "pairs", len(reg.seen))

	return nil
}
