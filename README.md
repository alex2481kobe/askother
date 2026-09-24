# Orca

<p align="center"><img src="assets/orca-mark.png" alt="Orca logo" width="160" height="160"></p>

Orca is a small tool that lets one coding agent (Claude Code or Codex in the terminal) start another one, know for sure when it finished, read its answer, send it a follow-up, or stop it.

It is not an agent harness or framework, and it is not a sandbox. It is the thin, reliable piece that lets command line agents call each other.

## Why this version

The first two versions of Orca did too much. They had a lot of code nobody needed, they were not well tested, and they were not reliable or efficient. This version is rebuilt from zero as the bare minimum that is actually needed: one small Go program with no dependencies and no shared server that stays up between sessions.

## Supported

- Claude Code CLI and Codex CLI, on macOS and Linux.
- The desktop apps (the Codex app, and Claude Code inside the Claude desktop app) are untested.
- Chat apps are not supported.

## Install

Orca is built from source with Go 1.26 or newer.

```sh
go install github.com/alex2481kobe/orca/cmd/orca@latest
```

Or clone the repo and build it yourself:

```sh
git clone https://github.com/alex2481kobe/orca.git
cd orca
go build -trimpath -o ~/.local/bin/orca ./cmd/orca
```

Run `orca help` to check that it works. It prints the setup lines below with the real path to your binary filled in.

## Setup (once)

Register Orca with each agent CLI that should be able to start other agents. Replace `/path/to/orca` with the path to your binary.

Claude Code:

```sh
claude mcp add -s user orca -- /path/to/orca mcp
```

Codex (CLI and app), in `~/.codex/config.toml`:

```toml
[mcp_servers.orca]
command = "/path/to/orca"
args = ["mcp"]
default_tools_approval_mode = "approve"
```

Codex needs `default_tools_approval_mode = "approve"` so it can use Orca's tools without stopping to ask a person each time. Without it, an agent working on its own gets stuck waiting for an approval nobody gives.

`orca mcp` is an MCP server (MCP is the standard way agent CLIs load extra tools). You never run it yourself. Each agent session starts its own copy, and it exits when the session ends.

## How agents use it

Orca gives agents six tools:

- `run`: start a worker (`claude` or `codex`) with a prompt in a folder. The reply has the run ID and a `watch` command.
- `wait`: wait up to 50 seconds for one or more runs to end. It never stops them.
- `result`: read a finished run's answer. This marks the run as read.
- `send`: send a follow-up to a finished run. It continues the same worker session as a new, linked run.
- `status`: show one run, or all runs, each with an "unread" flag.
- `stop`: ask a run to stop. Orca asks the worker to exit, and forces it after 5 seconds.

Every `run` and `send` call carries a `key`, a unique string the agent picks. If the agent retries the same call, it gets the same run back instead of starting a second one.

The flow is always the same: `run`, then wait, then `result`.

- **Claude Code agents** can run `orca wait <id>` as a background command. Claude Code wakes the agent when that command exits, so it does not have to keep checking.
- **Codex agents** call the `wait` tool again until it says the run is done.

`orca wait` prints one status line per run. It exits with 0 when every run is done, 1 if one failed, 2 if one was stopped, 3 if one was interrupted, 4 for a bad or unknown ID, and 124 if its `--timeout` ran out. A run keeps going if the waiter gives up.

## Defaults and modes

Each run has a mode: the worker CLI's own permission or sandbox setting. The agent that starts the run picks it. Orca passes the mode to the worker CLI, and the CLI enforces it. Orca adds no sandbox of its own.

The default is the safest mode each CLI has. A default that is too careful only costs a refused edit, and the agent can ask again with a stronger mode. A default that is too loose could cause real damage.

**Claude**

| | |
|---|---|
| Default mode | `dontAsk` (Claude can read, and anything that would need approval is refused) |
| Other modes | `plan`, `manual` (also called `default`), `acceptEdits`, `auto`, `bypassPermissions` |
| Pinned | `--permission-prompts none` |
| Why pinned | The worker runs with nobody watching, so nobody could answer a prompt. The mode alone decides what is allowed. |

**Codex**

| | |
|---|---|
| Default mode | `read-only` |
| Other modes | `workspace-write`, `danger-full-access` |
| Pinned | `approval_policy="never"` and `--skip-git-repo-check` |
| Why pinned | Nobody is there to approve a request for more access, so the worker never asks. Choose a stronger mode instead. Orca accepts any existing folder, not only Git repos. |

Agents can also pass a `model`, a reasoning `effort`, a `timeout_ms`, and free text `role` and `task` labels that show up in `status`.

## Where data lives and cleanup

Run records and answers are stored in `~/.local/state/orca`, or in `ORCA_HOME` if you set it. Only your user can read them.

Orca cleans up by itself. A run whose answer was read is deleted 24 hours later. A finished run nobody read is deleted after 7 days. A run that is still going is never deleted.

Orca finds `claude` and `codex` on your `PATH`, then in `~/.local/bin`, `/opt/homebrew/bin` and `/usr/local/bin`. To use a different binary, set it in `~/.config/orca/config.json` (or the file named by `ORCA_CONFIG`):

```json
{"workers": {"codex": {"binary": "/path/to/codex"}}}
```

## Limits, honestly

- Orca is not a sandbox. A worker can do whatever its mode allows, and a stronger mode allows more.
- Each run has a small Orca background process that watches the worker. If that process is killed, the worker may keep running, and the run shows as interrupted.
- The desktop apps are untested.
- Orca has no built-in way for agents to start agents. It does not give workers its own tools. A worker can use Orca only if you registered Orca for that CLI yourself, and that setup is untested.

## Planned

Planned: a Jev adapter.

## License

Apache-2.0. See [LICENSE](LICENSE).
