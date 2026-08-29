package traefikjail

import (
	"context"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Package-level singletons shared across all plugin instances.
// Traefik creates one plugin instance per router, so without singletons
// each instance would have its own Jailer (fragmenting error counts) and
// its own stats goroutine (flooding logs).
var (
	singletonJailer *Jailer
	singletonStats  *requestStats
	jailerOnce      sync.Once
	statsOnce       sync.Once
)

// Config holds the plugin configuration passed by Traefik.
// Duration fields are in seconds (int) because Yaegi's mapstructure decoder
// cannot parse time.Duration strings like "1m" or "60s".
type Config struct {
	Threshold     int      `json:"threshold,omitempty"`
	Window        int      `json:"window,omitempty"`        // seconds
	BaseBan       int      `json:"baseBan,omitempty"`       // seconds
	MaxBan        int      `json:"maxBan,omitempty"`        // seconds
	ResetAfter    int      `json:"resetAfter,omitempty"`    // seconds
	AllowList     []string `json:"allowList,omitempty"`     // CIDRs or single IPs to skip
	StatsInterval int      `json:"statsInterval,omitempty"` // seconds, 0 = disabled
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
	next      http.Handler
	name      string
	jailer    *Jailer
	allowList []string
	stats     *requestStats
}

// requestStats tracks plugin processing time with atomic counters.
type requestStats struct {
	count   atomic.Int64
	totalNs atomic.Int64
	minNs   atomic.Int64
	maxNs   atomic.Int64
}

func newRequestStats() *requestStats {
	s := &requestStats{}
	s.minNs.Store(1 << 62)

	return s
}

func (s *requestStats) record(d time.Duration) {
	ns := d.Nanoseconds()

	s.count.Add(1)
	s.totalNs.Add(ns)

	for {
		cur := s.minNs.Load()
		if ns >= cur {
			break
		}

		if s.minNs.CompareAndSwap(cur, ns) {
			break
		}
	}

	for {
		cur := s.maxNs.Load()
		if ns <= cur {
			break
		}

		if s.maxNs.CompareAndSwap(cur, ns) {
			break
		}
	}
}

func (s *requestStats) logAndReset() {
	count := s.count.Swap(0)
	if count == 0 {
		return
	}

	total := s.totalNs.Swap(0)
	minN := s.minNs.Swap(1 << 62)
	maxN := s.maxNs.Swap(0)

	avg := total / count

	log.Printf("traefik-jail: stats count=%d avg=%s min=%s max=%s",
		count,
		time.Duration(avg),
		time.Duration(minN),
		time.Duration(maxN),
	)
}

func (s *requestStats) startLogger(interval time.Duration) {
	ticker := time.NewTicker(interval)

	go func() {
		for range ticker.C {
			s.logAndReset()
		}
	}()
}

// New creates a new plugin instance.
// The Jailer and stats collector are package-level singletons so all
// instances share state — Traefik creates one instance per router.
func New(_ context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	jailerOnce.Do(func() {
		singletonJailer = NewJailer(
			config.Threshold,
			time.Duration(config.Window)*time.Second,
			time.Duration(config.BaseBan)*time.Second,
			time.Duration(config.MaxBan)*time.Second,
			time.Duration(config.ResetAfter)*time.Second,
		)
	})

	p := &JailPlugin{
		next:      next,
		name:      name,
		jailer:    singletonJailer,
		allowList: config.AllowList,
	}

	if config.StatsInterval > 0 {
		statsOnce.Do(func() {
			singletonStats = newRequestStats()
			singletonStats.startLogger(time.Duration(config.StatsInterval) * time.Second)
		})

		p.stats = singletonStats
	}

	return p, nil
}

// ServeHTTP implements http.Handler.
func (p *JailPlugin) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	var start time.Time
	if p.stats != nil {
		start = time.Now()
	}

	// Allowlist bypass — earliest possible return, before any allocation.
	if len(p.allowList) > 0 {
		ip := extractIPFromRequest(req)
		if isAllowed(ip, p.allowList) {
			if p.stats != nil {
				p.stats.record(time.Since(start))
			}

			p.next.ServeHTTP(rw, req)

			return
		}

		p.serveJailed(rw, req, ip, start)

		return
	}

	ip := extractIPFromRequest(req)
	p.serveJailed(rw, req, ip, start)
}

func (p *JailPlugin) serveJailed(rw http.ResponseWriter, req *http.Request, ip string, start time.Time) {
	if p.jailer.IsJailed(ip, time.Now()) {
		if p.stats != nil {
			p.stats.record(time.Since(start))
		}

		rw.WriteHeader(http.StatusForbidden)
		_, _ = rw.Write([]byte("403 Forbidden\n"))

		return
	}

	recorder := &statusRecorder{ResponseWriter: rw, status: http.StatusOK}
	p.next.ServeHTTP(recorder, req)

	if recorder.status >= 400 && recorder.status < 500 {
		p.jailer.RecordError(ip, time.Now())
	}

	if p.stats != nil {
		p.stats.record(time.Since(start))
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
