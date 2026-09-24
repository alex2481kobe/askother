# Security Policy

## Reporting a vulnerability

Please report security issues privately. Do not open a public issue.

Use the **Report a vulnerability** button on the
[Security tab](https://github.com/alex2481kobe/askother/security/advisories/new).
We will confirm we got the report, work with you on a fix, and agree on when to
make it public.

Helpful details:

- the AskOther version (`askother version`) or commit you built from
- your operating system and CPU type
- your Go version, and the Claude Code or Codex version involved
- the tool or command you used, and the smallest steps that show the problem
- what you expected to happen, and what happened instead

Do not include API keys, tokens, private host names or unredacted logs.

## Supported versions

Security fixes go into the next release. Please check the latest release or
`main` before reporting.

## What AskOther protects, and what it does not

AskOther starts agent CLIs (Claude Code and Codex) and records their results. It is
**not a security boundary** and adds no sandbox of its own.

- **Modes are passed through.** The mode an agent picks goes straight to the
  worker CLI, and that CLI enforces it. If the CLI allows something in that
  mode, AskOther does not stop it. The default mode is the most careful one each
  CLI offers.
- **Worker environments use an allowlist.** A worker gets only a short list of
  environment variables (such as `PATH`, `HOME`, `USER`, `SHELL`, `LANG`,
  `TMPDIR`, `TERM`, `LC_*` and `ASKOTHER_HOME`). Everything else is dropped,
  including the calling agent's own session variables and tokens.
- **Local state is private.** Run records and answers live in
  `~/.local/state/askother` (or `ASKOTHER_HOME`). The folder is created with mode
  `0700` and the files with mode `0600`, so only your user can read them.
- **Nothing listens on the network.** AskOther has no server and no open port.
  Agents reach it over standard input and output.
- **Anyone who can run programs as your user can use AskOther.** It does not check
  who is calling it.

## Repository safety

- Continuous integration uses read-only permissions and no secrets.
- Workflows on outside pull requests run only after a maintainer approves them.
- Changes to `.github/`, `go.mod`, this policy, the license or the
  contribution guide need owner review.
