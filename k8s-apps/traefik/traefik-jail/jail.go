package traefikjail

import (
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ipState tracks per-IP error history and ban status.
type ipState struct {
	mu            sync.Mutex
	errorCount    int       // 4xx errors within the current window
	windowStart   time.Time // start of the current counting window
	banCount      int       // total times this IP has been banned (for backoff)
	banUntil      time.Time // when the current ban expires
	lastBanExpiry time.Time // when the most recent ban expired (for reset check)
}

// Jailer manages per-IP error tracking and banning with exponential backoff.
type Jailer struct {
	mu         sync.Mutex
	ips        map[string]*ipState
	threshold  int           // errors within window to trigger a ban
	window     time.Duration // sliding window for counting errors
	baseBan    time.Duration // initial ban duration
	maxBan     time.Duration // cap on ban duration
	resetAfter time.Duration // reset ban counter after this long without errors
}

// NewJailer creates a Jailer with the given configuration.
func NewJailer(threshold int, window, baseBan, maxBan, resetAfter time.Duration) *Jailer {
	return &Jailer{
		ips:        make(map[string]*ipState),
		threshold:  threshold,
		window:     window,
		baseBan:    baseBan,
		maxBan:     maxBan,
		resetAfter: resetAfter,
	}
}

// IsJailed reports whether the IP is currently banned.
func (j *Jailer) IsJailed(ip string, now time.Time) bool {
	j.mu.Lock()
	defer j.mu.Unlock()

	s, ok := j.ips[ip]
	if !ok {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if now.Before(s.banUntil) {
		return true
	}

	// Ban has expired — check if we should reset the ban counter
	if !s.banUntil.IsZero() && now.Sub(s.banUntil) >= j.resetAfter {
		s.banCount = 0
		s.banUntil = time.Time{}
	}

	return false
}

// RecordError increments the error count for the given IP and jails it if the threshold is exceeded.
// Returns the ban duration if the IP was newly jailed, or zero if not.
func (j *Jailer) RecordError(ip string, now time.Time) time.Duration {
	j.mu.Lock()
	defer j.mu.Unlock()

	s, ok := j.ips[ip]
	if !ok {
		s = &ipState{}
		j.ips[ip] = s
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// If currently banned, don't count errors (they're blocked at the door)
	if now.Before(s.banUntil) {
		return 0
	}

	// Reset window if it has elapsed
	if now.Sub(s.windowStart) >= j.window {
		s.errorCount = 0
		s.windowStart = now
	}

	if s.windowStart.IsZero() {
		s.windowStart = now
	}

	s.errorCount++

	if s.errorCount < j.threshold {
		return 0
	}

	// Threshold exceeded — jail with exponential backoff
	s.banCount++
	banDuration := j.banDuration(s.banCount)
	s.banUntil = now.Add(banDuration)
	s.lastBanExpiry = s.banUntil
	s.errorCount = 0

	log.Printf("traefik-jail: banned ip=%s banCount=%d duration=%s", ip, s.banCount, banDuration)

	return banDuration
}

// banDuration calculates the ban duration with exponential backoff: baseBan * 2^(banCount-1),
// capped at maxBan.
func (j *Jailer) banDuration(banCount int) time.Duration {
	d := j.baseBan
	for i := 1; i < banCount; i++ {
		d *= 2
		if d > j.maxBan {
			return j.maxBan
		}
	}

	if d > j.maxBan {
		return j.maxBan
	}

	return d
}

// extractIPFromRequest returns the client IP from X-Forwarded-For or X-Real-IP headers,
// falling back to the remote address. Reads headers directly without allocation.
func extractIPFromRequest(req *http.Request) string {
	xff := req.Header.Get("X-Forwarded-For")
	if xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}

		return xff
	}

	xri := req.Header.Get("X-Real-Ip")
	if xri != "" {
		return xri
	}

	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}

	return host
}

// extractIP returns the client IP from a header map, for testing.
func extractIP(headers map[string]string, remoteAddr string) string {
	if xff, ok := headers["X-Forwarded-For"]; ok && xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}

		return xff
	}

	if xri, ok := headers["X-Real-Ip"]; ok && xri != "" {
		return xri
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}

	return host
}

// isAllowed checks if an IP matches any CIDR or single IP in the allowList.
func isAllowed(ipStr string, allowList []string) bool {
	if len(allowList) == 0 {
		return false
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	for _, entry := range allowList {
		if strings.Contains(entry, "/") {
			_, cidr, err := net.ParseCIDR(entry)
			if err != nil {
				continue
			}

			if cidr.Contains(ip) {
				return true
			}
		} else {
			allowed := net.ParseIP(entry)
			if allowed != nil && allowed.Equal(ip) {
				return true
			}
		}
	}

	return false
}
