# Changelog fragments

Each user-facing change adds one file here instead of editing a shared changelog, so two
PRs never touch the same line. The release tooling collects them into `CHANGELOG.md`
(see the handbook's [releases](https://github.com/bitsandbricks-ch/handbook/blob/main/core/releases.md)).

- Name: `<branch-topic>.<kind>.md`, for example `webhook-token.security.md`.
- Kind: `added`, `changed`, `fixed`, `removed` or `security`.
- Content: one or two plain sentences for someone running Plumb.

Internal changes (CI, tests, refactors) need no fragment.
