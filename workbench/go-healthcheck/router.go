package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v3"
	"github.com/hellofresh/health-go/v5"
)

func newRouter(h *health.Health) *chi.Mux {
	r := chi.NewRouter()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: httplog.SchemaECS.ReplaceAttr,
	})).With(slog.String("service", "go-healthcheck"))

	r.Use(middleware.Heartbeat("/health"))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(time.Second * 10))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
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

	return r
}
