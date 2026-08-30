package traefikjail

import (
	"context"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
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
	ErrorCodes    string   `json:"errorCodes,omitempty"`    // comma-separated codes/ranges, e.g. "400-499" or "404,403"
	ExcludeURLs   []string `json:"excludeURLs,omitempty"`   // glob patterns skipping jail entirely; path-only if starting with /, else host+path
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
	next       http.Handler
	name       string
	jailer     *Jailer
	allowList  []string
	stats      *requestStats
	errorCodes codeMatcher
	excludeURLs []string
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

// codeMatcher checks if an HTTP status code matches any of the configured codes or ranges.
type codeMatcher struct {
	codes  map[int]struct{}
	ranges [][2]int
}

func parseErrorCodes(s string) codeMatcher {
	m := codeMatcher{codes: make(map[int]struct{})}
	if s == "" {
		return m
	}

	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)

		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))

			hi, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil {
				continue
			}

			m.ranges = append(m.ranges, [2]int{lo, hi})
		} else {
			code, err := strconv.Atoi(part)
			if err != nil {
				continue
			}

			m.codes[code] = struct{}{}
		}
	}

	return m
}

func (m codeMatcher) matches(status int) bool {
	if _, ok := m.codes[status]; ok {
		return true
	}

	for _, r := range m.ranges {
		if status >= r[0] && status <= r[1] {
			return true
		}
	}

	return false
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
		next:        next,
		name:        name,
		jailer:      singletonJailer,
		allowList:   config.AllowList,
		errorCodes:  parseErrorCodes(config.ErrorCodes),
		excludeURLs: config.ExcludeURLs,
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
	// URL exclusion bypass — earliest possible return, before any allocation.
	// Patterns starting with / match against the path only; all others match
	// against the full host+path so exclusions can be scoped to a specific vhost.
	if len(p.excludeURLs) > 0 && p.isExcluded(req) {
		p.next.ServeHTTP(rw, req)

		return
	}

	// Allowlist bypass — earliest possible return, before any allocation.
	if len(p.allowList) > 0 {
		ip := extractIPFromRequest(req)
		if isAllowed(ip, p.allowList) {
			p.serveJailed(rw, req, ip, true)

			return
		}

		p.serveJailed(rw, req, ip, false)

		return
	}

	ip := extractIPFromRequest(req)
	p.serveJailed(rw, req, ip, false)
}

// isExcluded reports whether the request URL matches any excludeURLs glob pattern.
func (p *JailPlugin) isExcluded(req *http.Request) bool {
	for _, pattern := range p.excludeURLs {
		var target string
		if strings.HasPrefix(pattern, "/") {
			target = req.URL.Path
		} else {
			target = req.Host + req.URL.Path
		}

		matched, err := path.Match(pattern, target)
		if err == nil && matched {
			return true
		}
	}

	return false
}

func (p *JailPlugin) serveJailed(rw http.ResponseWriter, req *http.Request, ip string, allowed bool) {
	var start time.Time
	if p.stats != nil {
		start = time.Now()
	}

	if p.jailer.IsJailed(ip, time.Now()) {
		if p.stats != nil {
			p.stats.record(time.Since(start))
		}

		rw.WriteHeader(http.StatusForbidden)
		_, _ = rw.Write([]byte("403 Forbidden\n"))

		return
	}

	if allowed {
		p.next.ServeHTTP(rw, req)

		return
	}

	recorder := &statusRecorder{ResponseWriter: rw, status: http.StatusOK}

	// Stop timing before the upstream call to measure only plugin overhead.
	if p.stats != nil {
		p.stats.record(time.Since(start))
	}

	p.next.ServeHTTP(recorder, req)

	if p.errorCodes.matches(recorder.status) {
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
