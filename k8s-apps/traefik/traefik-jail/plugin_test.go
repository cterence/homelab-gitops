package traefikjail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPlugin_BannedIPGets403(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
	}

	// First request with a 404 response — threshold is 1, so this triggers a ban
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	rec := httptest.NewRecorder()

	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("first request: expected 404, got %d", rec.Code)
	}

	// Second request from the same IP — should be banned, get 403
	req2 := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req2.Header.Set("X-Forwarded-For", "1.2.3.4")

	rec2 := httptest.NewRecorder()

	plugin.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("banned request: expected 403, got %d", rec2.Code)
	}
}

func TestPlugin_NonBannedIPPassthrough(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(10, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	plugin.next = next

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "5.6.7.8")

	rec := httptest.NewRecorder()

	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 passthrough, got %d", rec.Code)
	}
}

func TestPlugin_5xxNotCounted(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
	}

	// 500 should not trigger a ban (only 4xx)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	plugin.next = next

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "9.9.9.9")

	rec := httptest.NewRecorder()

	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 passthrough, got %d", rec.Code)
	}

	// Should not be banned
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-Forwarded-For", "9.9.9.9")

	rec2 := httptest.NewRecorder()
	plugin.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 passthrough (not banned), got %d", rec2.Code)
	}
}

func TestPlugin_2xxNotCounted(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	plugin.next = next

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "8.8.8.8")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}

func TestPlugin_AllowedIPSkipsJail(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		allowList:  []string{"10.0.0.0/8"},
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
	}

	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	// Even with threshold=1 and 404 responses, allowed IP should never be jailed
	for range 20 {
		req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
		req.Header.Set("X-Forwarded-For", "10.0.0.5")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("allowed IP should pass through, got %d", rec.Code)
		}
	}

	if calls != 20 {
		t.Fatalf("expected 20 passthroughs, got %d", calls)
	}
}

func TestPlugin_VerifiedClientCertSkipsJail(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
	}

	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	// A verified client certificate (mTLS) bypasses the jail entirely —
	// even with threshold=1 and 404 responses.
	for range 20 {
		req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
		req.Header.Set("X-Forwarded-For", "92.92.127.221")
		req.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{}}}

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("verified client: expected 404 passthrough, got %d", rec.Code)
		}
	}

	if calls != 20 {
		t.Fatalf("expected 20 passthroughs, got %d", calls)
	}
}

func TestPlugin_UnverifiedClientCertStillJailed(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	// A presented-but-unverified certificate must NOT bypass the jail.
	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	req.Header.Set("X-Forwarded-For", "92.92.127.221")
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}

	rec := httptest.NewRecorder()
	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("first request: expected 404, got %d", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req2.Header.Set("X-Forwarded-For", "92.92.127.221")
	req2.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}

	rec2 := httptest.NewRecorder()
	plugin.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("unverified cert: expected 403 (banned), got %d", rec2.Code)
	}
}

func writePatternsFile(t *testing.T, dir, content string) string {
	t.Helper()

	path := filepath.Join(dir, "patterns.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing patterns file: %v", err)
	}

	return path
}

func TestPatternList_LoadAndMatch(t *testing.T) {
	dir := t.TempDir()

	path := writePatternsFile(t, dir, "# comment\n\n(?i)^/\\.env\n(?i)^/wp-config\\.php\n[invalid\n")

	p := newPatternList(path)

	p.mu.RLock()
	n := len(p.regexes)
	p.mu.RUnlock()

	if n != 2 {
		t.Errorf("loaded %d regexes, want 2 (comment, blank line and invalid regex skipped)", n)
	}

	tests := []struct {
		name    string
		urlPath string
		want    bool
	}{
		{"env match", "/.env", true},
		{"env nested match", "/.env.production", true},
		{"wp-config match", "/wp-config.php", true},
		{"no match", "/health", false},
		{"no match similar", "/environments", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.matches(tt.urlPath); got != tt.want {
				t.Errorf("matches(%q) = %v, want %v", tt.urlPath, got, tt.want)
			}
		})
	}
}

func TestPatternList_MissingFile(t *testing.T) {
	p := newPatternList(filepath.Join(t.TempDir(), "does-not-exist.txt"))

	if p.matches("/.env") {
		t.Fatal("no patterns loaded from a missing file, nothing can match")
	}
}

func TestPatternList_HotReload(t *testing.T) {
	dir := t.TempDir()

	path := writePatternsFile(t, dir, "(?i)^/\\.env\n")

	p := newPatternList(path)

	if !p.matches("/.env") {
		t.Fatal("expected initial pattern to match")
	}

	// Rewrite the file and bump its mtime into the future so the reload
	// sees the change (ConfigMap rollouts swap in a newer file).
	if err := os.WriteFile(path, []byte("(?i)^/healthcheck\\.txt$\n"), 0o600); err != nil {
		t.Fatalf("rewriting patterns file: %v", err)
	}

	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("bumping mtime: %v", err)
	}

	p.reload()

	if p.matches("/.env") {
		t.Error("old pattern should be gone after reload")
	}

	if !p.matches("/healthcheck.txt") {
		t.Error("new pattern should match after reload")
	}
}

