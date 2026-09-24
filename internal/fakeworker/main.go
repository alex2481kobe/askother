// Command fakeworker stands in for the codex and claude CLIs so Orca can be
// tested without models or credentials. It parses the argv shapes Orca uses,
// reads the prompt from stdin, and plays a scripted scenario whose JSONL events
// follow the shapes the real CLIs emit.
//
// The basename of argv[0] ("codex" or "claude") picks the dialect. The
// environment selects the rest:
//
//	ORCA_FAKE_SCENARIO  scenario name (default "ok"); see scenarios.go
//	ORCA_FAKE_ARG       optional scenario argument
//	ORCA_FAKE_RECORD    path of a JSON file recording argv, cwd, env names and stdin
//
// Exit 64 means the fake itself was misused (unknown scenario, unsupported mode).
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	envScenario = "ORCA_FAKE_SCENARIO"
	envArg      = "ORCA_FAKE_ARG"
	envRecord   = "ORCA_FAKE_RECORD"
	envChild    = "ORCA_FAKE_CHILD" // internal: run as a sleeping grandchild
	exitMisuse  = 64
)

func main() {
	registerLive()
	if d, ok := os.LookupEnv(envChild); ok {
		time.Sleep(parseDur(d, 30*time.Second))
		return
	}
	os.Exit(run(os.Args))
}

func run(argv []string) int {
	dialect := pickDialect(argv[0])
	if dialect == "" {
		return misuse("cannot tell the dialect; name the binary codex or claude")
	}
	in, err := parse(dialect, argv[1:])
	if err != nil {
		return reject(dialect, err)
	}
	if code := checkSupported(dialect, in); code != 0 {
		return code
	}
	var stdin []byte
	if in.Prompt == "" || in.Prompt == "-" {
		if stdin, err = io.ReadAll(os.Stdin); err != nil {
			return misuse("read stdin: " + err.Error())
		}
	}
	scenario := os.Getenv(envScenario)
	if scenario == "" {
		scenario = "ok"
	}
	rec := newRecord(dialect, scenario, argv, in, stdin)
	rec.save()
	prompt := string(stdin)
	if in.Prompt != "" && in.Prompt != "-" {
		prompt = in.Prompt
	}
	if strings.TrimSpace(prompt) == "" {
		fmt.Fprintln(os.Stderr, "Error: no prompt provided")
		return 1
	}
	// A resume continues the given session unless the scenario says otherwise.
	session := in.ResumeID
	if session == "" || scenario == "resume_new_thread" {
		session = newUUID()
	}
	e := &emitter{w: os.Stdout}
	var w worker
	if dialect == "codex" {
		w = &codex{e: e, in: in, rec: rec, thread: session}
	} else {
		w = &claude{e: e, in: in, rec: rec, session: session, denials: []any{}}
	}
	return play(scenario, os.Getenv(envArg), w, e, rec)
}

func pickDialect(argv0 string) string {
	base := filepath.Base(argv0)
	switch {
	case strings.HasPrefix(base, "codex"):
		return "codex"
	case strings.HasPrefix(base, "claude"):
		return "claude"
	}
	return ""
}

func parse(dialect string, args []string) (invocation, error) {
	if dialect == "codex" {
		return parseCodex(args)
	}
	return parseClaude(args)
}

// reject prints a parse error the way each CLI does: Codex prefixes "error: "
// and exits 2; Claude prints the message and exits 1.
func reject(dialect string, err error) int {
	if dialect == "codex" {
		fmt.Fprintf(os.Stderr, "error: %s\n\nFor more information, try '--help'.\n", err)
		return 2
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}

// checkSupported refuses valid real-CLI usage that the fake does not emulate.
func checkSupported(dialect string, in invocation) int {
	if dialect == "codex" && !in.has("--json") {
		return misuse("the fake codex only emits --json output")
	}
	if dialect == "claude" && (!in.has("--print") || in.format != "stream-json") {
		return misuse("the fake claude only emits --print --output-format stream-json")
	}
	return 0
}

func misuse(msg string) int {
	fmt.Fprintf(os.Stderr, "fakeworker: %s\n", msg)
	return exitMisuse
}
