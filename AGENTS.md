# AGENTS

Guide for coding agents and people changing this repo. Read `README.md` first
for what AskOther does. `CONTRIBUTING.md` has the full test rules.

## Layout

- `cmd/askother/`: the `askother` command: `mcp`, `wait`, `ui`, `help`, `version`,
  and the hidden `supervise` step that watches one run.
- `internal/run/`: run records on disk, locks, atomic writes, and worker
  process groups.
- `internal/lifecycle/`: starting, continuing, supervising, stopping, waiting
  for and cleaning up runs.
- `internal/worker/`: the Claude and Codex adapters: the command line each
  worker gets, and how its output is read.
- `internal/mcp/`: the stdio MCP server and the six tool schemas.
- `internal/tools/`: connects each MCP tool to the lifecycle code.
- `internal/config/`: paths, the optional config file, finding worker
  binaries, the worker environment allowlist, and caller identity.
- `internal/fakeworker/`, `internal/testutil/`: the fake worker and helpers
  that tests use instead of the real CLIs.
- `internal/ui/`: the local run viewer that `askother ui` serves.
- `internal/e2e/`: end to end tests on the built binary.
- `assets/`: the logo, favicon, provider icons and README screenshot.
- `skills/askother/`: the short agent skill users can install.

## Commands

```sh
go build -trimpath ./...
go vet ./...
go test -race -count=1 ./...
```

## Rules

- Standard library only. No new modules.
- Keep it minimal. Add code only for a real use or a real failure.
- One owner per concept. No compatibility shims or dead code.
- A test counts only after you have seen it fail against broken code.
- Tests never reach the real `claude` or `codex`. Give every test process a
  minimal environment (`PATH=/usr/bin:/bin`, temporary `HOME`, `ASKOTHER_HOME`
  and `ASKOTHER_CONFIG`).
- Tests leave no processes running.
- This repo is public. Never commit absolute machine paths, user names, host
  names, tokens, keys, logs, run records or internal planning notes.
- Stage explicit paths only. Never `git add -A` or `git add .`.
