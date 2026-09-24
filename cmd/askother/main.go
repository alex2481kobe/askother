// Command askother runs coding-agent CLIs as detached, supervised runs and serves
// them to agents over MCP.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// Exit codes owned by the command itself; run outcomes use run.ExitCode.
const (
	exitRefused  = 4
	exitInternal = 5
)

// version is the module version for `go install ...@vX`, else "dev".
func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitRefused
	}
	// `askother <command> --help` shows the help instead of running the command.
	for _, a := range args[1:] {
		if a == "--help" || a == "-h" {
			return cmdHelp(stdout, stderr)
		}
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "mcp":
		return cmdMCP(rest, stderr)
	case "supervise":
		return cmdSupervise(rest)
	case "wait":
		return cmdWait(rest, stdout, stderr)
	case "ui":
		return cmdUI(rest, stdout, stderr)
	case "help", "--help", "-h":
		return cmdHelp(stdout, stderr)
	case "version", "--version":
		fmt.Fprintln(stdout, "askother", version())
		return 0
	default:
		fmt.Fprintf(stderr, "askother: unknown command %q\n\n%s", cmd, usage)
		return exitRefused
	}
}

// setupFailed reports a Deps error (bad ASKOTHER_HOME or ASKOTHER_CONFIG) and
// returns the refused exit code.
func setupFailed(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "askother: %v\n", err)
	return exitRefused
}
