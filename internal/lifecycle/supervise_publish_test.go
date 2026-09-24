package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/run"
	"github.com/alex2481kobe/orca/internal/testutil"
	"github.com/alex2481kobe/orca/internal/worker"
)

// The worker env is the allowlist only: no caller harness ids and no
// ORCA_RUN_ID reach the worker, and no Orca MCP entry is added to its argv.
func TestSuperviseWorkerEnv(t *testing.T) {
	for _, dialect := range []string{"codex", "claude"} {
		t.Run(dialect, func(t *testing.T) {
			e := newSVEnv(t)
			bin, rec := e.fake(dialect, "ok", "")
			id, lock := e.put(dialect, bin, nil)
			if code := e.supervise(id, lock); code != 0 {
				t.Fatalf("Supervise = %d", code)
			}
			fr := testutil.ReadFakeRecord(t, rec)
			for _, n := range fr.EnvNames {
				if strings.HasPrefix(n, "CLAUDE") || strings.HasPrefix(n, "CODEX_") || n == "ORCA_RUN_ID" {
					t.Errorf("worker env has %s", n)
				}
			}
			if argv := strings.Join(fr.Argv, " "); strings.Contains(argv, "mcp") {
				t.Errorf("worker argv mentions mcp: %s", argv)
			}
		})
	}
}

// A continuation resumes the native session in its own record, copied
// from the source at admission; the source record is not needed at all.
func TestSuperviseContinuationResumesOwnSession(t *testing.T) {
	for _, dialect := range []string{"codex", "claude"} {
		t.Run(dialect, func(t *testing.T) {
			e := newSVEnv(t)
			session, gone := svUUID(t), "20260101T000000Z-00000000000000000000000000000000"
			bin, rec := e.fake(dialect, "ok", "")
			id, lock := e.put(dialect, bin, func(r *run.Run) { r.PreviousID, r.NativeSessionID = &gone, &session })
			if code := e.supervise(id, lock); code != 0 {
				t.Fatalf("Supervise = %d", code)
			}
			fr := testutil.ReadFakeRecord(t, rec)
			if !fr.Invocation.Resume || fr.Invocation.ResumeID != session {
				t.Fatalf("fake invocation %+v, want resume of %s", fr.Invocation, session)
			}
			if r := checkDone(t, e, id, fr); *r.NativeSessionID != session {
				t.Errorf("native_session_id %s, want %s", *r.NativeSessionID, session)
			}
		})
	}
}

// A continuation that fails keeps the session it was given, so the session
// can be continued again from this run.
func TestSuperviseFailedContinuationKeepsSession(t *testing.T) {
	e := newSVEnv(t)
	session := svUUID(t)
	bin, _ := e.fake("codex", "stderr_only", "")
	id, lock := e.put("codex", bin, func(r *run.Run) { r.NativeSessionID = &session })
	if code := e.supervise(id, lock); code != 0 {
		t.Fatalf("Supervise = %d", code)
	}
	r := e.read(id)
	if r.State != run.StateFailed || r.Execution != run.ExecExited || r.NativeSessionID == nil || *r.NativeSessionID != session {
		t.Fatalf("state %s/%s session %v, want failed/exited keeping %s", r.State, r.Execution, r.NativeSessionID, session)
	}
}

func TestSuperviseFailures(t *testing.T) {
	cases := []struct{ dialect, scenario, arg, code, msg string }{
		{"codex", "missing_o_file", "", "PROTOCOL", ""},
		{"codex", "turn_failed", "", "WORKER_FAILED", ""},
		{"claude", "is_error_result", "", "WORKER_FAILED", ""},
		{"codex", "crash_midstream", "", "WORKER_FAILED", ""},
		{"claude", "crash_midstream", "kill", "PROTOCOL", ""},
		{"codex", "huge_line", "4096", "PROTOCOL", "exceeds 1024 bytes"},
		// A worker that explains itself only on stderr: the error says why.
		{"codex", "stderr_only", "error: synthetic bad flag", "WORKER_FAILED", "stderr: error: synthetic bad flag"},
		{"claude", "stderr_only", "", "PROTOCOL", "stderr: Error: synthetic startup failure"},
	}
	for _, c := range cases {
		t.Run(c.dialect+"/"+c.scenario, func(t *testing.T) {
			e := newSVEnv(t)
			if c.scenario == "huge_line" {
				svMaxLine = 1024
			}
			bin, _ := e.fake(c.dialect, c.scenario, c.arg)
			id, lock := e.put(c.dialect, bin, nil)
			if code := e.supervise(id, lock); code != 0 {
				t.Fatalf("Supervise = %d", code)
			}
			r := e.read(id)
			if r.State != run.StateFailed || r.Execution != run.ExecExited || r.Error == nil || r.Error.Code != c.code {
				t.Fatalf("state %s/%s error %+v, want failed %s", r.State, r.Execution, r.Error, c.code)
			}
			if !strings.Contains(r.Error.Message, c.msg) {
				t.Errorf("error message %q lacks %q", r.Error.Message, c.msg)
			}
			if r.Result.Available || r.Exit == nil || r.EndedAt == nil {
				t.Errorf("result %+v exit %+v ended %v", r.Result, r.Exit, r.EndedAt)
			}
			for _, p := range []string{filepath.Join(e.d.StateHome, "runs", id+".txt"), e.d.Store.AnswerTempPath(id)} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Errorf("%s exists after a failed run", filepath.Base(p))
				}
			}
		})
	}
}

