// Package run owns the run record, its files and the worker process
// mechanics.
//
// JSON convention: an optional timestamp or number is omitted when unknown;
// a value readers must always be able to test (previous_id, exit, error,
// native_session_id, stop_reason) is always present and null when unknown.
// Unknown values are never written as invented zeroes.
package run

import (
	"time"

	"github.com/alex2481kobe/orca/internal/worker"
)

// SchemaVersion is the only record version this build reads or writes.
const SchemaVersion = 1

// State is the run lifecycle state.
type State string

const (
	StateStarting    State = "starting"
	StateRunning     State = "running"
	StateStopping    State = "stopping"
	StateDone        State = "done"
	StateFailed      State = "failed"
	StateStopped     State = "stopped"
	StateInterrupted State = "interrupted"
)

// Execution is what Orca knows about the worker process itself.
type Execution string

const (
	ExecNotStarted Execution = "not_started"
	ExecLive       Execution = "live"
	ExecExited     Execution = "exited"
	ExecUnknown    Execution = "unknown"
)

// CallerSource says where caller_id came from: the Claude Code session, the
// Codex thread, or the MCP server's own per-process id.
type CallerSource string

const (
	SourceClaude   CallerSource = "claude"
	SourceCodex    CallerSource = "codex"
	SourceFallback CallerSource = "fallback"
)

// StopReason says who asked for a stop.
type StopReason string

const (
	StopCaller  StopReason = "caller"
	StopTimeout StopReason = "timeout"
)

// Run is the record stored in runs/<id>.json.
type Run struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Key           string `json:"key"`
	RequestSHA256 string `json:"request_sha256"`

	CallerID     string       `json:"caller_id"`
	CallerSource CallerSource `json:"caller_source"`
	PreviousID   *string      `json:"previous_id"`

	Request Request `json:"request"`

	State     State     `json:"state"`
	Execution Execution `json:"execution"`

	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	DurationMS *int64     `json:"duration_ms,omitempty"`

	Runtime         Runtime `json:"runtime"`
	NativeSessionID *string `json:"native_session_id"`

	Exit   *Exit           `json:"exit"`
	Result Result          `json:"result"`
	Error  *worker.Failure `json:"error"`

	StderrTail StderrTail `json:"stderr_tail"`
	// Notes is omitted when empty: no notes is the absence of notes.
	Notes []string `json:"notes,omitempty"`

	StopRequestedAt *time.Time  `json:"stop_requested_at,omitempty"`
	StopReason      *StopReason `json:"stop_reason"`
	FirstReadAt     *time.Time  `json:"first_read_at,omitempty"`
}

// Request is the normalized request. Mode is the mode Orca passed, so it is
// always set; the other optional strings are empty when absent.
type Request struct {
	Worker    string `json:"worker"`
	Prompt    string `json:"prompt"`
	CWD       string `json:"cwd"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Mode      string `json:"mode"`
	Role      string `json:"role,omitempty"`
	Task      string `json:"task,omitempty"`
	TimeoutMS *int64 `json:"timeout_ms,omitempty"`
}

// Runtime is launch evidence, diagnostic only: the pids are never used to
// signal a worker after its supervisor is gone, because they may be reused.
type Runtime struct {
	Binary        string `json:"binary"`
	SupervisorPID *int   `json:"supervisor_pid,omitempty"`
	WorkerPID     *int   `json:"worker_pid,omitempty"`
	WorkerPGID    *int   `json:"worker_pgid,omitempty"`
}

// Exit is the persisted worker exit.
type Exit struct {
	Code   *int    `json:"code"`
	Signal *string `json:"signal"`
}

// ExitOf maps an observed worker exit to the persisted form (Signal "" is null).
func ExitOf(e worker.Exit) *Exit {
	out := &Exit{Code: e.Code}
	if e.Signal != "" {
		s := e.Signal
		out.Signal = &s
	}
	return out
}

// Result describes the published answer in runs/<id>.txt.
type Result struct {
	Available bool  `json:"available"`
	Bytes     int64 `json:"bytes"`
}

// StderrTail is the last 8 KiB of worker stderr, cut on a UTF-8 boundary.
type StderrTail struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}
