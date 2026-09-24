package main

import (
	"slices"
	"strings"
	"testing"
)

// AskOther's Codex argv, initial and resume, with every optional part.
func TestParseCodex(t *testing.T) {
	args := []string{"exec", "--json", "-o", "/tmp/ans.tmp", "--sandbox", "workspace-write",
		"-c", `approval_policy="never"`, "--skip-git-repo-check", "-m", "fake-model",
		"-c", `model_reasoning_effort="low"`, "-"}
	in, err := parseCodex(args)
	if err != nil {
		t.Fatal(err)
	}
	if in.Resume || in.OutputFile != "/tmp/ans.tmp" || in.Sandbox != "workspace-write" || in.Model != "fake-model" ||
		in.Effort != "low" || in.Prompt != "-" || len(in.Configs) != 2 || !in.has("--json") || !in.has("--skip-git-repo-check") {
		t.Fatalf("parsed %+v", in)
	}
	in, err = parseCodex(codexResumeArgv("/tmp/ans.tmp", testThread))
	if err != nil || !in.Resume || in.ResumeID != testThread || in.Prompt != "-" || in.Sandbox != "" ||
		!slices.Contains(in.Configs, `sandbox_mode="read-only"`) {
		t.Fatalf("resume parsed %+v, %v", in, err)
	}
}

// Real Codex rejects --sandbox after `resume` but accepts it before.
func TestParseCodexSandboxPlacement(t *testing.T) {
	for _, flag := range []string{"--sandbox", "-s", "--sandbox=read-only"} {
		args := []string{"exec", "resume", "--json", flag}
		if !strings.Contains(flag, "=") {
			args = append(args, "read-only")
		}
		_, err := parseCodex(append(args, testThread, "-"))
		if err == nil || !strings.Contains(err.Error(), "unexpected argument '--sandbox' found") {
			t.Fatalf("%s after resume: err = %v", flag, err)
		}
	}
	in, err := parseCodex([]string{"exec", "--sandbox", "read-only", "resume", "--json", testThread, "-"})
	if err != nil || !in.Resume || in.Sandbox != "read-only" || in.ResumeID != testThread {
		t.Fatalf("parent-level sandbox: %+v, %v", in, err)
	}
}

// AskOther's Claude argv, initial and resume. Flags AskOther no longer passes are
// rejected, so an adapter that still sends them fails loudly.
func TestParseClaude(t *testing.T) {
	in, err := parseClaude(claudeArgv("--model", "fake-model", "--effort", "low"))
	if err != nil || in.Resume || in.PermissionMode != "dontAsk" || in.format != "stream-json" ||
		in.Model != "fake-model" || in.Effort != "low" || !slices.Equal(in.Flags, []string{"--print", "--verbose"}) {
		t.Fatalf("parsed %+v, %v", in, err)
	}
	in, err = parseClaude(claudeArgv("--resume", testSession))
	if err != nil || !in.Resume || in.ResumeID != testSession {
		t.Fatalf("resume parsed %+v, %v", in, err)
	}
}

func TestParseRejects(t *testing.T) {
	codex := map[string][]string{
		"not exec":      {"review", "-"},
		"version":       {"--version"},
		"bad sandbox":   {"exec", "--sandbox", "open", "-"},
		"missing value": {"exec", "-o"},
		"unknown flag":  {"exec", "--frobnicate", "-"},
		"bad config":    {"exec", "-c", "novalue", "-"},
		"two prompts":   {"exec", "a", "b"},
		"flag with =":   {"exec", "--json=yes", "-"},
	}
	for name, args := range codex {
		if _, err := parseCodex(args); err == nil {
			t.Errorf("codex %s: %v accepted", name, args)
		}
	}
	claude := map[string][]string{
		"session id":        claudeArgv("--session-id", testSession),
		"mcp config":        claudeArgv("--mcp-config", "{}"),
		"version":           {"--version"},
		"bad mode":          {"--print", "--permission-mode", "yolo"},
		"stream no verbose": {"--print", "--output-format", "stream-json"},
		"missing value":     {"--print", "--model"},
		"two prompts":       {"--print", "a", "b"},
	}
	for name, args := range claude {
		if _, err := parseClaude(args); err == nil {
			t.Errorf("claude %s: %v accepted", name, args)
		}
	}
}
