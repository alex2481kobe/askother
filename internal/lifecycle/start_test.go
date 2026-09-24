package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
)

func rStartReq(t *testing.T, key string) StartRequest {
	return StartRequest{Key: key, Worker: "codex", Prompt: "do it", CWD: t.TempDir()}
}

func TestStartReturnsRunningRecord(t *testing.T) {
	d := rDeps(t, rModeReady)
	c := Caller{ID: "caller-1", Source: run.SourceClaude}
	req := rStartReq(t, "k1")
	r, reused, err := Start(d, c, req)
	if err != nil || reused {
		t.Fatalf("Start = reused %v, err %v", reused, err)
	}
	if r.State != run.StateRunning || r.Execution != run.ExecLive || r.Runtime.SupervisorPID == nil {
		t.Fatalf("got %s/%s pid %v, want running/live from the supervisor", r.State, r.Execution, r.Runtime.SupervisorPID)
	}
	want := run.Request{Worker: "codex", Prompt: "do it", CWD: req.CWD, Mode: "read-only"}
	if !reflect.DeepEqual(r.Request, want) {
		t.Fatalf("request = %+v, want default mode filled: %+v", r.Request, want)
	}
	hash, _ := run.RequestHash(want)
	switch {
	case r.Key != "k1" || r.RequestSHA256 != hash:
		t.Fatalf("key %q hash %q, want k1 %q", r.Key, r.RequestSHA256, hash)
	case r.CallerID != "caller-1" || r.CallerSource != run.SourceClaude:
		t.Fatalf("caller fields %q %q", r.CallerID, r.CallerSource)
	case r.PreviousID != nil || r.NativeSessionID != nil:
		t.Fatalf("previous %v session %v, want null for a new run", r.PreviousID, r.NativeSessionID)
	case r.Runtime.Binary != rBinary(d):
		t.Fatalf("runtime %+v, want the configured binary", r.Runtime)
	case r.CreatedAt.IsZero():
		t.Fatal("created_at unset")
	}
	if live, _ := d.Store.ProbeLive(r.ID); !live {
		t.Fatal("the launcher must hand the run lock to the supervisor, not release it")
	}
}

func TestStartLinksNestedWorkerToItsParentRun(t *testing.T) {
	d := rDeps(t, rModeReady)
	parent, _, err := Start(d, Caller{ID: "driver", Source: run.SourceCodex}, rStartReq(t, "parent"))
	if err != nil {
		t.Fatal(err)
	}
	d.Env["ASKOTHER_RUN_ID"] = parent.ID
	child, _, err := Start(d, Caller{ID: "child-agent", Source: run.SourceClaude}, rStartReq(t, "child"))
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID == nil || *child.ParentID != parent.ID {
		t.Fatalf("parent_id = %v, want %s", child.ParentID, parent.ID)
	}
	if child.CallerSource != run.SourceClaude {
		t.Fatalf("child caller source = %s, want claude", child.CallerSource)
	}
}

func TestStartKeyDedupe(t *testing.T) {
	d := rDeps(t, rModeReady)
	req := rStartReq(t, "k1")
	first, _, err := Start(d, Caller{ID: "c"}, req)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit default mode hashes the same as an omitted one.
	req.Mode = "read-only"
	again, reused, err := Start(d, Caller{ID: "c"}, req)
	if err != nil || !reused || again.ID != first.ID {
		t.Fatalf("identical retry = %v reused %v err %v, want run %s reused", again, reused, err, first.ID)
	}
	req.Prompt = "something else"
	_, _, err = Start(d, Caller{ID: "c"}, req)
	rCode(t, err, run.CodeKeyConflict)
	runs, _, _ := d.Store.List()
	if len(runs) != 1 {
		t.Fatalf("%d records, want 1", len(runs))
	}
}

