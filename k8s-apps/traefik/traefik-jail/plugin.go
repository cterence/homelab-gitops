package traefikjail

import (
	"context"
	"net/http"
	"time"
)

// Config holds the plugin configuration passed by Traefik.
type Config struct {
	Threshold  int           `json:"threshold,omitempty"`
	Window     time.Duration `json:"window,omitempty"`
	BaseBan    time.Duration `json:"baseBan,omitempty"`
	MaxBan     time.Duration `json:"maxBan,omitempty"`
	ResetAfter time.Duration `json:"resetAfter,omitempty"`
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		Threshold:  10,
		Window:     60 * time.Second,
		BaseBan:    1 * time.Minute,
		MaxBan:     1 * time.Hour,
		ResetAfter: 1 * time.Hour,
	}
}

// JailPlugin is the Traefik middleware plugin.
type JailPlugin struct {
	next   http.Handler
	name   string
	jailer *Jailer
}

// New creates a new plugin instance.
func New(_ context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	return &JailPlugin{
		next:   next,
		name:   name,
		jailer: NewJailer(config.Threshold, config.Window, config.BaseBan, config.MaxBan, config.ResetAfter),
	}, nil
}

// ServeHTTP implements http.Handler.
func (p *JailPlugin) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	ip := extractIP(map[string]string{
		"X-Forwarded-For": req.Header.Get("X-Forwarded-For"),
		"X-Real-Ip":       req.Header.Get("X-Real-Ip"),
	}, req.RemoteAddr)

	if p.jailer.IsJailed(ip, time.Now()) {
		rw.WriteHeader(http.StatusForbidden)
		_, _ = rw.Write([]byte("403 Forbidden\n"))

		return
	}

	recorder := &statusRecorder{ResponseWriter: rw, status: http.StatusOK}
	p.next.ServeHTTP(recorder, req)

	if recorder.status >= 400 && recorder.status < 500 {
		p.jailer.RecordError(ip, time.Now())
	}
}

// statusRecorder wraps http.ResponseWriter to capture the response status code.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}

	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}

	return r.ResponseWriter.Write(b)
}
