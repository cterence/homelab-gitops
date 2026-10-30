package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var port string

	cmd := &cli.Command{
		Name:    "slackmoji",
		Usage:   "Shrink GIFs to Slack emoji limits (128 KB, 128 px)",
		Version: "dev",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "port",
				Usage:       "HTTP listen port",
				Value:       "3000",
				Sources:     cli.NewValueSourceChain(cli.EnvVar("PORT")),
				Destination: &port,
			},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return serve(ctx, port)
		},
	}

	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "slackmoji:", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, port string) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           newRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	slog.Info("listening", "addr", srv.Addr)

	select {
	case err := <-errCh:
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return srv.Shutdown(shutdownCtx)
	}
}