func TestPlugin_PatternMatchOn200Counts(t *testing.T) {
	dir := t.TempDir()

	path := writePatternsFile(t, dir, "(?i)^/\\.env\n")

	plugin := &JailPlugin{
		jailer:        NewJailer(10, 60*time.Second, 60*time.Second, time.Hour, time.Hour),
		stats:         newRequestStats(),
		errorCodes:    parseErrorCodes("400-499"),
		patterns:      newPatternList(path),
		patternWeight: 5,
	}

	// Backend answers 200: pattern hits must still count (5 each)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	plugin.next = next

	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/.env", nil)
		req.Header.Set("X-Forwarded-For", "130.12.180.117")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("probe request: expected 200, got %d", rec.Code)
		}
	}

	// 2 * 5 = 10 >= threshold: the next request is banned
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("X-Forwarded-For", "130.12.180.117")

	rec := httptest.NewRecorder()
	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 after 2 pattern hits on 200s, got %d", rec.Code)
	}
}

func TestPlugin_PatternMatchOn404CountsWeightNotOne(t *testing.T) {
	dir := t.TempDir()

	path := writePatternsFile(t, dir, "(?i)^/\\.env\n")

	plugin := &JailPlugin{
		jailer:        NewJailer(6, 60*time.Second, 60*time.Second, time.Hour, time.Hour),
		stats:         newRequestStats(),
		errorCodes:    parseErrorCodes("400-499"),
		patterns:      newPatternList(path),
		patternWeight: 5,
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	// Two matching 404s: 2 * 5 = 10 >= threshold 6. With plain error
	// counting the tally would be 2 and no ban would trigger.
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/.env", nil)
		req.Header.Set("X-Forwarded-For", "45.148.10.9")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("probe request: expected 404, got %d", rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("X-Forwarded-For", "45.148.10.9")

	rec := httptest.NewRecorder()
	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (pattern weight applied, not 1), got %d", rec.Code)
	}
}

func TestPlugin_NoPatternsFileClassicBehavior(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*time.Second, 60*time.Second, time.Hour, time.Hour),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("400-499"),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	plugin.next = next

	// Without a patterns file a 200 on a probe-ish path is not counted
	for range 10 {
		req := httptest.NewRequest(http.MethodGet, "/.env", nil)
		req.Header.Set("X-Forwarded-For", "136.85.1.1")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 passthrough (no patterns configured), got %d", rec.Code)
		}
	}
}

func TestPlugin_AllowedIPSkipsPatternCounting(t *testing.T) {
	dir := t.TempDir()

	path := writePatternsFile(t, dir, "(?i)^/\\.env\n")

	plugin := &JailPlugin{
		jailer:        NewJailer(1, 60*time.Second, 60*time.Second, time.Hour, time.Hour),
		allowList:     []string{"10.0.0.0/8"},
		stats:         newRequestStats(),
		errorCodes:    parseErrorCodes("400-499"),
		patterns:      newPatternList(path),
		patternWeight: 5,
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	plugin.next = next

	// Threshold 1 and patternWeight 5 would ban instantly, but the
	// allowlisted IP bypasses pattern counting entirely.
	for range 10 {
		req := httptest.NewRequest(http.MethodGet, "/.env", nil)
		req.Header.Set("X-Forwarded-For", "10.0.0.7")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("allowed IP: expected 200 passthrough, got %d", rec.Code)
		}
	}
}

func TestNew_PatternDefaults(t *testing.T) {
	dir := t.TempDir()

	path := writePatternsFile(t, dir, "(?i)^/\\.env\n")

	cfg := &Config{
		Threshold:    5,
		Window:       30,
		BaseBan:      60,
		MaxBan:       3600,
		ResetAfter:   3600,
		PatternsFile: path,
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})

	h, err := New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	p, ok := h.(*JailPlugin)
	if !ok {
		t.Fatalf("New() returned %T, want *JailPlugin", h)
	}

	if p.patternWeight != 3 {
		t.Errorf("patternWeight = %d, want 3 (default)", p.patternWeight)
	}

	if p.patterns == nil {
		t.Fatal("patterns should be loaded when patternsFile is set")
	}

	if !p.patterns.matches("/.env") {
		t.Error("loaded pattern should match")
	}
}

