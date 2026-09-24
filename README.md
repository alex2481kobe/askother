<p align="center">
  <img src="assets/askother-mark.png" alt="AskOther" width="140" height="140" />
</p>

<h1 align="center">AskOther</h1>

<p align="center">
  <strong>Let Claude Code and Codex ask each other for help.</strong><br />
  A second opinion, a review, or a hand with the work, from the session you already use.
</p>

<p align="center">
  <a href="#get-started">Get started</a> ·
  <a href="#what-you-can-ask">What you can ask</a> ·
  <a href="USAGE.md">Usage and settings</a>
</p>

<img src="assets/ui-light.png" alt="The AskOther run tree in the browser, with sample data" width="912" />

AskOther is one small program. Your agent starts the other agent, waits for it to finish, and reads its answer, and you can follow up or stop it at any time. Run `askother ui` to watch every run in your browser.

## Get started

Tell Claude Code or Codex:

```text
Install AskOther from https://github.com/alex2481kobe/askother and set it up for Claude Code and Codex.
```

Or do it yourself: grab a [release](https://github.com/alex2481kobe/askother/releases) or `go install` it, then run `askother help`. Full steps are in [setup by hand](USAGE.md#set-up-by-hand).

## What you can ask

Start a new session, then just ask:

```text
Have Codex review my changes and tell me what it finds.
```

```text
Get a second opinion from Claude on this plan before we start.
```

```text
Have Codex write the tests for this while you keep going.
```

## Works with

Claude Code and Codex on macOS and Linux, in the terminal and in their desktop apps (the `claude` and `codex` command line tools need to be installed). On Windows, use WSL. Other CLI agents can be added with an adapter, and a Jev adapter is planned.

Apache-2.0. The Claude Code and Codex icons in the UI belong to Anthropic and OpenAI and only label which worker is which.
