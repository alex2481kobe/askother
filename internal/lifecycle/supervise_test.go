package lifecycle

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/testutil"
	"github.com/alex2481kobe/askother/internal/worker"
)

// svEnv is one in-process supervision setup: a temp store, the real
// adapters, and fake worker binaries reached through wrapper scripts. The
// wrapper is needed because the worker env is an allowlist, so the fake's
// ASKOTHER_FAKE_* selection cannot come from d.Env.
type svEnv struct {
	t             *testing.T
	d             Deps
	dir, cwd      string
	codex, claude string
}

func newSVEnv(t *testing.T) *svEnv {
	codex, claude := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "state")
	st, err := run.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(dir, "work")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	svSeams(t, 20*time.Millisecond, 300*time.Millisecond)
	return &svEnv{t: t, dir: dir, cwd: cwd, codex: codex, claude: claude, d: Deps{
		Store:     st,
		Adapters:  map[string]worker.Adapter{"codex": worker.Codex{}, "claude": worker.Claude{}},
		Self:      filepath.Join(dir, "bin", "askother"),
		StateHome: home,
		Env: map[string]string{
			"PATH": os.Getenv("PATH"), "HOME": dir, "LANG": "C.UTF-8",
			"CLAUDE_CODE_SESSION_ID": "caller-session", "CLAUDECODE": "1",
			"CODEX_HOME": filepath.Join(dir, "codex-home"), "ASKOTHER_RUN_ID": "caller-run",
		},
	}}
}

// svSeams shortens the poll and grace for the test and restores all seams.
func svSeams(t *testing.T, poll, grace time.Duration) {
	p, g, retry, pub, max := svPoll, svGrace, svRetryDelay, svPublish, svMaxLine
	svPoll, svGrace, svRetryDelay = poll, grace, 20*time.Millisecond
	t.Cleanup(func() {
		svPoll, svGrace, svRetryDelay, svPublish, svMaxLine = p, g, retry, pub, max
	})
}

// fake writes a wrapper named after the dialect that selects scenario and
// arg for the fake and execs it; it returns the wrapper and the fake's record.
func (e *svEnv) fake(dialect, scenario, arg string) (bin, record string) {
	fakeBin := e.codex
	if dialect == "claude" {
		fakeBin = e.claude
	}
	dir, err := os.MkdirTemp(e.dir, "bin-")
	if err != nil {
		e.t.Fatal(err)
	}
	record = filepath.Join(dir, "fake-record.json")
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := fmt.Sprintf("#!/bin/sh\nASKOTHER_FAKE_SCENARIO=%s ASKOTHER_FAKE_ARG=%s ASKOTHER_FAKE_RECORD=%s\n"+
		"export ASKOTHER_FAKE_SCENARIO ASKOTHER_FAKE_ARG ASKOTHER_FAKE_RECORD\nexec %s \"$@\"\n",
		q(scenario), q(arg), q(record), q(fakeBin))
	bin = filepath.Join(dir, dialect)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		e.t.Fatal(err)
	}
	return bin, record
}

// put writes a starting record shaped as the launcher writes it, creates its
// run lock, and returns the id and lock. mut may adjust the record first.
func (e *svEnv) put(dialect, bin string, mut func(*run.Run)) (string, *os.File) {
	t := e.t
	id, err := run.NewID()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := e.d.Store.CreateRunLock(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	mode := worker.Codex{}.Facts().DefaultMode
	if dialect == "claude" {
		mode = worker.Claude{}.Facts().DefaultMode
	}
	r := &run.Run{
		SchemaVersion: run.SchemaVersion, ID: id, Key: "key-" + id,
		CallerID: "caller-1", CallerSource: run.SourceClaude,
		Request: run.Request{Worker: dialect, Prompt: "say OK", CWD: e.cwd, Mode: mode},
		State:   run.StateStarting, Execution: run.ExecNotStarted,
		CreatedAt: time.Now().UTC(),
		Runtime:   run.Runtime{Binary: bin},
	}
	if mut != nil {
		mut(r)
	}
	if r.RequestSHA256, err = run.RequestHash(r.Request); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Store.WithIndexLock(func() error { return e.d.Store.Put(r) }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.killWorker(id) })
	return id, lock
}

// killWorker SIGKILLs the run's worker group if that pid is still a live fake.
func (e *svEnv) killWorker(id string) {
	r, err := e.d.Store.Read(id)
	if err == nil && r.Runtime.WorkerPGID != nil && slices.Contains(testutil.LiveFakes(), *r.Runtime.WorkerPGID) {
		_ = syscall.Kill(-*r.Runtime.WorkerPGID, syscall.SIGKILL)
	}
}

// supervise runs Supervise to completion and returns its exit code.
func (e *svEnv) supervise(id string, lock *os.File) int {
	rr, rw, err := run.NewReadiness()
	if err != nil {
		e.t.Fatal(err)
	}
	defer rr.Close()
	return Supervise(e.d, id, lock, rw)
}

