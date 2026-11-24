# Threat model

Plumb is read-only by design. This page lists every way into it, who may use it, what it can
change, and what guards it. Update it in the same PR that adds a new way in (a command, a
button, a listener, a route, a new API Plumb calls).

## What Plumb can change

| Target | Change | Who triggers it | Guard |
| --- | --- | --- | --- |
| Alertmanager | Creates a silence (1h, 4h or 24h) for exactly one alert group | Platform role member clicks Ack | Guild and role checked on the server; matchers come from Plumb's own message; a silence without matchers is refused; audited |
| Discord | Posts and edits its own messages in the alert and notice channels | Alertmanager webhook, `/notice`, Resolve | Webhook token; guild and role check for commands and buttons; mentions limited to the Platform role; audited |
| Public status page | Shows notices | `/notice`, Resolve, the git-managed `notices.yaml` | Only Plumb's own messages are read back, and only after it knows its own ID |

Nothing else is written: no Kubernetes object, no database, no file.

## Ways in

| Way in | Reachable from | Authentication | Input limits |
| --- | --- | --- | --- |
| Discord gateway (slash commands, buttons) | Discord, outbound connection only | Discord signs and delivers interactions; Plumb checks guild ID and Platform role on every one (`Bot.allowed`) | Discord limits option sizes; `/notice` title 100 and details 1000 characters, duration at most 14 days |
| `POST /webhook/alertmanager` on `:8080` | Cluster-internal only | Bearer token (`PLUMB_WEBHOOK_TOKEN`), compared in constant time | 1 MiB body, 15 s handling timeout |
| `/healthz`, `/readyz`, `/metrics` on `:8080` | Cluster-internal only | None | No input |
| Status page `/`, `/api/status.json`, `/assets/*` on `:8081` | Public | None (read-only) | No input; strict CSP; read and write timeouts |

## What Plumb reads, with which rights

| System | Right | Credential |
| --- | --- | --- |
| VictoriaMetrics | PromQL queries | None (cluster-internal) |
| Alertmanager | Read alerts, create silences | None (cluster-internal) |
| Kubernetes API (optional discovery) | get and list on Traefik IngressRoutes | Pod service account |
| Discord | Bot scope in one server | `PLUMB_DISCORD_TOKEN` or `_FILE` |

## Secrets

| Secret | Lives in | Owner | Rotation |
| --- | --- | --- | --- |
| Discord bot token | Cluster secret, mounted as a file | Mike | Reset in the Discord developer portal, update the secret, restart |
| Webhook token | Cluster secret, shared with Alertmanager | Mike | New random value in both places, restart both |

Neither is logged or shown in any response.

## Known gaps

- The webhook token is optional. When it is empty, the NetworkPolicy is the only guard.
  TODO: decide whether Plumb should refuse to start without it.
- One Discord role covers both reading (`/status`) and writing (Ack, `/notice`). The handbook's
  `viewer` and `operator` split is not modelled yet.
- Anyone who can reach `:8080` can read `/metrics`; it holds only per-service up or down values.
- Audit lines go to the container log; how long they are kept depends on the cluster's log
  retention.