func TestWithStderr(t *testing.T) {
	long := strings.Repeat("\u00e9", stderrInError) // 2 bytes a rune
	cases := []struct {
		name string
		f    *worker.Failure
		tail run.StderrTail
		want string
		cut  bool
	}{
		{"no failure, no stderr", nil, run.StderrTail{}, "the worker did not succeed and gave no reason", false},
		{"no failure", nil, run.StderrTail{Text: " boom\n"}, "stderr: boom", false},
		{"failure, no stderr", &worker.Failure{Code: "PROTOCOL", Message: "m"}, run.StderrTail{Text: "\n"}, "m", false},
		{"both", &worker.Failure{Code: "PROTOCOL", Message: "m"}, run.StderrTail{Text: "boom"}, "m; stderr: boom", false},
		{"tail already cut", nil, run.StderrTail{Text: "boom", Truncated: true}, "stderr: boom", true},
		{"long tail", nil, run.StderrTail{Text: long}, "stderr: " + long[len(long)-stderrInError:], true},
	}
	for _, c := range cases {
		got := withStderr(c.f, c.tail)
		if got.Message != c.want || got.Truncated != c.cut {
			t.Errorf("%s: %q truncated %v, want %q %v", c.name, got.Message, got.Truncated, c.want, c.cut)
		}
	}
}

// svChmodRuns makes the runs directory unwritable (or writable again).
func svChmodRuns(t *testing.T, e *svEnv, writable bool) {
	mode := os.FileMode(0o500)
	if writable {
		mode = 0o700
	}
	if err := os.Chmod(filepath.Join(e.d.StateHome, "runs"), mode); err != nil {
		t.Fatal(err)
	}
}

// A store fault at publish never yields done. The runs dir is made
// unwritable at the publish step through the svPublish seam, so
// the real rename fails; the Store's own fault hook is not reachable here.
func TestSupervisePublishFaults(t *testing.T) {
	cases := []struct {
		name     string
		restore  time.Duration // when to make the runs dir writable again; <0 never
		visible  bool          // fake a visible-not-durable publish instead
		wantCode int
		want     run.State
	}{
		{"rename_fails", 0, false, 0, run.StateFailed},
		{"not_durable", 0, true, 0, run.StateFailed},
		{"terminal_retried", 30 * time.Millisecond, false, 0, run.StateFailed},
		{"retries_exhausted", -1, false, 1, run.StateRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSVEnv(t)
			t.Cleanup(func() { svChmodRuns(t, e, true) })
			svPublish = func(s *run.Store, id, tmp string) (int64, error) {
				if c.visible {
					n, err := s.PublishAnswer(id, tmp)
					if err != nil {
						t.Errorf("real publish: %v", err)
					}
					return n, &run.StoreError{Path: id + ".txt", Op: "dirsync", Visible: true, Err: syscall.EIO}
				}
				svChmodRuns(t, e, false)
				n, err := s.PublishAnswer(id, tmp)
				if c.restore == 0 {
					svChmodRuns(t, e, true)
				} else if c.restore > 0 {
					restored := make(chan struct{})
					time.AfterFunc(c.restore, func() {
						_ = os.Chmod(filepath.Join(e.d.StateHome, "runs"), 0o700) // no t.Fatal off the test goroutine
						close(restored)
					})
					t.Cleanup(func() { <-restored })
				}
				if err == nil || run.Visible(err) || !errors.Is(err, run.CodeStoreIO) {
					t.Errorf("publish into an unwritable dir: %v", err)
				}
				return n, err
			}
			bin, _ := e.fake("codex", "ok", "")
			id, lock := e.put("codex", bin, nil)
			if code := e.supervise(id, lock); code != c.wantCode {
				t.Fatalf("Supervise = %d, want %d", code, c.wantCode)
			}
			svChmodRuns(t, e, true)
			r := e.read(id)
			if r.State != c.want || r.Result.Available {
				t.Fatalf("state %s result %+v, want %s and no answer", r.State, r.Result, c.want)
			}
			if c.want == run.StateFailed && (r.Error == nil || r.Error.Code != "STORE_IO") {
				t.Errorf("error %+v, want STORE_IO", r.Error)
			}
			if live, _ := e.d.Store.ProbeLive(id); live {
				t.Fatal("run lock held after Supervise returned")
			}
			// With the lock free, readers see interrupted/unknown, never done.
			if v := derive(r, false); v.State == run.StateDone || (c.wantCode == 1 && v.State != run.StateInterrupted) {
				t.Errorf("reconciled view %s/%s", v.State, v.Execution)
			}
		})
	}
}

// A protocol error from Consume fails the run even if the decoder's Finish
// would call it a success: errors are remembered, not dropped.
func TestSuperviseRemembersProtocolError(t *testing.T) {
	e := newSVEnv(t)
	e.d.Adapters["codex"] = svFlakyCodex{}
	bin, _ := e.fake("codex", "ok", "")
	id, lock := e.put("codex", bin, nil)
	if code := e.supervise(id, lock); code != 0 {
		t.Fatalf("Supervise = %d", code)
	}
	if r := e.read(id); r.State != run.StateFailed || r.Error == nil || r.Error.Code != "PROTOCOL" {
		t.Fatalf("state %s error %+v, want failed PROTOCOL", r.State, r.Error)
	}
}

// svFlakyCodex's decoder reports a protocol error on the first line but
// otherwise decodes as Codex does.
type svFlakyCodex struct{ worker.Codex }

func (svFlakyCodex) NewDecoder(in worker.Invocation) worker.Decoder {
	return &svFlakyDecoder{Decoder: worker.Codex{}.NewDecoder(in)}
}

type svFlakyDecoder struct {
	worker.Decoder
	seen bool
}

func (d *svFlakyDecoder) Consume(line []byte) (worker.Update, error) {
	u, err := d.Decoder.Consume(line)
	if !d.seen && err == nil {
		err = errors.New("synthetic protocol error")
	}
	d.seen = true
	return u, err
}
