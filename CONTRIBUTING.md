# Contributing

Plumb follows the [BitsAndBricks handbook](https://github.com/bitsandbricks-ch/handbook).
What is specific to this repo is in [AGENTS.md](AGENTS.md); it applies to people too.

1. Open an issue first for anything bigger than a small fix.
2. Work on a branch (`<type>/<short-topic>`, e.g. `fix/notice-timezone`), and open a draft PR.
3. Before pushing, run what CI runs: `gofmt -l .`, `go vet ./...`, `go test -race ./...`.
4. Add a fragment to `changelog.d/` for a change people running Plumb will notice.
5. Mark the PR ready when checks are green. The maintainer merges.

Plumb is read-only by design: a change that lets it write anywhere new needs a setting
that is off by default, a server-side permission check, and an audit log line.
