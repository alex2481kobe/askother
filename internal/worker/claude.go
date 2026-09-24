package worker

import "slices"

// Claude is the adapter for Claude Code in headless print mode. It is
// stateless.
type Claude struct{}

// claudeModes: Claude reports manual as default, and has been seen to run
// auto as default. plan can ignore --model and write under ~/.claude/plans.
var claudeModes = []string{"dontAsk", "plan", "manual", "default", "acceptEdits", "auto", "bypassPermissions"}

func (Claude) Facts() Facts {
	return Facts{
		Name:        "claude",
		DefaultMode: "dontAsk",
		Modes:       slices.Clone(claudeModes),
		Pinned:      []string{"--permission-prompts none (headless: nobody can answer a prompt; the mode decides)"},
	}
}

// Build returns the argv; the prompt goes on stdin. A new session gets its
// id from Claude, reported in system/init. A resume passes in.ResumeID.
// Every Build error is a *Failure with code INVALID_INPUT.
func (Claude) Build(in Invocation) (Command, error) {
	if err := checkInvocation("claude", in, claudeModes); err != nil {
		return Command{}, err
	}
	args := []string{"--print", "--output-format", "stream-json", "--verbose",
		"--permission-mode", in.Mode, "--permission-prompts", "none"}
	if in.ResumeID != "" {
		args = append(args, "--resume", in.ResumeID)
	}
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	if in.Effort != "" {
		args = append(args, "--effort", in.Effort)
	}
	return Command{Path: in.Binary, Args: args, Stdin: []byte(in.Prompt)}, nil
}

// NewDecoder decodes one run of in: the answer is written to in.TempAnswer,
// and on a resume system/init must report in.ResumeID.
func (Claude) NewDecoder(in Invocation) Decoder {
	return &claudeDecoder{tempAnswer: in.TempAnswer, session: in.ResumeID}
}
