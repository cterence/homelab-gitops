package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// newProxy builds a reverse proxy to upstream that rewrites GLM reasoning
// responses on chat completions: typed content arrays are flattened into a
// plain string content with the reasoning moved to reasoning_content, on
// both the non-streaming and SSE streaming paths. Everything else passes
// through untouched.
func newProxy(upstream string) (http.Handler, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parsing upstream URL %s: %w", upstream, err)
	}

	return &httputil.ReverseProxy{
		// Flush every SSE chunk as soon as it arrives.
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			// A compressed body would hide the JSON this adapter rewrites.
			r.Out.Header.Set("Accept-Encoding", "identity")
		},
		ModifyResponse: modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("proxying request", "err", err, "path", r.URL.Path)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}, nil
}

func modifyResponse(resp *http.Response) error {
	if resp.Request == nil || !strings.HasSuffix(resp.Request.URL.Path, "/chat/completions") {
		return nil
	}

	switch {
	case strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"):
		resp.Body = newSSETransformReader(resp.Body)
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
	case strings.Contains(resp.Header.Get("Content-Type"), "application/json"):
		body, err := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()

		if err != nil {
			return fmt.Errorf("reading upstream response: %w", err)
		}

		if closeErr != nil {
			return fmt.Errorf("closing upstream response body: %w", closeErr)
		}

		out := transformChatPayload(body)
		resp.Body = io.NopCloser(bytes.NewReader(out))
		resp.ContentLength = int64(len(out))
		resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	}

	return nil
}

// sseTransformReader rewrites an upstream SSE body line by line.
type sseTransformReader struct {
	sc     *bufio.Scanner
	buf    bytes.Buffer
	closer io.Closer
	done   bool
}

func newSSETransformReader(body io.ReadCloser) *sseTransformReader {
	sc := bufio.NewScanner(body)
	// LLM chunks can exceed the default 64KiB scanner limit.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	return &sseTransformReader{sc: sc, closer: body}
}

func (r *sseTransformReader) Read(p []byte) (int, error) {
	for r.buf.Len() == 0 && !r.done {
		if !r.sc.Scan() {
			r.done = true
			if err := r.sc.Err(); err != nil {
				return 0, fmt.Errorf("reading upstream SSE: %w", err)
			}

			return 0, io.EOF
		}
		// The scanner reuses its buffer, so copy before transforming.
		line := append([]byte(nil), r.sc.Bytes()...)
		r.buf.Write(transformSSELine(line))
		r.buf.WriteByte('\n')
	}

	return r.buf.Read(p)
}

func (r *sseTransformReader) Close() error {
	return r.closer.Close()
}

// requestMeta extracts request metadata from a chat completions body.
// Message content is never logged; only the model name and stream flag.
func requestMeta(body []byte) (model string, stream bool) {
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", false
	}

	return req.Model, req.Stream
}

// statusRecorder captures the response status code for logging while
// passing Flush through so SSE streaming keeps working.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withRequestLog logs one line per proxied request: method, path, status,
// duration, and the requested model and stream flag. The request body is
// buffered only to extract metadata; its content is never logged.
func withRequestLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		var (
			model  string
			stream bool
		)

		if r.Body != nil && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/completions") {
			body, err := io.ReadAll(r.Body)

			closeErr := r.Body.Close()
			if err == nil && closeErr == nil {
				r.Body = io.NopCloser(bytes.NewReader(body))
				model, stream = requestMeta(body)
			}
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur", time.Since(start).Round(time.Millisecond).String(),
			"model", model,
			"stream", stream,
		)
	})
}
