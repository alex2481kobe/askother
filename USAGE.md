# Orca usage and settings

## Agent workflow

Register `orca mcp` once with each agent CLI that should use Orca. The CLI starts its own MCP server for each session and closes it with that session. A worker can use Orca only if Orca is also registered in that worker's CLI.

The usual flow is `run`, `wait`, then `result`. Orca saves a run's answer until it is read or cleaned up.

| Tool | What it does |
| --- | --- |
| `run` | Starts a Claude Code or Codex worker in the folder you choose. Returns a run ID and a `watch` command. |
| `wait` | Waits for one or more runs for up to about 50 seconds. Call it again if the run is still going. |
| `result` | Reads the finished answer and marks it as read. |
| `send` | Sends a follow-up to a finished worker session as a linked run. |
| `status` | Shows a run or lists runs, including whether a finished answer is unread. |
| `stop` | Requests a stop. Orca gives the worker 5 seconds to exit, then forces it. |

Each `run` and `send` call needs a unique `key`. Retrying the same call with the same key returns the original run instead of starting a duplicate. Agents can pass a requested `model`, reasoning `effort`, `timeout_ms`, and free-text `role` and `task` labels. Orca records role and task but does not interpret them.

Claude Code can run the returned `orca wait <id>` command in the background and wake when it exits. Codex can call the `wait` tool again. A wait timeout never stops the worker.

`orca wait` exits with:

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

Orca passes the chosen mode to the worker CLI. The CLI enforces it; Orca adds no sandbox of its own. A worker runs unattended, so approval prompts are disabled. Choose a stronger mode only when the task needs it.

| Worker | Default | Other modes |
| --- | --- | --- |
| Claude Code | `dontAsk` | `plan`, `manual` or `default`, `acceptEdits`, `auto`, `bypassPermissions` |
| Codex | `read-only` | `workspace-write`, `danger-full-access` |

Claude Code runs with `--permission-prompts none`. Codex runs with `approval_policy="never"` and `--skip-git-repo-check`. The Codex registration setting `default_tools_approval_mode = "approve"` lets the agent call Orca without waiting for a person to approve each tool call.

## Local UI

Run `orca ui` to open the run tree. It listens only on a random loopback port while that command is running; Ctrl-C closes it. Its URL contains a one-time token.

The sidebar has one row per caller session, identified by its client and a short session ID. There can be several rows for the same client. The root card shows the caller; child cards show the workers it started. A Claude Code caller may start a Codex worker, and a Codex caller may start a Claude Code worker. Runs started by a worker appear below that worker.

Select a card to see its task, role, worker, requested model when given, mode, folder, elapsed time, and outcome. `Clear from view` hides a run in the UI; it does not delete the record or mark the answer as read. `Clear finished` hides all finished runs. The `result` tool is how an agent reads and acknowledges an answer.

## Files and cleanup

Run records and answers live in `~/.local/state/orca`, or in `ORCA_HOME` if set. The directory is readable only by your user. Read runs become eligible for removal 24 hours after their first result read; unread finished runs after 7 days. A cleanup pass runs on the next `run` or `send` call, so expired records can remain while Orca is idle. Running runs are never removed automatically.

Orca looks for `claude` and `codex` on `PATH`, then in `~/.local/bin`, `/opt/homebrew/bin`, and `/usr/local/bin`. To point to another binary, use `~/.config/orca/config.json`, or set `ORCA_CONFIG` to another config file:

```json
{"workers": {"codex": {"binary": "/path/to/codex"}}}
```

Orca uses one small background supervisor for each active run. If that supervisor is killed, its worker may continue, and Orca reports the run as interrupted rather than claiming it finished.
