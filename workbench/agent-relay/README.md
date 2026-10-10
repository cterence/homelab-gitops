# agent-relay

Generic webhook to Mistral-agent relay. Each configured route receives a JSON
payload over HTTP, renders a prompt template, and starts a conversation with
the configured Mistral agent (Agents API). Requests are answered `202` before
the conversation runs, so callers never block on the agent.

Deployed as `http://agent-relay.agent-relay:8080` (cluster-internal only).

## Route configuration

Routes are read from the YAML file at `AGENT_RELAY_CONFIG`
(default `/config/config.yaml`, mounted from the app's ConfigMap):

```yaml
routes:
  - path: /alerts
    agent_id: ag_xxx
    conversation_name: alert-triage
    prompt: |
      An Alertmanager alert fired. Payload:

      {{.JSON}}
    dedup_key: alerts.0.labels.alertname
    dedup_window: 15m
```

| Field | Meaning |
|---|---|
| `path` | Webhook path; only `POST <path>` is served |
| `agent_id` | Mistral agent invoked for this route |
| `conversation_name` | Name given to the stored conversation (optional) |
| `prompt` | Go `text/template` with the request body as `.JSON`, pretty-printed |
| `dedup_key` | Dot-path into the payload; requests sharing the resolved string value within `dedup_window` are dropped (optional) |
| `dedup_window` | Go duration string (`15m`, `1h`); both fields required for dedup |

## Endpoints

| Method | Path | Behavior |
|---|---|---|
| `GET` | `/health` | `{"status":"ok"}` |
| `POST` | `<route.path>` | `202 Accepted`; `400` on invalid JSON |

Deduplicated requests also return `202` so Alertmanager does not retry them.

## Environment variables

| Variable | Purpose |
|---|---|
| `MISTRAL_API_KEY` | Mistral API key (from the `agent-relay/credentials` openbao secret) |
| `AGENT_RELAY_CONFIG` | Config file path (default `/config/config.yaml`) |

## Behavior notes

- The Mistral call runs in a background goroutine with a 10-minute timeout,
  detached from the request context. Failures are logged (`agent conversation
  failed`) and are not retried — callers such as Alertmanager already have
  their own delivery path for the raw alert.
- Dedup state is in-memory and per-pod; a restart can cause one extra
  invocation per key in the worst case.
