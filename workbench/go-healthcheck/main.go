package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hellofresh/health-go/v5"
	cli "github.com/urfave/cli/v3"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := &cli.Command{
		Name:  "go-healthcheck",
		Usage: "serve health checks for external targets",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Value: "config.yaml", Usage: "path to the config file"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			serve(ctx, c.String("config"))

			return nil
		},
	}

	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "go-healthcheck: %v\n", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, configPath string) {
	config := &Config{}

	err := config.Load(configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	h, err := health.New(health.WithComponent(health.Component{
		Name:    config.Name,
		Version: config.Version,
	}))
	if err != nil {
		log.Fatalf("failed to create health check container: %v", err)
	}

	targetTypes := []struct {
		name      string
		endpoints []string
		new       func() Target
	}{
		{"http", config.Targets.HTTP, func() Target { return &HTTP{} }},
		{"postgresql", config.Targets.PostgreSQL, func() Target { return &PostgreSQL{} }},
	}

	for _, tt := range targetTypes {
		for _, endpoint := range tt.endpoints {
			t := tt.new()
			if err := Register(t, endpoint, h, config); err != nil {
				log.Fatalf("failed to register %s target: %v", tt.name, err)
			}

			fmt.Printf("Registered %s target: %s\n", tt.name, t)
		}
	}

	r := newRouter(h)

	srv := &http.Server{Addr: ":" + config.Port, Handler: r}

	go func() {
		fmt.Printf("Listening on port %s\n", config.Port)

		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("failed to listen on port %s: %v", config.Port, err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("failed to shutdown gracefully: %v", err)
	}
}
