package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/alex2481kobe/askother/internal/tools"
	"github.com/alex2481kobe/askother/internal/worker"
)

const usage = `Usage:
  askother mcp                                  stdio MCP server; a harness starts it (see Setup)
  askother wait <id>... [--any] [--timeout D]   block until the runs end; one status line per run
  askother ui                                   open the local run viewer until Ctrl-C
  askother help                                 this help
  askother version                              print the version

askother wait exits 0 when every run is done, 1 if any failed, 2 if any was
stopped, 3 if any was interrupted (precedence 3 > 1 > 2), 4 for a bad or
unknown id, 124 when --timeout expires and 130 when interrupted. Runs keep
going when a waiter gives up.
`

// cmdHelp prints usage, the worker defaults and the registration lines for
// this binary.
func cmdHelp(stdout, stderr io.Writer) int {
	self, err := selfPath()
	if err != nil {
		fmt.Fprintf(stderr, "askother: %v\n", err)
		return exitInternal
	}
	renderHelp(stdout, tools.Facts(adapters()), self)
	return 0
}

// renderHelp writes the help text. Every default comes from facts; nothing
// about a worker is written by hand here, so it cannot drift from the
// adapters.
func renderHelp(w io.Writer, facts []worker.Facts, self string) {
	fmt.Fprint(w, usage)
	fmt.Fprint(w, "\nDefaults (the run tool shows the same)\n")
	for _, f := range facts {
		fmt.Fprintf(w, "  %s\n", f.Name)
		fmt.Fprintf(w, "    default mode  %s\n", f.DefaultMode)
		fmt.Fprintf(w, "    modes         %s\n", strings.Join(f.Modes, ", "))
		for i, p := range f.Pinned {
			label := ""
			if i == 0 {
				label = "pinned"
			}
			fmt.Fprintf(w, "    %-13s %s\n", label, p)
		}
	}
	fmt.Fprintf(w, `
Setup (once per harness)
  Claude Code:
    claude mcp add -s user askother -- %s mcp
  Codex (CLI and app), in ~/.codex/config.toml:
    [mcp_servers.askother]
    command = %s
    args = ["mcp"]
    default_tools_approval_mode = "approve"
`, shellQuote(self), strconv.Quote(self))
}

// shellQuote leaves plain paths alone and single-quotes anything else.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+@%:,=") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
