package testutil

import (
	"encoding/json"
	"os"
	"testing"
)

// FakeRecord is the JSON the fake worker writes to $ASKOTHER_FAKE_RECORD. It is
// rewritten as the scenario proceeds; read it after the fake exits.
type FakeRecord struct {
	Dialect       string         `json:"dialect"`
	Scenario      string         `json:"scenario"`
	Arg           string         `json:"arg"`
	Argv          []string       `json:"argv"`
	Cwd           string         `json:"cwd"`
	EnvNames      []string       `json:"env_names"` // names only, sorted
	Stdin         string         `json:"stdin"`
	PID           int            `json:"pid"`
	PGID          int            `json:"pgid"`
	Invocation    FakeInvocation `json:"invocation"`
	SessionID     string         `json:"session_id"` // thread_id or session_id the fake reported
	Answer        *FakeAnswer    `json:"answer"`     // the final answer it emitted, if any
	GrandchildPID int            `json:"grandchild_pid"`
	SplitWrites   int            `json:"split_writes"`        // split_utf8: writes ending mid-codepoint
	WriteErrors   int            `json:"stdout_write_errors"` // epipe_tolerant: failed stdout writes
}

// FakeInvocation is what the fake parsed from its argv.
type FakeInvocation struct {
	Resume         bool     `json:"resume"`
	ResumeID       string   `json:"resume_id"`   // codex thread_id or claude --resume
	OutputFile     string   `json:"output_file"` // codex -o
	Sandbox        string   `json:"sandbox"`     // codex --sandbox (not -c sandbox_mode)
	Configs        []string `json:"configs"`     // codex -c values, verbatim
	Model          string   `json:"model"`
	Effort         string   `json:"effort"` // claude --effort or codex -c model_reasoning_effort, unquoted
	PermissionMode string   `json:"permission_mode"`
	Flags          []string `json:"flags"`
	Prompt         string   `json:"prompt_arg"`
}

// FakeAnswer identifies the final answer without carrying it.
type FakeAnswer struct {
	Bytes  int    `json:"bytes"`
	Runes  int    `json:"runes"`
	SHA256 string `json:"sha256"`
}

// ReadFakeRecord decodes the record the fake wrote to path.
func ReadFakeRecord(t testing.TB, path string) FakeRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fake record: %v", err)
	}
	var r FakeRecord
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("decode fake record: %v", err)
	}
	return r
}
