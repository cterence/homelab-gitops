package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// invoker starts a Mistral agent conversation and returns its conversation id.
type invoker func(ctx context.Context, apiKey, agentID, conversationName, prompt string) (string, error)

const (
	maxBody       = 1 << 20
	invokeTimeout = 10 * time.Minute
)

type relay struct {
	logger  *slog.Logger
	apiKey  string
	invoker invoker

	mu   sync.Mutex
	last map[string]time.Time
}

func (rl *relay) handler(r route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxBody))
		if err != nil {
			http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
			return
		}

		var payload any
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid JSON payload", http.StatusBadRequest)
			return
		}

		pretty := &bytes.Buffer{}
		if err := json.Indent(pretty, body, "", "  "); err != nil {
			http.Error(w, "indenting payload", http.StatusBadRequest)
			return
		}

		var prompt bytes.Buffer
		if err := r.promptTpl.Execute(&prompt, map[string]any{"JSON": pretty.String()}); err != nil {
			http.Error(w, "rendering prompt", http.StatusInternalServerError)
			return
		}

		if r.window > 0 && r.DedupKey != "" {
			key, ok := jsonPath(payload, r.DedupKey)
			if !ok {
				rl.logger.Warn("dedup key missing, forwarding without dedup", "route", r.Path, "key_path", r.DedupKey)
			} else if !rl.claim(r.Path+"\x00"+key, r.window, time.Now()) {
				rl.logger.Info("deduplicated request", "route", r.Path, "key", key)
				w.WriteHeader(http.StatusAccepted)

				return
			}
		}

		// Detached from the request: the conversation may outlive it.
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), invokeTimeout)
		go func() {
			defer cancel()

			convID, err := rl.invoker(bgCtx, rl.apiKey, r.AgentID, r.ConversationName, prompt.String())
			if err != nil {
				rl.logger.Error("agent conversation failed", "route", r.Path, "err", err)
				return
			}

			rl.logger.Info("agent conversation started", "route", r.Path, "conversation_id", convID)
		}()

		w.WriteHeader(http.StatusAccepted)
	})
}

// claim records key at now and reports whether window elapsed since its last claim.
func (rl *relay) claim(key string, window time.Duration, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if t, ok := rl.last[key]; ok && now.Sub(t) < window {
		return false
	}

	if rl.last == nil {
		rl.last = map[string]time.Time{}
	}

	rl.last[key] = now

	return true
}

// jsonPath walks a dot-separated path through decoded JSON; numeric segments
// index arrays. Only string leaves are supported.
func jsonPath(v any, path string) (string, bool) {
	cur := v
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return "", false
			}

			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return "", false
			}

			cur = node[i]
		default:
			return "", false
		}
	}

	s, ok := cur.(string)

	return s, ok
}
