package main

import (
	"fmt"
	"log"
	"net/http"

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

	for _, endpoint := range config.Targets.HTTP {
		t := &HTTP{}
		if err := Register(t, endpoint, h, config); err != nil {
			log.Fatalf("failed to register http target: %v", err)
		}
	}

	for _, endpoint := range config.Targets.PostgreSQL {
		t := &PostgreSQL{}
		if err := Register(t, endpoint, h, config); err != nil {
			log.Fatalf("failed to register postgresql target: %v", err)
		}
	}

	for _, endpoint := range config.Targets.Redis {
		t := &Redis{}
		if err := Register(t, endpoint, h, config); err != nil {
			log.Fatalf("failed to register redis target: %v", err)
		}
	}

	r := newRouter(h)

	fmt.Printf("Listening on port %s\n", config.Port)

	err = http.ListenAndServe(":"+config.Port, r)
	if err != nil {
		log.Fatalf("failed to listen on port %s: %v", config.Port, err)
	}
}
