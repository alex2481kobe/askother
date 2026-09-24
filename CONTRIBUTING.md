# Contributing

Thanks for helping. AskOther is meant to stay small, so the best changes are short,
well tested and fix a real problem.

## Build and test

You need Go 1.26 or newer. Nothing else.

```sh
go build -trimpath ./...
go vet ./...
go test -race ./...
```

Run all three before you open a pull request. `gofmt -l .` should print nothing.

## Rules for code

- **Standard library only.** Do not add modules to `go.mod`.
- **Keep it small.** Add a file, flag or option only when a real use or a real
  failure needs it. Aim for files of about 200 lines.
- **Comments explain behavior in plain words.**

## Rules for tests

- **Show that a test can fail.** Before you trust a new test, break the code it
  checks on purpose, watch the test fail, then put the code back. A test that
  was never seen failing proves nothing. Say in your pull request what you
  broke and how the test caught it.
- **Never let tests reach the real CLIs.** Tests use a fake worker program that
  imitates `claude` and `codex`. Every test process gets a minimal environment:
  `PATH=/usr/bin:/bin`, a temporary `HOME`, and temporary `ASKOTHER_HOME` and
  `ASKOTHER_CONFIG`. Worker binaries come only from that temporary config. If a
  test inherited your real `PATH`, one mistake could start a real agent,
  spend real model tokens, and touch real files.
- **Tests that call the real CLIs are opt-in.** They sit behind the `realcli`
  build tag and run only when you ask for them, for example
  `go test -tags realcli -run RealClaude ./internal/worker`.
- **Test data is made up.** Use synthetic paths such as `/synthetic/user`.
  Never copy real session logs, run records or home paths into the repo.
- **Clean up.** A test must not leave processes running. After a test run,
  no `askother supervise` or fake worker process should remain.

## Pull requests

- Explain what changed and which checks you ran.
- Keep unrelated changes out of the pull request.
- Workflows on pull requests from outside contributors run only after a
  maintainer approves them.
- Changes to `.github/`, `go.mod`, `SECURITY.md`, this file, `LICENSE`,
  `AGENTS.md` or `CLAUDE.md` need owner review. Maintainers may ask for smaller
  pull requests when these change.
- Never commit secrets, tokens, API keys, `.env` files, logs, or absolute paths
  from your machine.
- Workflows must keep read-only permissions and must not use
  `pull_request_target` or receive secrets.

## License

Contributions are accepted under Apache-2.0. By contributing, you agree that
your contribution can be distributed under that license.
