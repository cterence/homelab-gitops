package main

import (
	"log/slog"
	"net/http"
	"strings"
)

// allowedHostPatterns guards against DNS-rebinding: hosts are matched
// exactly, and entries ending in ":*" match the host with any port.
var allowedHostPatterns = []string{
	"localhost",
	"localhost:*",
	"127.0.0.1",
	"127.0.0.1:*",
	"[::1]:*",
	"tmcp.terence.cloud",
	"tmcp.terence.cloud:*",
	"telegram-mcp.snow-delta.ts.net",
	"telegram-mcp.snow-delta.ts.net:*",
}

func hostAllowed(host string) bool {
	for _, pattern := range allowedHostPatterns {
		if pattern == host {
			return true
		}

		if base, ok := strings.CutSuffix(pattern, ":*"); ok && strings.HasPrefix(host, base+":") {
			return true
		}
	}

	return false
}

// hostGuard rejects requests whose Host header is outside the allowlist with
// 421 Misdirected Request, mirroring the DNS-rebinding protection the Python
// MCP SDK applied via TransportSecuritySettings.
func hostGuard(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			logger.Warn("rejected host", "host", r.Host, "path", r.URL.Path)
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)

			return
		}

		next.ServeHTTP(w, r)
	})
}
