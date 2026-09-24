package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// invocation is what the fake understood from its argv. One type serves both
// dialects; fields a dialect does not use stay empty. It is written to the
// record file so tests can assert what AskOther passed.
type invocation struct {
	Resume         bool     `json:"resume"`
	ResumeID       string   `json:"resume_id,omitempty"`
	OutputFile     string   `json:"output_file,omitempty"`
	Sandbox        string   `json:"sandbox,omitempty"`
	Configs        []string `json:"configs,omitempty"`
	Model          string   `json:"model,omitempty"`
	Effort         string   `json:"effort,omitempty"`
	PermissionMode string   `json:"permission_mode,omitempty"`
	Flags          []string `json:"flags,omitempty"`
	Prompt         string   `json:"prompt_arg,omitempty"`
	format         string   // claude --output-format
}

func (in invocation) has(flag string) bool { return slices.Contains(in.Flags, flag) }

// argError is a command-line rejection, printed the way the real CLI would.
type argError string

func (e argError) Error() string { return string(e) }

func argErrorf(format string, a ...any) error { return argError(fmt.Sprintf(format, a...)) }

// grammar is the part of a CLI's command line the fake accepts. Anything else
// is rejected, so an adapter that passes a flag AskOther no longer uses fails.
type grammar struct {
	values     map[string]string // spelling -> long name, for options that take a value
	flags      []string
	unknownFmt string // how the CLI rejects an argument
	missingFmt string // how it rejects an option without its value
}

var (
	codexGrammar = grammar{
		values: map[string]string{"-o": "--output-last-message", "--output-last-message": "--output-last-message",
			"-s": "--sandbox", "--sandbox": "--sandbox", "-c": "--config", "--config": "--config",
			"-m": "--model", "--model": "--model"},
		flags:      []string{"--json", "--skip-git-repo-check"},
		unknownFmt: "unexpected argument '%s' found",
		missingFmt: "a value is required for '%s <VALUE>' but none was supplied",
	}
	claudeGrammar = grammar{
		values: map[string]string{"--output-format": "--output-format", "--permission-mode": "--permission-mode",
			"--permission-prompts": "--permission-prompts", "--resume": "--resume", "--model": "--model",
			"--effort": "--effort"},
		flags:      []string{"--print", "--verbose"},
		unknownFmt: "error: unknown option '%s'",
		missingFmt: "error: option '%s <value>' argument missing",
	}
	codexSandboxes = []string{"read-only", "workspace-write", "danger-full-access"}
	claudeModes    = []string{"acceptEdits", "auto", "bypassPermissions", "default", "dontAsk", "manual", "plan"}
)

// parse walks args. Flags are returned; each option (by long name) and each
// positional argument (name "") goes to apply, in order.
func (g grammar) parse(args []string, apply func(name, val string) error) ([]string, error) {
	var flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, inline := a, "", false
		if strings.HasPrefix(a, "--") {
			name, val, inline = strings.Cut(a, "=")
		}
		long, takesValue := g.values[name]
		var err error
		switch {
		case a == "-" || !strings.HasPrefix(a, "-"):
			err = apply("", a)
		case slices.Contains(g.flags, name) && !inline:
			flags = append(flags, name)
		case takesValue:
			if !inline {
				if i+1 >= len(args) {
					return nil, argErrorf(g.missingFmt, name)
				}
				i++
				val = args[i]
			}
			err = apply(long, val)
		default:
			err = argErrorf(g.unknownFmt, a)
		}
		if err != nil {
			return nil, err
		}
	}
	return flags, nil
}

// parseCodex understands `exec [opts] [resume [opts] <thread_id>] -`. Like
// real Codex, `resume` does not accept --sandbox; the mode must be given
// before `resume` or as `-c sandbox_mode=...`.
func parseCodex(args []string) (invocation, error) {
	var in invocation
	if len(args) == 0 || args[0] != "exec" {
		return in, argErrorf("the fake codex only implements 'codex exec'")
	}
	flags, err := codexGrammar.parse(args[1:], func(name, val string) error {
		switch name {
		case "":
			switch {
			case val == "resume" && !in.Resume && in.Prompt == "":
				in.Resume = true
			case in.Resume && in.ResumeID == "" && val != "-":
				in.ResumeID = val
			case in.Prompt == "":
				in.Prompt = val
			default:
				return argErrorf(codexGrammar.unknownFmt, val)
			}
		case "--output-last-message":
			in.OutputFile = val
		case "--sandbox":
			if in.Resume {
				return argErrorf(codexGrammar.unknownFmt, name)
			}
			if !slices.Contains(codexSandboxes, val) {
				return argErrorf("invalid value '%s' for '--sandbox <SANDBOX_MODE>'", val)
			}
			in.Sandbox = val
		case "--config":
			k, v, ok := strings.Cut(val, "=")
			if !ok || k == "" {
				return argErrorf("invalid value '%s' for '--config <key=value>': expected key=value", val)
			}
			in.Configs = append(in.Configs, val)
			if k == "model_reasoning_effort" {
				in.Effort, _ = strconv.Unquote(v)
			}
		case "--model":
			in.Model = val
		}
		return nil
	})
	in.Flags = flags
	return in, err
}

// parseClaude understands the headless Claude Code flags AskOther uses.
func parseClaude(args []string) (invocation, error) {
	var in invocation
	flags, err := claudeGrammar.parse(args, func(name, val string) error {
		switch name {
		case "":
			if in.Prompt != "" {
				return argErrorf("error: too many arguments")
			}
			in.Prompt = val
		case "--output-format":
			in.format = val
		case "--permission-mode":
			if !slices.Contains(claudeModes, val) {
				return argErrorf("error: option '--permission-mode <mode>' argument '%s' is invalid. Allowed choices are %s.",
					val, strings.Join(claudeModes, ", "))
			}
			in.PermissionMode = val
		case "--resume":
			in.Resume, in.ResumeID = true, val
		case "--model":
			in.Model = val
		case "--effort":
			in.Effort = val
		}
		return nil
	})
	in.Flags = flags
	if err == nil && in.has("--print") && in.format == "stream-json" && !in.has("--verbose") {
		err = argErrorf("Error: When using --print, --output-format=stream-json requires --verbose")
	}
	return in, err
}