// start runs Supervise in the background and waits for the readiness byte.
// It returns the record as read right after the byte, and the exit code.
func (e *svEnv) start(id string, lock *os.File) (*run.Run, <-chan int) {
	t := e.t
	rr, rw, err := run.NewReadiness()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rr.Close() })
	code, done := make(chan int, 1), make(chan struct{})
	go func() {
		defer close(done)
		code <- Supervise(e.d, id, lock, rw)
	}()
	t.Cleanup(func() { // a failed test must not leave Supervise running into the next
		for range 100 {
			e.killWorker(id) // the pgid may be committed only after a failed check
			select {
			case <-done:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		t.Error("Supervise still running at cleanup")
	})
	if got, err := run.WaitReady(rr, 10*time.Second); got != run.Ready || err != nil {
		t.Fatalf("readiness = %v, %v", got, err)
	}
	r, err := e.d.Store.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return r, code
}

// requestStop writes a caller stop request as RequestStop does.
func (e *svEnv) requestStop(id string) time.Time {
	now := time.Now()
	if _, err := e.d.Store.Update(id, func(r *run.Run) error {
		at, reason := now.UTC(), run.StopCaller
		r.StopRequestedAt, r.StopReason = &at, &reason
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	return now
}

func (e *svEnv) read(id string) *run.Run {
	r, err := e.d.Store.Read(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func svUUID(t *testing.T) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func waitCode(t *testing.T, code <-chan int, within time.Duration) int {
	t.Helper()
	select {
	case c := <-code:
		return c
	case <-time.After(within):
		t.Fatalf("Supervise did not return within %v", within)
	}
	return -1
}

// checkDone asserts a done record whose published answer matches the fake's.
func checkDone(t *testing.T, e *svEnv, id string, fr testutil.FakeRecord) *run.Run {
	t.Helper()
	r := e.read(id)
	if r.State != run.StateDone || r.Execution != run.ExecExited || r.Error != nil {
		t.Fatalf("state %s/%s error %+v, want done/exited", r.State, r.Execution, r.Error)
	}
	if fr.Answer == nil || r.Result.Bytes != int64(fr.Answer.Bytes) {
		t.Fatalf("result %+v, fake answer %+v", r.Result, fr.Answer)
	}
	data, err := os.ReadFile(filepath.Join(e.d.StateHome, "runs", id+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != fr.Answer.SHA256 {
		t.Fatalf("published answer is not byte-exact (%d bytes)", len(data))
	}
	if live, _ := e.d.Store.ProbeLive(id); live {
		t.Fatal("run lock still held after Supervise returned")
	}
	return r
}

func TestSuperviseCodexDone(t *testing.T) {
	for _, scenario := range []string{"ok", "progress_then_final"} {
		t.Run(scenario, func(t *testing.T) {
			e := newSVEnv(t)
			bin, rec := e.fake("codex", scenario, "")
			id, lock := e.put("codex", bin, nil)
			if code := e.supervise(id, lock); code != 0 {
				t.Fatalf("Supervise = %d", code)
			}
			fr := testutil.ReadFakeRecord(t, rec)
			r := checkDone(t, e, id, fr)
			if r.NativeSessionID == nil || *r.NativeSessionID != fr.SessionID {
				t.Errorf("native_session_id %v, fake thread %q", r.NativeSessionID, fr.SessionID)
			}
			if r.StartedAt == nil || r.DurationMS == nil || r.Runtime.WorkerPID == nil ||
				*r.Runtime.WorkerPID != fr.PID || r.Runtime.SupervisorPID == nil || *r.Runtime.SupervisorPID != os.Getpid() {
				t.Errorf("launch evidence %+v started %v duration %v", r.Runtime, r.StartedAt, r.DurationMS)
			}
			if _, err := os.Stat(e.d.Store.AnswerTempPath(id)); !os.IsNotExist(err) {
				t.Errorf("answer temp still present: %v", err)
			}
		})
	}
}

// Claude picks its own session and reports it in system/init; the record
// takes it from there.
func TestSuperviseClaudeDoneTakesSessionFromInit(t *testing.T) {
	e := newSVEnv(t)
	bin, rec := e.fake("claude", "ok", "")
	id, lock := e.put("claude", bin, nil)
	if code := e.supervise(id, lock); code != 0 {
		t.Fatalf("Supervise = %d", code)
	}
	fr := testutil.ReadFakeRecord(t, rec)
	r := checkDone(t, e, id, fr)
	if fr.Invocation.Resume || fr.SessionID == "" {
		t.Fatalf("fake invocation %+v session %q, want a new session", fr.Invocation, fr.SessionID)
	}
	if r.NativeSessionID == nil || *r.NativeSessionID != fr.SessionID {
		t.Errorf("native_session_id %v, want %q from init", r.NativeSessionID, fr.SessionID)
	}
}
