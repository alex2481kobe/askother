package worker

import (
	"slices"
	"strings"
)

// Codex is the adapter for `codex exec --json`. It is stateless.
type Codex struct{}

var codexModes = []string{"read-only", "workspace-write", "danger-full-access"}

func (Codex) Facts() Facts {
	return Facts{
		Name:        "codex",
		DefaultMode: "read-only",
		Modes:       slices.Clone(codexModes),
		Pinned: []string{
			`approval_policy="never" (headless: no human can approve an escalation; choose a stronger sandbox mode instead)`,
			`--skip-git-repo-check (AskOther accepts any existing directory)`,
		},
	}
}

// Build returns the argv; the prompt goes on stdin. It never passes
// --ephemeral, which prevents resume, or -C, because the run package sets
// cmd.Dir. Every Build error is a *Failure with code INVALID_INPUT.
func (Codex) Build(in Invocation) (Command, error) {
	if err := checkInvocation("codex", in, codexModes); err != nil {
		return Command{}, err
	}
	// Codex parses every -c value as TOML. Accepting only plain effort names
	// keeps the value a valid quoted string without any escaping.
	if strings.ContainsFunc(in.Effort, notEffortRune) {
		return Command{}, failf(codeInvalidInput, "codex: effort %q is not [A-Za-z0-9_-]+", in.Effort)
	}
	args := []string{"exec"}
	if in.ResumeID != "" {
		// resume rejects --sandbox and does not inherit the sandbox of the
		// session, so the mode is set again through -c.
		args = append(args, "resume", "--json", "-o", in.TempAnswer, "-c", `sandbox_mode="`+in.Mode+`"`)
	} else {
		args = append(args, "--json", "-o", in.TempAnswer, "--sandbox", in.Mode)
	}
	// Without approval_policy="never", a user config that allows automatic
	// approval can let a read-only run escape its sandbox through an
	// escalation that no event reports.
	args = append(args, "-c", `approval_policy="never"`, "--skip-git-repo-check")
	if in.Model != "" {
		args = append(args, "-m", in.Model)
	}
	if in.Effort != "" {
		args = append(args, "-c", `model_reasoning_effort="`+in.Effort+`"`)
	}
	if in.ResumeID != "" {
		args = append(args, in.ResumeID)
	}
	args = append(args, "-")
	return Command{Path: in.Binary, Args: args, Stdin: []byte(in.Prompt)}, nil
}

func notEffortRune(r rune) bool {
	return !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
}

// NewDecoder decodes one run of in: the answer is in.TempAnswer, and a resume
// must continue in.ResumeID.
func (Codex) NewDecoder(in Invocation) Decoder {
	return &codexDecoder{tempAnswer: in.TempAnswer, session: in.ResumeID}
}
