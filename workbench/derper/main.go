package main

// A minimal DERP relay for the catbox stash, built on the same pinned
// tailscale.com DERP stack the tailcat data plane uses. TLS terminates
// at traefik; this binary serves plain HTTP. Admission is delegated to
// the stash: a client key is only allowed to relay once catbox's
// /derp/admit endpoint (roster-backed) says so.

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"tailscale.com/derp/derpserver"
	"tailscale.com/types/key"
)

func main() {
	addr := flag.String("addr", ":38090", "HTTP listen address")
	keyFile := flag.String("key", "/data/derper.key", "node key file; created on first boot, stable identity across restarts")
	verifyURL := flag.String("verify-url", "", "admission controller URL (catbox /derp/admit); required, fail-closed")
	flag.Parse()

	if *verifyURL == "" {
		log.Fatal("derper: -verify-url is required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	priv := loadOrCreateKey(*keyFile)

	s := derpserver.New(priv, log.Printf)
	s.SetVerifyClientURL(*verifyURL)

	mux := http.NewServeMux()
	derpHandler := derpserver.Handler(s)
	derpHandler = derpserver.AddWebSocketSupport(s, derpHandler)
	mux.Handle("/derp", derpHandler)
	mux.HandleFunc("/derp/probe", derpserver.ProbeHandler)
	mux.HandleFunc("/derp/latency-check", derpserver.ProbeHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("derper: serving on %s (key %s, admission %s)", *addr, priv.Public(), *verifyURL)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// loadOrCreateKey returns the persisted node key, generating and
// saving a fresh one when the file is missing or empty. The key is the
// relay's identity: clients pin it, so it must survive restarts.
func loadOrCreateKey(path string) key.NodePrivate {
	if b, err := os.ReadFile(path); err == nil {
		var k key.NodePrivate
		if err := k.UnmarshalText(b); err != nil {
			log.Fatalf("derper: parsing %s: %v", path, err)
		}
		if !k.IsZero() {
			return k
		}
	}

	k := key.NewNode()
	b, err := k.MarshalText()
	if err != nil {
		log.Fatalf("derper: marshaling key: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Fatalf("derper: creating key dir: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		log.Fatalf("derper: writing %s: %v", path, err)
	}

	return k
}
