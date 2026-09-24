# AskOther usage and settings

## Set up by hand

AskOther runs on macOS and Linux and needs Go 1.26 or newer, plus the Claude Code or Codex CLI you want to use.

```sh
go install github.com/alex2481kobe/askother/cmd/askother@latest
askother help
```

`askother help` prints the setup lines below with the real path to your binary filled in. If `askother` is not on your `PATH`, run it from your Go bin directory.

**Claude Code**

```sh
claude mcp add -s user askother -- /path/to/askother mcp
```

**Codex, CLI and desktop app**, in `~/.codex/config.toml`:

```toml
[mcp_servers.askother]
command = "/path/to/askother"
args = ["mcp"]
default_tools_approval_mode = "approve"
```

Optionally copy `skills/askother` from this repo into `~/.claude/skills/` and `~/.codex/skills/`, so agents know when and how to use AskOther. Start a new agent session afterwards.

## Good to know

- AskOther uses the worker CLI's own permission mode. It is not a sandbox. The defaults are Claude Code `dontAsk` and Codex `read-only`.
- Tested: Claude Code CLI, Codex CLI, and the Codex desktop app. Claude Code inside the Claude desktop app is untested. Chat apps are not supported.
- Other CLI agents need an adapter in `internal/worker`. Claude Code and Codex are the only ones today; a Jev adapter is planned.
- The UI shows a worker's requested model when one was given. It does not guess the model a CLI picked by default.
- The Claude Code and Codex icons in the UI are Anthropic's and OpenAI's marks, used only to label which worker is which.

## Agent workflow

Register `askother mcp` once with each agent CLI that should use AskOther. The CLI starts its own MCP server for each session and closes it with that session. A worker can use AskOther only if AskOther is also registered in that worker's CLI.

The usual flow is `run`, `wait`, then `result`. AskOther saves a run's answer until it is read or cleaned up.

| Tool | What it does |
| --- | --- |
| `run` | Starts a Claude Code or Codex worker in the folder you choose. Returns a run ID and a `watch` command. |
| `wait` | Waits for one or more runs for up to about 50 seconds. Call it again if the run is still going. |
| `result` | Reads the finished answer and marks it as read. |
| `send` | Sends a follow-up to a finished worker session as a linked run. |
| `status` | Shows a run or lists runs, including whether a finished answer is unread. |
| `stop` | Requests a stop. AskOther gives the worker 5 seconds to exit, then forces it. |

Each `run` and `send` call needs a unique `key`. Retrying the same call with the same key returns the original run instead of starting a duplicate. Agents can pass a requested `model`, reasoning `effort`, `timeout_ms`, and free-text `role` and `task` labels. AskOther records role and task but does not interpret them.

Claude Code can run the returned `askother wait <id>` command in the background and wake when it exits. Codex can call the `wait` tool again. A wait timeout never stops the worker.

`askother wait` exits with:

| Code | Meaning |
| --- | --- |
| `0` | Every run finished successfully. |
| `1` | A run failed. |
| `2` | A run was stopped. |
| `3` | A run was interrupted. |
| `4` | A run ID was invalid or unknown. |
| `124` | The wait timeout expired. The runs keep going. |
| `130` | The wait was interrupted. The runs keep going. |

## Modes

AskOther passes the chosen mode to the worker CLI. The CLI enforces it; AskOther adds no sandbox of its own. A worker runs unattended, so approval prompts are disabled. Choose a stronger mode only when the task needs it.

| Worker | Default | Other modes |
| --- | --- | --- |
| Claude Code | `dontAsk` | `plan`, `manual` or `default`, `acceptEdits`, `auto`, `bypassPermissions` |
| Codex | `read-only` | `workspace-write`, `danger-full-access` |

Claude Code runs with `--permission-prompts none`. Codex runs with `approval_policy="never"` and `--skip-git-repo-check`. The Codex registration setting `default_tools_approval_mode = "approve"` lets the agent call AskOther without waiting for a person to approve each tool call.

## Local UI

Run `askother ui` to open the run tree. It listens only on a random loopback port while that command is running; Ctrl-C closes it. Its URL contains a one-time token. Drag to pan, scroll to zoom, and click a card for details.

The sidebar has one row per caller session, identified by its client and a short session ID. There can be several rows for the same client. The root card shows the caller; child cards show the workers it started. A Claude Code caller may start a Codex worker, and a Codex caller may start a Claude Code worker. Runs started by a worker appear below that worker.

Select a card to see its task, role, worker, requested model when given, mode, folder, elapsed time, and outcome. `Clear from view` hides a run in the UI; it does not delete the record or mark the answer as read. `Clear finished` hides all finished runs. The `result` tool is how an agent reads and acknowledges an answer.

## Files and cleanup

Run records and answers live in `~/.local/state/askother`, or in `ASKOTHER_HOME` if set. The directory is readable only by your user. Read runs become eligible for removal 24 hours after their first result read; unread finished runs after 7 days. A cleanup pass runs on the next `run` or `send` call, so expired records can remain while AskOther is idle. Running runs are never removed automatically.

AskOther looks for `claude` and `codex` on `PATH`, then in `~/.local/bin`, `/opt/homebrew/bin`, and `/usr/local/bin`. To point to another binary, use `~/.config/askother/config.json`, or set `ASKOTHER_CONFIG` to another config file:

```json
{"workers": {"codex": {"binary": "/path/to/codex"}}}
```

AskOther uses one small background supervisor for each active run. If that supervisor is killed, its worker may continue, and AskOther reports the run as interrupted rather than claiming it finished.
