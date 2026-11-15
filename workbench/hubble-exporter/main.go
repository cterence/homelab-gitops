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
		Name:  "hubble-exporter",
		Usage: "record novel flow pairs from a hubble relay",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "server", Value: "hubble-relay.kube-system.svc.cluster.local:80", Usage: "hubble relay address"},
			&cli.StringFlag{Name: "valkey", Value: "hubble-exporter-valkey:6379", Usage: "valkey address holding the seen-record set"},
			&cli.StringFlag{Name: "pod-cidr", Value: "10.244.0.0/16", Usage: "cluster pod CIDR, used to classify destinations"},
			&cli.StringFlag{Name: "node-cidr", Value: "192.168.1.0/24", Usage: "node LAN CIDR, used to classify destinations"},
			&cli.StringFlag{Name: "state", Value: "/data/seen.json", Usage: "legacy seen.json to migrate into valkey on startup"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return run(ctx, c.String("server"), c.String("valkey"), c.String("pod-cidr"), c.String("node-cidr"), c.String("state"))
		},
	}

	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "hubble-exporter: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, server, valkeyAddr, podCIDR, nodeCIDR, statePath string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	store := NewStore(valkeyAddr)
	defer func() {
		if err := store.Close(); err != nil {
			logger.Warn("closing valkey connection", "err", err)
		}
	}()

	if err := store.Wait(ctx, logger); err != nil {
		return fmt.Errorf("waiting for valkey: %w", err)
	}

	if n, err := store.Migrate(ctx, statePath); err != nil {
		return err
	} else if n > 0 {
		logger.Info("legacy state migrated", "records", n, "path", statePath)
	}

	if size, err := store.Size(ctx); err != nil {
		logger.Warn("reading valkey size", "err", err)
	} else {
		logger.Info("valkey ready", "records", size)
	}

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
			// follow from now; a fresh stream on reconnect re-emits pairs
			// valkey drops as already recorded
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

	broke := false

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errs:
			logger.Warn("stream error, reconnecting", "err", err)
		case resp, ok := <-events:
			if !ok {
				return nil
			}

			fl := resp.GetFlow()
			at := fl.GetTime().AsTime()

			if p, ok := FlowPair(fl, podCIDR, nodeCIDR); ok && store.novelOrWarn(ctx, logger, pairKey(p), at, &broke) {
				logger.Info("pair", p.LogAttrs()...)
			}

			if ns, qname, ips, ok := DNSRecord(fl); ok && store.novelOrWarn(ctx, logger, dnsKey(ns, qname), at, &broke) {
				logger.Info("dns", "src_ns", ns, "qname", qname, "ips", ips)
			}
		}
	}
}

// novelOrWarn reports novelty, treating store failures as novel so pairs
// are never silently dropped, and logs one line per outage.
func (s *Store) novelOrWarn(ctx context.Context, logger *slog.Logger, key string, at time.Time, broke *bool) bool {
	novel, err := s.Novel(ctx, key, at)
	if err == nil {
		*broke = false
		return novel
	}

	if !*broke {
		logger.Warn("valkey error, treating records as novel until it recovers", "err", err)

		*broke = true
	}

	return true
}
