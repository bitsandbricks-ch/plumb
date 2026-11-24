# Plumb

Plumb is the BitsAndBricks Platform bot for the Oak cluster. Like a plumb
line, it measures and never moves anything.

- Posts Oak alerts from Alertmanager to Discord, one message per alert group,
  updated in place and marked resolved when the alert clears.
- **Ack** buttons (1h, 4h, 24h) create an Alertmanager silence for exactly that
  alert group, recorded with the Discord user's name.
- `/status` gives a one-screen health summary: nodes, firing alerts, broken
  pods, deployments missing replicas, Longhorn, Postgres connections, disk.
- `/notice` posts a maintenance, incident or info notice to the public status
  page, with a Resolve button. Notices are Plumb's own Discord messages, read
  back from the channel, so Plumb keeps no state of its own.
- Serves the public status page on its own port: up, degraded or down per app,
  90 days of daily uptime, a cluster summary made of counts only, notices
  (from `/notice` and from a git-managed `notices.yaml`) and past incidents
  (detected outages plus written notes).

## Read-only by design

| What | Access |
| --- | --- |
| Kubernetes API | get/list on Traefik IngressRoutes only, for status page discovery (optional) |
| VictoriaMetrics | read (PromQL queries) |
| Alertmanager | read alerts, create silences (the Ack button), nothing else |
| Discord | send, edit and read back its own messages in its channels, slash commands |

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
| `PLUMB_STATUS_SLOW` | `2s` | Slower than this shows as "Degraded" |
| `PLUMB_STATUS_HOST` | empty | Host shown in the page's title bar |
| `PLUMB_STATUS_DISCOVERY` | `false` | Also show IngressRoutes labelled `status.reyki.me/show: "true"` |
| `PLUMB_NOTICES_FILE` | empty | Git-managed notices (YAML, see below) |
| `PLUMB_NOTICE_CHANNEL_ID` | alert channel | Where `/notice` posts live |
| `PLUMB_TIMEZONE` | `Europe/Zurich` | Times on the page and in `/notice` |

Status page discovery (on an IngressRoute):

```yaml
metadata:
  labels:
    status.reyki.me/show: "true"
  annotations:
    status.reyki.me/name: Floorbit                        # else reyki.me/name, else the route name
    status.reyki.me/url: https://floor.seatbit.ch/healthz # else https://<first Host>/
    status.reyki.me/group: Apps                           # optional
    status.reyki.me/order: "20"                           # optional, else reyki.me/order
```

`notices.yaml`:

```yaml
notices:
  - title: Postgres minor upgrade
    kind: maintenance            # maintenance | incident | info
    services: [Seatbit, Floorbit]
    start: 2026-11-20T20:00:00+01:00
    end: 2026-11-20T20:30:00+01:00
    body: Each app may be unreachable for about a minute.
```

History: Plumb serves `plumb_status_state{service,group}` (2 up, 1 degraded,
0 down) on `:8080/metrics`. VictoriaMetrics scrapes it, and the page reads 90
days of daily uptime and 14 days of outages back from there.

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
(`latest`, `sha-…` and `main-<UTC timestamp>-<sha>` from `main`, `X.Y.Z` from
`vX.Y.Z` tags). Oak's Flux image automation follows the `main-…` tags.

## Roadmap

- v2: `/alerts`, `/app`, `/db`, `/certs`, `/flux`, `/logs` (private channel only)
- v3: deploy feed, 08:00 digest

Logo: `assets/plumb-mark.svg` (uses `currentColor` for the string), and
`assets/plumb-icon.svg` / `plumb-icon-512.png` for the Discord app icon.

## Licence

[MIT](LICENSE). The bundled fonts in `internal/statuspage/assets/` (Archivo, IBM Plex Mono)
are under the SIL Open Font License; their licence files sit next to them.
