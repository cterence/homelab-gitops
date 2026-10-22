// agent-relay is a generic webhook→Mistral-agent relay. Each configured route
// receives a JSON payload, renders a prompt template, and starts a
// conversation with a Mistral agent. Requests are answered 202 immediately;
// the conversation runs in the background.
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

	cli "github.com/urfave/cli/v3"
)

const (
	listenAddr    = ":8080"
	defaultConfig = "/config/config.yaml"
	configPathEnv = "AGENT_RELAY_CONFIG"
)

func run(ctx context.Context, args []string) error {
	var configPath string

	cmd := &cli.Command{
		Name:  "agent-relay",
		Usage: "generic webhook to Mistral-agent relay",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config",
				Value:       defaultConfig,
				Sources:     cli.NewValueSourceChain(cli.EnvVar(configPathEnv)),
				Destination: &configPath,
				Usage:       "path to the routes config file",
			},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			return serve(ctx, configPath)
		},
	}

	return cmd.Run(ctx, args)
}

// serve is the command body: everything after flag parsing.
func serve(ctx context.Context, configPath string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		return errors.New("MISTRAL_API_KEY environment variable must be set")
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config %s: %w", configPath, err)
	}

	rl := &relay{
		logger:  logger,
		apiKey:  apiKey,
		invoker: startConversation,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	for _, r := range cfg.Routes {
		logger.Info("route loaded",
			"path", r.Path,
			"agent_id", r.AgentID,
			"conversation_name", r.ConversationName,
			"dedup_key", r.DedupKey,
			"dedup_window", r.window.String(),
		)
		mux.Handle("POST "+r.Path, rl.handler(r))
	}

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           requestLog(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	logger.Info("listening", "addr", listenAddr, "routes", len(cfg.Routes))

	select {
	case err := <-errCh:
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		return srv.Shutdown(shutdownCtx)
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
