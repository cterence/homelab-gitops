package main

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

func runPlugins(ctx context.Context, devices []Device) error {
	g, ctx := errgroup.WithContext(ctx)
	for _, d := range devices {
		g.Go(func() error {
			if err := serveDevice(ctx, d); err != nil {
				return fmt.Errorf("serving %s: %w", d.Resource, err)
			}

			return nil
		})
	}

	return g.Wait()
}
