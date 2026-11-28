package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	cli "github.com/urfave/cli/v3"
)

const defaultConfigPath = "config.yaml"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := &cli.Command{
		Name:  "device-plugin",
		Usage: "expose host device nodes as extended resources",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Value: defaultConfigPath, Usage: "path to the config file"},
			&cli.BoolFlag{Name: "debug", Value: false, Usage: "debug logging"},
		},
		Commands: []*cli.Command{
			{
				Name:  "probe",
				Usage: "exit non-zero unless every configured plugin answers on its socket",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "config", Value: defaultConfigPath, Usage: "path to the config file"},
				},
				Action: func(ctx context.Context, c *cli.Command) error {
					return probeDevices(ctx, c.String("config"))
				},
			},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return serve(ctx, c.String("config"), c.Bool("debug"))
		},
	}

	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "device-plugin: %v\n", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, configPath string, debug bool) error {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	devices, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	return runPlugins(ctx, devices)
}