// A lost reply is retried with the same key. The retry must recover the
// run even when its cwd, its worker binary and the config are gone by then,
// so the key is looked up before anything that can disappear.
func TestStartRetryRecoversAfterDependenciesVanish(t *testing.T) {
	d := rDeps(t, rModeReady)
	req := rStartReq(t, "k1")
	first, _, err := Start(d, Caller{ID: "c"}, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(req.CWD); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(rBinary(d)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.ConfigPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, reused, err := Start(d, Caller{ID: "c"}, req)
	if err != nil || !reused || again.ID != first.ID {
		t.Fatalf("retry = %v reused %v err %v, want run %s reused", again, reused, err, first.ID)
	}
	// A new key still meets every check.
	_, _, err = Start(d, Caller{ID: "c"}, rStartReq(t, "k2"))
	rCode(t, err, run.CodeInvalidInput)
}

// Many concurrent starts of one key admit exactly one run.
func TestStartConcurrentSameKey(t *testing.T) {
	d := rDeps(t, rModeReady)
	req := rStartReq(t, "k1")
	var wg sync.WaitGroup
	ids := make([]string, 12)
	for i := range ids {
		wg.Go(func() {
			r, _, err := Start(d, Caller{ID: "c"}, req)
			if err != nil {
				t.Errorf("start %d: %v", i, err)
				return
			}
			ids[i] = r.ID
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids %v: one key admitted several runs", ids)
		}
	}
	if runs := mustList(t, d); len(runs) != 1 {
		t.Fatalf("%d records, want 1", len(runs))
	}
}

// An unreadable record could hold this key or a session in use, so every
// admission refuses while one exists, naming it; observation is unaffected.
func TestStartRefusesWhileARecordIsUnreadable(t *testing.T) {
	d := rDeps(t, rModeReady)
	done := rDone(t, d)
	bad, err := run.NewID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(d.Store.AnswerTempPath(bad)), bad+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = Start(d, Caller{ID: "c"}, rStartReq(t, "k1"))
	rCode(t, err, run.CodeCorruptState)
	if !strings.Contains(err.Error(), bad) {
		t.Fatalf("error %q does not name the unreadable record", err)
	}
	_, _, err = Send(d, Caller{ID: "c"}, SendRequest{Key: "s1", ID: done.ID, Message: "m"})
	rCode(t, err, run.CodeCorruptState)
	if _, err := Observe(d, done.ID); err != nil {
		t.Fatalf("observing a readable run: %v", err)
	}
}

func mustList(t *testing.T, d Deps) []*run.Run {
	t.Helper()
	runs, bad, err := d.Store.List()
	if err != nil || len(bad) > 0 {
		t.Fatalf("list: %v %v", bad, err)
	}
	return runs
}

func TestStartSupervisorExitsWithoutSignal(t *testing.T) {
	d := rDeps(t, rModeExit)
	began := time.Now()
	r, _, err := Start(d, Caller{ID: "c"}, rStartReq(t, "k1"))
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(began); el > ReadyTimeout/2 {
		t.Fatalf("Start took %v: EOF must end the wait, not the timeout", el)
	}
	if r.State != run.StateInterrupted || r.Execution != run.ExecNotStarted || r.EndedAt != nil {
		t.Fatalf("got %s/%s ended %v, want the derived interrupted/not_started", r.State, r.Execution, r.EndedAt)
	}
	if stored := mustList(t, d)[0]; stored.State != run.StateStarting {
		t.Fatalf("stored state %s: the interrupted view must never be saved", stored.State)
	}
}

func TestStartReadinessTimeout(t *testing.T) {
	d := rDeps(t, rModeSilent)
	old := readyTimeout
	readyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { readyTimeout = old })
	began := time.Now()
	r, _, err := Start(d, Caller{ID: "c"}, rStartReq(t, "k1"))
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(began); el < 300*time.Millisecond || el > 5*time.Second {
		t.Fatalf("Start took %v, want about the readiness timeout", el)
	}
	// The stand-in still holds the lock, so the record stands as written.
	if r.State != run.StateStarting || r.Execution != run.ExecNotStarted {
		t.Fatalf("got %s/%s, want starting/not_started", r.State, r.Execution)
	}
}

// Claude reports its own session id at init; AskOther no longer invents one.
func TestStartClaudeHasNoPresetSession(t *testing.T) {
	d := rDeps(t, rModeReady)
	req := rStartReq(t, "k1")
	req.Worker = "claude"
	r, _, err := Start(d, Caller{ID: "c"}, req)
	if err != nil {
		t.Fatal(err)
	}
	if r.NativeSessionID != nil {
		t.Fatalf("native_session_id = %q, want null until the worker reports one", *r.NativeSessionID)
	}
	if r.Request.Mode != "dontAsk" {
		t.Fatalf("mode %q, want claude default dontAsk", r.Request.Mode)
	}
}

func TestStartValidation(t *testing.T) {
	d := rDeps(t, rModeReady)
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	zero, neg, week, overWeek := int64(0), int64(-5), MaxTimeout.Milliseconds(), MaxTimeout.Milliseconds()+1
	cases := []struct {
		name string
		edit func(*StartRequest)
	}{
		{"no key", func(r *StartRequest) { r.Key = "" }},
		{"unknown worker", func(r *StartRequest) { r.Worker = "gemini" }},
		{"empty prompt", func(r *StartRequest) { r.Prompt = "" }},
		{"relative cwd", func(r *StartRequest) { r.CWD = "rel/dir" }},
		{"missing cwd", func(r *StartRequest) { r.CWD = filepath.Join(file, "nope") }},
		{"cwd is a file", func(r *StartRequest) { r.CWD = file }},
		{"mode of another worker", func(r *StartRequest) { r.Mode = "dontAsk" }},
		{"zero timeout", func(r *StartRequest) { r.TimeoutMS = &zero }},
		{"negative timeout", func(r *StartRequest) { r.TimeoutMS = &neg }},
		{"timeout over 7 days", func(r *StartRequest) { r.TimeoutMS = &overWeek }},
		{"invalid utf-8", func(r *StartRequest) { r.Prompt = "\xff" }},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := rStartReq(t, fmt.Sprint("k", i))
			c.edit(&req)
			_, _, err := Start(d, Caller{ID: "c"}, req)
			rCode(t, err, run.CodeInvalidInput)
		})
	}
	rConfig(t, d, filepath.Join(file, "missing-codex"))
	_, _, err := Start(d, Caller{ID: "c"}, rStartReq(t, "k-missing"))
	rCode(t, err, run.CodeInvalidInput)
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("missing binary: %v", err)
	}
	if err := os.WriteFile(d.ConfigPath, []byte(`{"cap":4}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = Start(d, Caller{ID: "c"}, rStartReq(t, "k-config"))
	rCode(t, err, run.CodeInvalidInput)
	if runs, _, _ := d.Store.List(); len(runs) != 0 {
		t.Fatalf("refused starts wrote %d records", len(runs))
	}
	rConfig(t, d, rBinary(d))
	req := rStartReq(t, "k-week")
	req.TimeoutMS = &week
	if _, _, err := Start(d, Caller{ID: "c"}, req); err != nil {
		t.Fatalf("a 7-day timeout must be accepted: %v", err)
	}
}

func isCode(err error, c run.Code) bool {
	var e *run.Error
	return errors.As(err, &e) && e.Code == c
}
