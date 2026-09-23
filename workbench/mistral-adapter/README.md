# mistral-adapter

Reverse proxy that normalizes Mistral's OpenAI-compatible chat completions
API for clients that expect standard OpenAI wire formats. Deployed as part of
the kagent umbrella chart (k8s-apps/kagent) at
`http://kagent-mistral-adapter.kagent:8080`.

## Why

Mistral hosts Z.ai GLM reasoning models (e.g. `zai-glm-5-3`) with a
non-standard response shape: `choices[].message.content` (and
`choices[].delta.content` in streaming chunks) arrives as a typed parts
array instead of a plain string, which breaks OpenAI-compatible clients
without any error (see mistralai/mistral-vibe#1119). kagent renders the raw
parts JSON into its chat responses.

## What it does

- Forwards everything to the upstream unchanged, except responses on paths
  ending in `/chat/completions`.
- Flattens typed content arrays: `text` parts are concatenated into a plain
  string `content`; `thinking` parts are moved to `reasoning_content`.
  Works on both non-streaming JSON responses and SSE streams (each `data:`
  chunk is rewritten in place and flushed immediately).
- Payloads that are not typed arrays (plain string content, tool-call
  chunks, error responses, non-JSON) pass through untouched, so any model —
  including Mistral-native ones — can safely be routed through this proxy.

## Configuration (environment variables)

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | Listen address for the HTTP server |
| `UPSTREAM_BASE_URL` | `https://api.mistral.ai` | Upstream API base URL (no trailing slash, no `/v1` suffix) |

## Endpoints

- `GET /healthz` — local liveness probe, answers `ok` without proxying.
- Everything else — reverse-proxied to the upstream.

## Use with kagent

Point the kagent ModelConfig `openAI.baseUrl` at the adapter instead of
Mistral directly:

```yaml
providers:
  openAI:
    provider: OpenAI
    model: zai-glm-5-3
    config:
      baseUrl: http://mistral-adapter.mistral-adapter:8080/v1
```
