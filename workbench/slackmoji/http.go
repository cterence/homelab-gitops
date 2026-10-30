package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
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

	mux.Handle("GET /", http.FileServerFS(sub))

	return mux
}

type convertResponse struct {
	Report *Report `json:"report"`
	Data   string  `json:"data"`
}

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

	out, report, err := Convert(data, mode)
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

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
