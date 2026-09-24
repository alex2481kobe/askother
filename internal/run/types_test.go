package run

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/worker"
)

func ptr[T any](v T) *T { return &v }

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)

func minimalRun() *Run {
	return &Run{
		SchemaVersion: SchemaVersion,
		ID:            "20260102T030405Z-00112233445566778899aabbccddeeff",
		Key:           "k1",
		RequestSHA256: strings.Repeat("0", 64),
		CallerID:      "caller-1",
		CallerSource:  SourceFallback,
		Request:       Request{Worker: "codex", Prompt: "hi", CWD: "/tmp/project", Mode: "read-only"},
		State:         StateStarting,
		Execution:     ExecNotStarted,
		CreatedAt:     t0,
		Runtime:       Runtime{Binary: "/usr/local/bin/codex"},
	}
}

func fullRun() *Run {
	r := minimalRun()
	r.CallerSource = SourceClaude
	r.PreviousID = ptr("previous-run")
	r.Request = Request{Worker: "claude", Prompt: "p", CWD: "/tmp/project", Model: "m", Effort: "high",
		Mode: "dontAsk", Role: "scoper", Task: "t", TimeoutMS: ptr(int64(60000))}
	r.State, r.Execution = StateDone, ExecExited
	r.StartedAt, r.EndedAt = ptr(t0.Add(time.Second)), ptr(t0.Add(time.Minute))
	r.DurationMS = ptr(int64(59000))
	r.Runtime.SupervisorPID, r.Runtime.WorkerPID, r.Runtime.WorkerPGID = ptr(10), ptr(11), ptr(11)
	r.NativeSessionID = ptr("session-1")
	r.Exit = &Exit{Code: ptr(0)}
	r.Result = Result{Available: true, Bytes: 5}
	r.Error = &worker.Failure{Code: "PROTOCOL", Message: "m", Truncated: true}
	r.StderrTail = StderrTail{Text: "warn", Truncated: true}
	r.Notes = []string{"drain_bound_expired"}
	r.StopRequestedAt = ptr(t0.Add(3 * time.Second))
	r.StopReason = ptr(StopCaller)
	r.FirstReadAt = ptr(t0.Add(time.Hour))
	return r
}

func roundTrip(t *testing.T, in *Run) map[string]any {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Run
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, &out) {
		t.Fatalf("round trip changed the record\nin:  %+v\nout: %+v", in, &out)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunRoundTripFull(t *testing.T) {
	m := roundTrip(t, fullRun())
	for _, k := range []string{"started_at", "ended_at", "duration_ms", "notes", "stop_requested_at", "first_read_at"} {
		if _, ok := m[k]; !ok {
			t.Errorf("full record lacks %q", k)
		}
	}
}

// The fields v2 removed must not come back under their old names.
func TestRunHasNoRemovedFields(t *testing.T) {
	m := roundTrip(t, fullRun())
	for _, k := range []string{"parent_id", "effective", "usage", "activity", "ui_hidden_at"} {
		if _, ok := m[k]; ok {
			t.Errorf("record has removed field %q", k)
		}
	}
	for _, path := range [][2]string{{"runtime", "boot_id"}, {"runtime", "version"}, {"result", "sha256"}} {
		if _, ok := m[path[0]].(map[string]any)[path[1]]; ok {
			t.Errorf("record has removed field %s.%s", path[0], path[1])
		}
	}
}

func TestRunRoundTripMinimalKeepsUnknownsAbsentOrNull(t *testing.T) {
	m := roundTrip(t, minimalRun())
	for _, k := range []string{"started_at", "ended_at", "duration_ms", "notes", "stop_requested_at", "first_read_at"} {
		if _, ok := m[k]; ok {
			t.Errorf("minimal record has %q = %v, want absent", k, m[k])
		}
	}
	for _, k := range []string{"previous_id", "native_session_id", "exit", "error", "stop_reason"} {
		v, ok := m[k]
		if !ok || v != nil {
			t.Errorf("%q = %v (present %v), want null", k, v, ok)
		}
	}
	req := m["request"].(map[string]any)
	for _, k := range []string{"model", "effort", "role", "task", "timeout_ms"} {
		if _, ok := req[k]; ok {
			t.Errorf("request.%s present, want absent", k)
		}
	}
	rt := m["runtime"].(map[string]any)
	for _, k := range []string{"supervisor_pid", "worker_pid", "worker_pgid"} {
		if _, ok := rt[k]; ok {
			t.Errorf("runtime.%s present, want absent", k)
		}
	}
}

func TestExitOf(t *testing.T) {
	if e := ExitOf(worker.Exit{Code: ptr(3)}); *e.Code != 3 || e.Signal != nil {
		t.Errorf("code exit = %+v", e)
	}
	if e := ExitOf(worker.Exit{Signal: "SIGKILL"}); e.Code != nil || e.Signal == nil || *e.Signal != "SIGKILL" {
		t.Errorf("signal exit = %+v", e)
	}
}
