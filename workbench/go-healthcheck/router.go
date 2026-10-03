package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/hellofresh/health-go/v5"
)

func newRouter(h *health.Health) http.Handler {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(slog.String("service", "go-healthcheck"))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		type CheckWithFailures struct {
			health.Check
			Failures map[string]string `json:"failures"` // Overrides Check.Failures which is normally omitempty
		}

		cwf := CheckWithFailures{}

		c := h.Measure(r.Context())

		cwf.Status = c.Status
		cwf.Timestamp = c.Timestamp
		cwf.Failures = c.Failures
		cwf.Component = c.Component

		w.Header().Set("Content-Type", "application/json")

		data, err := json.Marshal(cwf)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		code := http.StatusOK
		if c.Status == "Unavailable" {
			code = http.StatusServiceUnavailable
		}

		logger.Info(string(data))
		w.WriteHeader(code)

		_, err = w.Write(data)
		if err != nil {
			// A failing client connection must not take the whole service down.
			log.Printf("failed to write response: %v", err)
		}
	})

	return http.TimeoutHandler(mux, 10*time.Second, "request timed out")
}
