# Plumb

Plumb is the BitsAndBricks Platform bot for the Oak cluster. Like a plumb
line, it measures and never moves anything.

- Posts Oak alerts from Alertmanager to Discord, one message per alert group,
  updated in place and marked resolved when the alert clears.
- **Ack** buttons (1h, 4h, 24h) create an Alertmanager silence for exactly that
  alert group, recorded with the Discord user's name.
- `/status` gives a one-screen health summary: nodes, firing alerts, broken
  pods, deployments missing replicas, Longhorn, Postgres connections, disk.
- Serves a public status page (up, slow or down per app, 24h history) on its
  own port.

## Read-only by design

| What | Access |
| --- | --- |
| Kubernetes API | none in v1 (everything comes from metrics) |
| VictoriaMetrics | read (PromQL queries) |
| Alertmanager | read alerts, create silences (the Ack button), nothing else |
| Discord | send and edit its own messages, slash commands |

Commands and buttons only work in the configured server and only for members
with the Platform role. Every command and Ack is logged as JSON with
`"audit":true` and the Discord user. A silence without matchers is refused, so
an Ack can never mute everything.

There is no public endpoint for Discord: Plumb connects out to the Discord
gateway. The internal listener (`:8080`, webhook and health checks) must stay
cluster-internal; only the status listener (`:8081`) may be routed publicly,
and it serves nothing but `/` and `/api/status.json`.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `PLUMB_DISCORD_TOKEN` or `PLUMB_DISCORD_TOKEN_FILE` | required | Bot token |
| `PLUMB_GUILD_ID` | required | Discord server ID |
| `PLUMB_ALERT_CHANNEL_ID` | required | Channel for alerts |
| `PLUMB_PLATFORM_ROLE_ID` | required | Role allowed to use commands and Ack |
| `PLUMB_PING_ON_CRITICAL` | `true` | Mention the role on new critical alerts |
| `PLUMB_ALERTMANAGER_URL` | `http://alertmanager.operations.svc.cluster.local:9093` | |
| `PLUMB_METRICS_URL` | `http://victoriametrics.platform.svc.cluster.local:8428` | |
| `PLUMB_GRAFANA_URL` | empty | Link on `/status` |
| `PLUMB_LISTEN_ADDR` | `:8080` | Internal listener |
| `PLUMB_WEBHOOK_TOKEN` or `PLUMB_WEBHOOK_TOKEN_FILE` | empty | Bearer token Alertmanager must send |
| `PLUMB_STATUS_ADDR` | `:8081` | Public status page listener |
| `PLUMB_STATUS_TITLE` | `BitsAndBricks status` | Page title |
| `PLUMB_STATUS_SERVICES` | empty (page off) | `Name=https://url,Other=https://url2` |
| `PLUMB_STATUS_INTERVAL` | `60s` | Probe interval |
| `PLUMB_STATUS_SLOW` | `2s` | Slower than this shows as "Slow" |

Alertmanager receiver:

```yaml
receivers:
  - name: plumb
    webhook_configs:
      - url: http://plumb.operations.svc.cluster.local:8080/webhook/alertmanager
        send_resolved: true
        http_config:
          authorization:
            credentials_file: /etc/alertmanager/plumb/token
```

## Development

```sh
go test ./...
go build ./cmd/plumb
```

Images are built by GitHub Actions to `ghcr.io/bitsandbricks-ch/plumb`
(`latest` and `sha-…` from `main`, `X.Y.Z` from `vX.Y.Z` tags).

## Roadmap

- v2: `/alerts`, `/app`, `/db`, `/certs`, `/flux`, `/logs` (private channel only)
- v3: deploy feed, 08:00 digest
