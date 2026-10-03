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
)

func main() {
	config := &Config{}

	err := config.Load()
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
