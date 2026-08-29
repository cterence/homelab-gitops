package traefikjail

import (
	"context"
	"net/http"
	"time"
)

// Config holds the plugin configuration passed by Traefik.
// Duration fields are in seconds (int) because Yaegi's mapstructure decoder
// cannot parse time.Duration strings like "1m" or "60s".
type Config struct {
	Threshold  int `json:"threshold,omitempty"`
	Window     int `json:"window,omitempty"`     // seconds
	BaseBan    int `json:"baseBan,omitempty"`     // seconds
	MaxBan     int `json:"maxBan,omitempty"`     // seconds
	ResetAfter int `json:"resetAfter,omitempty"` // seconds
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		Threshold:  10,
		Window:     60,
		BaseBan:    60,
		MaxBan:     3600,
		ResetAfter: 3600,
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
		jailer: NewJailer(
			config.Threshold,
			time.Duration(config.Window)*time.Second,
			time.Duration(config.BaseBan)*time.Second,
			time.Duration(config.MaxBan)*time.Second,
			time.Duration(config.ResetAfter)*time.Second,
		),
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
