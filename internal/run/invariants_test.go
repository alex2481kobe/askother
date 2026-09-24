package run

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ended returns a record in a terminal shape that Validate must accept.
func ended(s State, e Execution) *Run {
	r := minimalRun()
	r.State, r.Execution = s, e
	r.EndedAt = ptr(t0.Add(time.Minute))
	return r
}

func TestValidateAcceptsLegalShapes(t *testing.T) {
	done := ended(StateDone, ExecExited)
	done.Exit = &Exit{Code: ptr(0)}
	done.Result = Result{Available: true, Bytes: 12}
	emptyDone := ended(StateDone, ExecExited)
	emptyDone.Exit = &Exit{Code: ptr(0)}
	emptyDone.Result = Result{Available: true, Bytes: 0}
	failed := ended(StateFailed, ExecExited)
	failed.Exit = &Exit{Code: ptr(1)}
	stopped := ended(StateStopped, ExecExited)
	stopped.Exit = &Exit{Signal: ptr("SIGTERM")}
	cases := map[string]*Run{
		"starting":            minimalRun(),
		"done":                done,
		"done empty answer":   emptyDone,
		"failed":              failed,
		"failed without exit": ended(StateFailed, ExecUnknown),
		"stopped":             stopped,
		"full fixture":        fullRun(),
	}
	for name, r := range cases {
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateRejectsIllegalShapes(t *testing.T) {
	mut := func(f func(r *Run)) *Run { r := fullRun(); f(r); return r }
	cases := map[string]*Run{
		"schema_version 0":         mut(func(r *Run) { r.SchemaVersion = 0 }),
		"schema_version 2":         mut(func(r *Run) { r.SchemaVersion = 2 }),
		"unknown state":            mut(func(r *Run) { r.State = "finished" }),
		"unknown execution":        mut(func(r *Run) { r.Execution = "dead" }),
		"done without ended_at":    mut(func(r *Run) { r.EndedAt = nil }),
		"failed without ended_at":  mut(func(r *Run) { r.State, r.EndedAt = StateFailed, nil }),
		"stopped without ended_at": mut(func(r *Run) { r.State, r.EndedAt = StateStopped, nil }),
		"done result unavailable":  mut(func(r *Run) { r.Result.Available = false }),
		"done negative bytes":      mut(func(r *Run) { r.Result.Bytes = -1 }),
		"done without exit":        mut(func(r *Run) { r.Exit = nil }),
		"done exit code null":      mut(func(r *Run) { r.Exit = &Exit{Signal: ptr("SIGKILL")} }),
		"done exit code 1":         mut(func(r *Run) { r.Exit = &Exit{Code: ptr(1)} }),
		"done execution unknown":   mut(func(r *Run) { r.Execution = ExecUnknown }),
		"done execution live":      mut(func(r *Run) { r.Execution = ExecLive }),
		"interrupted unknown":      mut(func(r *Run) { r.State, r.Execution = StateInterrupted, ExecUnknown }),
		"interrupted not_started":  mut(func(r *Run) { r.State, r.Execution = StateInterrupted, ExecNotStarted }),
	}
	for name, r := range cases {
		err := r.Validate()
		if !errors.Is(err, CodeCorruptState) {
			t.Errorf("%s: Validate = %v, want CORRUPT_STATE", name, err)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	want := map[State]bool{StateStarting: false, StateRunning: false, StateStopping: false,
		StateDone: true, StateFailed: true, StateStopped: true, StateInterrupted: true, "bogus": false}
	for s, w := range want {
		if IsTerminal(s) != w {
			t.Errorf("IsTerminal(%q) = %v", s, !w)
		}
	}
}

func mustHash(t *testing.T, r Request) string {
	t.Helper()
	h, err := RequestHash(r)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRequestHashStableAcrossFieldOrderAndWhitespace(t *testing.T) {
	docs := []string{
		`{"worker":"codex","prompt":"do it","cwd":"/tmp/p","mode":"read-only","model":"m","timeout_ms":5}`,
		"{ \"timeout_ms\" : 5 ,\n\t\"model\":\"m\", \"mode\":\"read-only\",\"cwd\":\"/tmp/p\",\"prompt\":\"do it\",\"worker\":\"codex\" }",
		`{"worker":"codex","prompt":"do it","cwd":"/tmp/p","mode":"read-only","model":"m","timeout_ms":5,"effort":"","role":""}`,
	}
	var first string
	for i, d := range docs {
		var r Request
		if err := json.Unmarshal([]byte(d), &r); err != nil {
			t.Fatal(err)
		}
		h := mustHash(t, r)
		if len(h) != 64 {
			t.Fatalf("hash %q is not hex sha256", h)
		}
		if i == 0 {
			first = h
		} else if h != first {
			t.Errorf("doc %d hashed %s, want %s", i, h, first)
		}
	}
}

func TestRequestHashChangesWithEveryField(t *testing.T) {
	base := Request{Worker: "codex", Prompt: "p", CWD: "/tmp/p", Model: "m", Effort: "e",
		Mode: "read-only", Role: "r", Task: "t", TimeoutMS: ptr(int64(5))}
	h0 := mustHash(t, base)
	edits := map[string]func(r *Request){
		"worker":          func(r *Request) { r.Worker = "claude" },
		"prompt":          func(r *Request) { r.Prompt = "p " },
		"cwd":             func(r *Request) { r.CWD = "/tmp/p/" },
		"model":           func(r *Request) { r.Model = "" },
		"effort":          func(r *Request) { r.Effort = "E" },
		"mode":            func(r *Request) { r.Mode = "workspace-write" },
		"role":            func(r *Request) { r.Role = "r2" },
		"task":            func(r *Request) { r.Task = "t2" },
		"timeout_ms":      func(r *Request) { r.TimeoutMS = ptr(int64(6)) },
		"timeout absent":  func(r *Request) { r.TimeoutMS = nil },
		"timeout zero":    func(r *Request) { r.TimeoutMS = ptr(int64(0)) },
		"value moves key": func(r *Request) { r.Role, r.Task = "t", "r" },
	}
	for name, edit := range edits {
		r := base
		edit(&r)
		if mustHash(t, r) == h0 {
			t.Errorf("changing %s kept the hash", name)
		}
	}
}

func TestRequestHashRejectsInvalidUTF8(t *testing.T) {
	_, err := RequestHash(Request{Worker: "codex", Prompt: "\xff", CWD: "/tmp/p", Mode: "read-only"})
	if !errors.Is(err, CodeInvalidInput) {
		t.Errorf("err = %v, want INVALID_INPUT", err)
	}
}

func TestExitCodePrecedence(t *testing.T) {
	type out struct {
		s State
		e Execution
	}
	D, F := out{StateDone, ExecExited}, out{StateFailed, ExecExited}
	S, I := out{StateStopped, ExecExited}, out{StateInterrupted, ExecUnknown}
	INS := out{StateInterrupted, ExecNotStarted}
	FU := out{StateFailed, ExecUnknown}
	R := out{StateRunning, ExecLive}
	cases := []struct {
		name string
		runs []out
		want int
	}{
		{"all done", []out{D, D}, 0},
		{"one failed", []out{D, F}, 1},
		{"one stopped", []out{D, S}, 2},
		{"one interrupted", []out{D, I}, 3},
		{"interrupted not_started", []out{INS}, 3},
		{"terminal with unknown execution", []out{FU}, 3},
		{"failed beats stopped", []out{S, F}, 1},
		{"interrupted beats failed", []out{F, I}, 3},
		{"interrupted beats stopped", []out{S, I, D}, 3},
		{"interrupted beats all", []out{D, F, S, I}, 3},
		{"still running, else done", []out{D, R}, 5},
		{"still running with failed", []out{R, F}, 1},
		{"no runs", nil, 4},
	}
	for _, c := range cases {
		var states []State
		var execs []Execution
		for _, r := range c.runs {
			states, execs = append(states, r.s), append(execs, r.e)
		}
		if got := ExitCode(states, execs); got != c.want {
			t.Errorf("%s: ExitCode = %d, want %d", c.name, got, c.want)
		}
	}
	if got := ExitCode([]State{StateDone}, nil); got != 5 {
		t.Errorf("mismatched lengths: ExitCode = %d, want 5", got)
	}
}