// loadProductionPatterns extracts the patterns.txt block from the Helm
// template that ships the plugin ConfigMap, so the shipped list itself is
// what gets validated.
func loadProductionPatterns(t testing.TB) []*regexp.Regexp {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "templates", "traefik-jail.yaml"))
	if err != nil {
		t.Fatalf("reading template: %v", err)
	}

	lines := strings.Split(string(data), "\n")

	start, end := -1, -1

	for i, line := range lines {
		switch {
		case strings.TrimSpace(line) == "patterns.txt: |":
			start = i + 1
		case start >= 0 && line != "" && !strings.HasPrefix(line, "    ") && end == -1:
			end = i
		}
	}

	if start < 0 || end < 0 {
		t.Fatal("patterns.txt block not found in template")
	}

	regexes := make([]*regexp.Regexp, 0, 64)

	for _, line := range lines[start:end] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		re, err := regexp.Compile(line)
		if err != nil {
			t.Errorf("production pattern %q does not compile: %v", line, err)
			continue
		}

		regexes = append(regexes, re)
	}

	if len(regexes) == 0 {
		t.Fatal("no production patterns found")
	}

	return regexes
}

func TestProductionPatterns_MatchProbes(t *testing.T) {
	regexes := loadProductionPatterns(t)

	probes := []string{
		// curated
		"/.env", "/.env.production", "/api/.env",
		"/.ssh/id_rsa", "/.git/config", "/.aws/credentials",
		"/.config/gcloud/credentials.db",
		"/wp-config.php", "/wp-admin/setup-config.php", "/phpinfo.php",
		"/blog/wp/v2/users",
		"/backup.sql", "/dump.sql",
		"/secrets.json", "/server.key", "/docker-compose.yml",
		"/dbadmin/",
		"/storage/logs/laravel.log", "/user_secrets.yml",
		"/zzcanary-abc123",
		// fail2ban-derived (botsearch)
		"/roundcube/", "/mail", "/webmail", "/v-webmail", "/horde",
		"/pma", "/pma/", "/phpmyadmin", "/phpMyAdmin-4.2.5",
		"/typo3/phpmyadmin/", "/admin/pma",
		"/wp-login.php", "/wp-signup.php", "/wp-admin.php",
		"/cgi-bin/test.cgi", "/mysqladmin/",
	}

	for _, probe := range probes {
		matched := false

		for _, re := range regexes {
			if re.MatchString(probe) {
				matched = true
				break
			}
		}

		if !matched {
			t.Errorf("probe %q matches no pattern", probe)
		}
	}
}

func TestProductionPatterns_NoFalsePositives(t *testing.T) {
	regexes := loadProductionPatterns(t)

	legit := []string{
		"/", "/health", "/healthz", "/ready",
		"/api/users", "/api/v1/things",
		"/wp-content/uploads/a.png",
		"/keynote.pdf", "/secrets-guide",
		"/mailman", "/email", "/mailbox/feed",
		"/pmarticles",
		"/admin/panel",
		"/sitemap.xml", "/blog/post-1",
		"/docker-compose.yaml",
	}

	for _, path := range legit {
		for _, re := range regexes {
			if re.MatchString(path) {
				t.Errorf("legit path %q falsely matches %q", path, re.String())
			}
		}
	}
}

// BenchmarkPatternList_Matches measures the per-request cost of the pattern
// scan through the production patternList path (mutex, stat throttle and
// regex scan included). matches() runs on every non-bypassed request, so the
// no-match cases are the ones that matter for overall proxy overhead.
func BenchmarkPatternList_Matches(b *testing.B) {
	p := &patternList{path: "/nonexistent", regexes: loadProductionPatterns(b)}

	// Prime the stat throttle so the benchmark measures steady state.
	_ = p.matches("/.env")

	benchmarks := []struct {
		name string
		path string
	}{
		{"no-match-long", "/api/v1/some/deeply/nested/resource/with/many/segments"},
		{"no-match-short", "/"},
		{"match-first-pattern", "/.env"},
		{"match-last-pattern", "/mysqladmin/"},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				_ = p.matches(bm.path)
			}
		})
	}
}

func TestNew_ValidConfig(t *testing.T) {
	cfg := &Config{
		Threshold:  5,
		Window:     30,
		BaseBan:    120,
		MaxBan:     7200,
		ResetAfter: 1800,
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})

	h, err := New(context.Background(), next, cfg, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if h == nil {
		t.Fatal("New() returned nil handler")
	}
}

func TestCreateConfig_Defaults(t *testing.T) {
	cfg := CreateConfig()

	if cfg.Threshold != 10 {
		t.Errorf("Threshold = %d, want 10", cfg.Threshold)
	}

	if cfg.Window != 60 {
		t.Errorf("Window = %d, want 60", cfg.Window)
	}

	if cfg.BaseBan != 60 {
		t.Errorf("BaseBan = %d, want 60", cfg.BaseBan)
	}

	if cfg.MaxBan != 3600 {
		t.Errorf("MaxBan = %d, want 3600", cfg.MaxBan)
	}

	if cfg.ResetAfter != 3600 {
		t.Errorf("ResetAfter = %d, want 3600", cfg.ResetAfter)
	}
}

