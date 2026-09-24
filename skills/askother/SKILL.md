---
name: askother
description: Use when the user wants the other coding agent (Codex from Claude Code, or Claude Code from Codex) to review, check, research, or do part of the work, or asks for a second opinion from another agent.
---

# AskOther

AskOther lets you start the other agent as a worker and get its answer back.
Its MCP tools (`run`, `wait`, `result`, `send`, `status`, `stop`) carry the
details in their descriptions. If the tools are missing, run `askother help`
and follow its Setup section.

The flow is always `run`, then wait, then `result`. Claude Code: run the
returned `askother wait <id>` as a background command and continue when it
exits. Codex: call the `wait` tool again until it says the run is done.
