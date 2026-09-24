# AskOther

<p align="center"><img src="assets/askother-mark.png" alt="AskOther logo" width="160" height="160"></p>

AskOther lets Claude Code and Codex call each other while you stay in the agent session you already use. Ask the other agent for a second opinion, a review, or implementation help; wait for its answer; and send a follow-up when it finishes. It is one small Go binary with six tools and no shared server to keep running.

![AskOther run tree with sample data](assets/ui-light.png)

*The run tree shown with sample data.*

## Get started

AskOther supports macOS and Linux. Install Go 1.26 or newer, plus the Claude Code or Codex CLI you want to run as a worker.

```sh
go install github.com/alex2481kobe/askother/cmd/askother@latest
```

Run `askother help` to check the install. It also prints setup commands with the path to your binary. If `askother` is not on your `PATH`, run it from your Go bin directory.

Register AskOther with the agent you use:

**Claude Code**

```sh
claude mcp add -s user askother -- /path/to/askother mcp
```

**Codex CLI and desktop app**, in `~/.codex/config.toml`:

```toml
[mcp_servers.askother]
command = "/path/to/askother"
args = ["mcp"]
default_tools_approval_mode = "approve"
```

Replace `/path/to/askother` with the path printed by `askother help`, then start a new agent session.

## Use it

Ask your agent, for example:

> Use AskOther to ask a Codex worker to review this change. Wait for it, read its answer, and tell me what it found.

AskOther provides six tools: `run`, `wait`, `result`, `send`, `status`, and `stop`. Workers can call AskOther too when it is registered in their CLI.

Run `askother ui` to open the local run tree. Each sidebar row is one caller session, so you can have several Codex or Claude Code sessions. The root shows who started the work; cards beneath it show the workers, which can be either CLI. Drag to pan, scroll to zoom, and click a card for details.

## Good to know

- AskOther uses the worker CLI's permission mode. It is not a sandbox. The defaults are Claude Code `dontAsk` and Codex `read-only`.
- The UI shows a worker's requested model when one was specified. It does not guess the model a CLI chose by default.
- Run records live in `~/.local/state/askother`. Read results are eligible for cleanup after 24 hours; unread finished runs after 7 days. Cleanup runs on a later `run` or `send` call.
- The Claude Code and Codex icons in the UI are Anthropic's and OpenAI's marks, used only to label which worker is which.
- Codex desktop on macOS passed a small MCP test: start a Codex CLI worker, wait, and read its answer. Claude Code inside the Claude desktop app is untested. General chat apps are not supported.

See [Usage and settings](USAGE.md) for tool details, modes, configuration, and cleanup. A Jev adapter is planned.

Apache-2.0. See [LICENSE](LICENSE).