func TestCodeMatcher(t *testing.T) {
	tests := []struct {
		name   string
		config string
		status int
		want   bool
	}{
		{"range match", "400-499", 404, true},
		{"range no match", "400-499", 500, false},
		{"single match", "404", 404, true},
		{"single no match", "404", 403, false},
		{"mixed range and single match range", "404,500-503", 502, true},
		{"mixed range and single match single", "404,500-503", 404, true},
		{"mixed no match", "404,500-503", 403, false},
		{"empty config no match", "", 404, false},
		{"invalid code ignored", "404,abc,500", 500, true},
		{"whitespace trimmed", " 404 , 500-503 ", 404, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := parseErrorCodes(tt.config)
			if m.matches(tt.status) != tt.want {
				t.Errorf("parseErrorCodes(%q).matches(%d) = %v, want %v",
					tt.config, tt.status, m.matches(tt.status), tt.want)
			}
		})
	}
}

func TestPlugin_CustomErrorCodes_5xxCounted(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("500-503"),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	plugin.next = next

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "7.7.7.7")

	rec := httptest.NewRecorder()
	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 passthrough, got %d", rec.Code)
	}

	// Should be banned now (500 matches custom config)
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-Forwarded-For", "7.7.7.7")

	rec2 := httptest.NewRecorder()
	plugin.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (banned), got %d", rec2.Code)
	}
}

func TestPlugin_CustomErrorCodes_4xxNotCounted(t *testing.T) {
	plugin := &JailPlugin{
		jailer:     NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:      newRequestStats(),
		errorCodes: parseErrorCodes("500-503"),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "6.6.6.6")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 passthrough, got %d", rec.Code)
		}
	}
}

func TestPlugin_ExcludeURLs_SkipsJail(t *testing.T) {
	plugin := &JailPlugin{
		jailer:      NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:       newRequestStats(),
		errorCodes:  parseErrorCodes("400-499"),
		excludeURLs: []string{"niks3.terence.cloud/*.narinfo", "niks3.terence.cloud/api/*"},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	// Threshold is 1 — any 404 should ban. But excluded URLs bypass the jail entirely.
	excludedPaths := []string{
		"/abc123.narinfo",
		"/api/cache-config",
		"/api/pending_closures",
	}

	for _, p := range excludedPaths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = "niks3.terence.cloud"
		req.Header.Set("X-Forwarded-For", "1.2.3.4")

		rec := httptest.NewRecorder()
		plugin.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("excluded URL %s: expected 404 passthrough, got %d", p, rec.Code)
		}
	}

	// Verify the IP was never banned: a non-excluded request should still pass.
	req := httptest.NewRequest(http.MethodGet, "/other", nil)
	req.Host = "niks3.terence.cloud"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	rec := httptest.NewRecorder()
	plugin.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatal("IP should not be banned — excluded URLs must not count toward threshold")
	}
}

func TestPlugin_ExcludeURLs_PathOnly(t *testing.T) {
	plugin := &JailPlugin{
		jailer:      NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:       newRequestStats(),
		errorCodes:  parseErrorCodes("400-499"),
		excludeURLs: []string{"/*.narinfo"},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	// Path-only pattern matches regardless of host.
	req := httptest.NewRequest(http.MethodGet, "/abc.narinfo", nil)
	req.Host = "anything.terence.cloud"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	rec := httptest.NewRecorder()
	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("path-only exclude: expected 404 passthrough, got %d", rec.Code)
	}
}

func TestPlugin_ExcludeURLs_NonExcludedStillJailed(t *testing.T) {
	plugin := &JailPlugin{
		jailer:      NewJailer(1, 60*1000_000_000, 60*1000_000_000, 3600*1000_000_000, 3600*1000_000_000),
		stats:       newRequestStats(),
		errorCodes:  parseErrorCodes("400-499"),
		excludeURLs: []string{"niks3.terence.cloud/*.narinfo"},
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	plugin.next = next

	// Non-excluded path on the same host triggers a ban.
	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	req.Host = "niks3.terence.cloud"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	rec := httptest.NewRecorder()
	plugin.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("first request: expected 404, got %d", rec.Code)
	}

	// Second request — banned now.
	req2 := httptest.NewRequest(http.MethodGet, "/other", nil)
	req2.Host = "niks3.terence.cloud"
	req2.Header.Set("X-Forwarded-For", "1.2.3.4")

	rec2 := httptest.NewRecorder()
	plugin.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (banned from non-excluded path), got %d", rec2.Code)
	}
}
