# AskOther

<p align="center"><img src="assets/askother-mark.png" alt="AskOther logo" width="160" height="160"></p>

Let Claude Code and Codex ask each other for help, right from the agent session you already use. Ask the other agent for a second opinion, a review, or a hand with the work, and get its answer back in your session.

![AskOther run tree with sample data](assets/ui-light.png)

## Set it up

Paste this to Claude Code or Codex:

```text
Set up AskOther for me (https://github.com/alex2481kobe/askother).
1. Install it: go install github.com/alex2481kobe/askother/cmd/askother@latest
2. Run `askother help` and follow its Setup section to register AskOther
   with Claude Code and Codex, whichever of them I have installed.
3. Tell me to start a new session when you are done.
```

It needs Go 1.26 or newer.

## Use it

In a new session, just ask. For example:

> Ask Codex to review this change and tell me what it finds.

Run `askother ui` to watch your runs in the browser.

## Works with

Claude Code and Codex, in the terminal and in the Codex desktop app, on macOS and Linux. Other CLI agents can be added by writing an adapter. A Jev adapter is planned.

Setup by hand, tools, modes and files: [USAGE.md](USAGE.md). License: Apache-2.0.
