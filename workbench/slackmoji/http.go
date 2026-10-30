package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

//go:embed static
var staticFS embed.FS

func newRouter() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /convert", handleConvert)

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}

	mux.Handle("GET /", noCache(http.FileServerFS(sub)))

	return mux
}

// noCache keeps long-lived tabs from serving a stale UI after a redeploy:
// embedded files have no mod time, so the browser gets no validators.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

type convertResponse struct {
	Report *Report `json:"report"`
	Data   string  `json:"data"`
}

// conversion work is CPU- and memory-bound: a small semaphore makes
// concurrent requests queue instead of multiplying load, and a deadline
// bounds the ladder searches server-side (the client timeout only stops
// the browser waiting, not the work).
var convertSlots = make(chan struct{}, 2)

const convertDeadline = 30 * time.Second

func handleConvert(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("missing file field"))
		return
	}

	defer func() {
		_ = file.Close()
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("file exceeds 32MB"))
		return
	}

	mode := r.FormValue("mode")
	if mode != ModeRatio && mode != ModeSquare {
		writeError(w, http.StatusBadRequest, errors.New("mode must be ratio or square"))
		return
	}

	opts := Options{Mode: mode}

	n, err := formInt(r, "maxFrames", 0, 1<<30)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	opts.MaxFrames = n

	n, err = formInt(r, "maxSide", minSide, maxSide)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	opts.MaxSide = n

	n, err = formInt(r, "maxColors", colorFloor, 255)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	opts.MaxColors = n

	ctx, cancel := context.WithTimeout(r.Context(), convertDeadline)
	defer cancel()

	select {
	case convertSlots <- struct{}{}:
		defer func() { <-convertSlots }()
	case <-ctx.Done():
		writeError(w, http.StatusServiceUnavailable, errors.New("server busy, try again"))
		return
	}

	out, report, err := Convert(ctx, data, opts)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}

	slog.Info("converted",
		"format", report.Format,
		"passthrough", report.Passthrough,
		"inputBytes", report.InputSize,
		"outputBytes", report.OutputSize,
		"framesIn", report.FramesIn,
		"framesOut", report.FramesOut,
		"colors", report.Colors,
		"mode", mode,
	)

	mime := "image/gif"
	if report.Passthrough {
		mime = report.Format
	}

	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(convertResponse{
		Report: report,
		Data:   "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(out),
	})
}

// formInt reads an optional integer form field with bounds. An absent
// field is the zero value.
func formInt(r *http.Request, name string, min, max int) (int, error) {
	v := r.FormValue(name)
	if v == "" {
		return 0, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d", name, min, max)
	}

	return n, nil
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
