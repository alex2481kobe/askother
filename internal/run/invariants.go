package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"
)

// IsTerminal reports whether a run in state s has ended. interrupted is
// terminal, but it is only ever a view derived on read (see Validate).
func IsTerminal(s State) bool {
	switch s {
	case StateDone, StateFailed, StateStopped, StateInterrupted:
		return true
	}
	return false
}

func knownState(s State) bool {
	switch s {
	case StateStarting, StateRunning, StateStopping:
		return true
	}
	return IsTerminal(s)
}

func knownExecution(e Execution) bool {
	switch e {
	case ExecNotStarted, ExecLive, ExecExited, ExecUnknown:
		return true
	}
	return false
}

// Validate checks the rules every stored record keeps. It returns a
// CORRUPT_STATE *Error naming the first rule r breaks. interrupted is never
// stored: readers derive it from a free run lock, so a stored one is corrupt.
func (r *Run) Validate() error {
	bad := func(format string, args ...any) error {
		return Errorf(CodeCorruptState, "run %q: "+format, append([]any{r.ID}, args...)...)
	}
	if r.SchemaVersion != SchemaVersion {
		return bad("schema_version %d, want %d", r.SchemaVersion, SchemaVersion)
	}
	if !knownState(r.State) {
		return bad("unknown state %q", r.State)
	}
	if !knownExecution(r.Execution) {
		return bad("unknown execution %q", r.Execution)
	}
	if r.State == StateInterrupted {
		return bad("interrupted is derived on read and never stored")
	}
	if IsTerminal(r.State) && r.EndedAt == nil {
		return bad("state %s without ended_at", r.State)
	}
	if r.State == StateDone {
		// done needs a published answer of result.bytes bytes (zero is
		// allowed) from a worker that exited 0.
		if !r.Result.Available || r.Result.Bytes < 0 {
			return bad("done without an available result")
		}
		if r.Execution != ExecExited {
			return bad("done with execution %q, want exited", r.Execution)
		}
		if r.Exit == nil || r.Exit.Code == nil || *r.Exit.Code != 0 {
			return bad("done without exit code 0")
		}
	}
	return nil
}

// RequestHash returns the lowercase hex sha256 of the canonical encoding of
// req, used to tell a retry of a key from a different request under it.
//
// Canonical encoding: compact JSON of one object whose keys appear in the
// fixed order worker, prompt, cwd, model, effort, mode, role, task,
// timeout_ms. Normalized: the order and whitespace of whatever JSON req came
// from (req is hashed as a struct, never as raw bytes); an empty optional
// string (model, effort, role, task) and a nil timeout_ms are omitted, so
// absent and "" hash the same. Not normalized: string contents are hashed
// byte for byte (no trimming, case folding or path cleaning), and an explicit
// timeout_ms of 0 differs from an absent one. Callers hash after defaults are
// applied, so mode is the mode AskOther will pass. Strings must be valid UTF-8;
// otherwise the JSON encoder would replace bytes and distinct requests could
// collide, so RequestHash returns INVALID_INPUT instead.
func RequestHash(req Request) (string, error) {
	for _, s := range []string{req.Worker, req.Prompt, req.CWD, req.Model, req.Effort, req.Mode, req.Role, req.Task} {
		if !utf8.ValidString(s) {
			return "", Errorf(CodeInvalidInput, "request contains invalid UTF-8")
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return "", Errorf(CodeInvalidInput, "encode request: %v", err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:]), nil
}

// ExitCode maps the outcomes of the selected runs to an `askother wait` exit
// code with precedence 3 > 1 > 2 > 0. states[i] and execs[i] describe run i.
//   - 3: any interrupted run, or any terminal run whose execution is unknown
//   - 1: any failed; 2: any stopped; 0: every run done
//   - 5: a run that has not ended and nothing above applies (no trustworthy
//     "all done"), mismatched slice lengths, or an unknown state
//   - 4: no runs selected
//
// 124 and 130 belong to the waiter, not to outcomes, and are never returned.
func ExitCode(states []State, execs []Execution) int {
	if len(states) != len(execs) {
		return 5
	}
	if len(states) == 0 {
		return 4
	}
	var interrupted, failed, stopped, pending bool
	for i, s := range states {
		switch {
		case s == StateInterrupted, IsTerminal(s) && execs[i] == ExecUnknown:
			interrupted = true
		case s == StateFailed:
			failed = true
		case s == StateStopped:
			stopped = true
		case s == StateDone:
		default:
			pending = true
		}
	}
	switch {
	case interrupted:
		return 3
	case failed:
		return 1
	case stopped:
		return 2
	case pending:
		return 5
	}
	return 0
}
