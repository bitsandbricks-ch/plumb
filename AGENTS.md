# Rules for coding agents

These rules are for AI coding agents (Claude Code, Codex, Copilot, opencode, ...) working on Plumb.
`CLAUDE.md` imports this file.

This repo follows the [BitsAndBricks handbook](https://github.com/bitsandbricks-ch/handbook),
profile **service** (see `.mason.toml`). Read its core chapters and the blocks this profile
lists. The rules below are only what is specific to this repo.

TODO: the handbook has no `service` profile yet (a small always-on Go service in the cluster:
Go, deploy, observability, and "What a cluster expects from an app" in the Kubernetes block).
Until it does, follow core, [local agents](https://github.com/bitsandbricks-ch/handbook/blob/main/blocks/local-agents.md)
where it names Plumb, and [Kubernetes](https://github.com/bitsandbricks-ch/handbook/blob/main/blocks/kubernetes.md).

## What this is

Plumb is the BitsAndBricks platform bot: a single Go binary that posts cluster alerts from
Alertmanager to Discord with an Ack button, answers read-only slash commands (`/status`,
`/notice`), and serves a public status page. It connects out to the Discord gateway and opens
no public endpoint for Discord.

## Before you push

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...
```

## Repo-specific rules

- **Read-only by design.** The only writes Plumb makes are Alertmanager silences (the Ack
  button) and its own Discord messages. A new write ability is off by default, behind its own
  setting, and says so in the PR.
- **Every interaction is checked on the server.** `Bot.allowed` (guild and Platform role) runs
  before any command or button does anything. A new command or button goes through it too.
- **Every write is audited.** Log it as JSON with `"audit", true`, the Discord user name and
  user ID, and what changed.
- **Two listeners, kept apart.** `:8080` (webhook, `/healthz`, `/readyz`, `/metrics`) stays
  cluster-internal. `:8081` is the public status page and serves only the page, its assets and
  `/api/status.json`. Nothing on it names pods, nodes, alerts or error text.
- **No state of its own.** Discord holds the notices; VictoriaMetrics holds the history. Don't
  add a database or volume without a decision record in `docs/decisions/`.
- **Config** comes only through `internal/config`, from `PLUMB_*` environment variables, and
  `*_FILE` for secrets. Document every new variable in the README table.
- **Examples** in code, tests and docs use `example.com`. The status page discovery label keys (`LabelShow` and friends in `internal/statuspage/discovery.go`)
  and the status page footer link are a known exception, waiting on a decision.
- **Image tags** differ from the handbook: `main` builds also get
  `main-<UTC timestamp>-<sha>` for Flux image automation, and `latest` follows `main`. Keep
  this until the handbook settles it.
