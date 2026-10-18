#!/usr/bin/env bash
# End-to-end test for telegram-hub: builds and starts the server locally,
# loads the real credentials from the in-cluster secret (without ever
# printing them), then drives the send_message and tts_article tools over
# the streamable-HTTP transport and checks the host guard.
#
# Usage: ./e2e/run.sh
set -euo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
NAMESPACE="${NAMESPACE:-telegram-hub}"
SERVER_BIN="$(mktemp -t telegram-hub-e2e.XXXXXX)"
CLIENT_BIN="$(mktemp -t telegram-hub-e2e-client.XXXXXX)"

cleanup() {
  if [ -n "${SERVER_PID:-}" ]; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  rm -f "$SERVER_BIN" "$CLIENT_BIN"
}
trap cleanup EXIT

# Build the server and the e2e client.
(cd "$APP_DIR" && go build -o "$SERVER_BIN" .)
(cd "$APP_DIR/e2e" && go build -o "$CLIENT_BIN" .)

# Load credentials straight into the server environment; never echoed.
export TELEGRAM_BOT_TOKEN="$(kubectl get secret credentials -n "$NAMESPACE" -o jsonpath='{.data.TELEGRAM_BOT_TOKEN}' | base64 -d)"
export TELEGRAM_CHAT_ID="$(kubectl get secret credentials -n "$NAMESPACE" -o jsonpath='{.data.TELEGRAM_CHAT_ID}' | base64 -d)"
export MISTRAL_API_KEY="$(kubectl get secret credentials -n "$NAMESPACE" -o jsonpath='{.data.MISTRAL_API_KEY}' | base64 -d)"
export ALLOWED_USERS="$(kubectl get secret credentials -n "$NAMESPACE" -o jsonpath='{.data.ALLOWED_USERS}' | base64 -d)"
for var in TELEGRAM_BOT_TOKEN TELEGRAM_CHAT_ID MISTRAL_API_KEY ALLOWED_USERS; do
  if [ -z "${!var}" ]; then
    echo "failed to load $var from namespace $NAMESPACE" >&2
    exit 1
  fi
done
echo "credentials loaded from namespace $NAMESPACE (values not printed)"

"$SERVER_BIN" &
SERVER_PID=$!
sleep 1

echo "--- health check:"
curl -sf http://127.0.0.1:8000/health
echo
echo "--- host guard (expect 421):"
GUARD_STATUS="$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.com' -X POST http://127.0.0.1:8000/mcp)"
if [ "$GUARD_STATUS" != "421" ]; then
  echo "host guard check failed: got $GUARD_STATUS, want 421" >&2
  exit 1
fi
echo "$GUARD_STATUS"

echo "--- mcp e2e:"
"$CLIENT_BIN"
