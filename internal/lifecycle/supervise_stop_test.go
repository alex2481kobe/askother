package lifecycle

import (
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/config"
	"github.com/alex2481kobe/orca/internal/run"
	"github.com/alex2481kobe/orca/internal/testutil"
	"github.com/alex2481kobe/orca/internal/worker"
)

// Readiness comes only after running/live is committed, and a stop ends
// the run stopped within poll plus grace.
func TestSuperviseStop(t *testing.T) {
	cases := []struct {
		scenario, signal string
		minWait          time.Duration // ignore_term must sit out the grace
	}{
		{"hang", "SIGTERM", 0},
		{"ignore_term", "SIGKILL", 300 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			e := newSVEnv(t)
			bin, _ := e.fake("codex", c.scenario, "")
			id, lock := e.put("codex", bin, nil)
			r, code := e.start(id, lock)
			if r.State != run.StateRunning || r.Execution != run.ExecLive || r.StartedAt == nil || r.Runtime.WorkerPGID == nil {
				t.Fatalf("at readiness: %s/%s started %v pgid %v, want running/live", r.State, r.Execution, r.StartedAt, r.Runtime.WorkerPGID)
			}
			time.Sleep(100 * time.Millisecond) // let ignore_term arm its handler
			asked := e.requestStop(id)
			if got := waitCode(t, code, 5*time.Second); got != 0 {
				t.Fatalf("Supervise = %d", got)
			}
			took := time.Since(asked)
			r = e.read(id)
			if r.State != run.StateStopped || r.Exit == nil || r.Exit.Signal == nil || *r.Exit.Signal != c.signal {
				t.Fatalf("state %s exit %+v, want stopped by %s", r.State, r.Exit, c.signal)
			}
			if r.Result.Available || r.Error != nil || *r.StopReason != run.StopCaller {
				t.Errorf("result %+v error %+v reason %v", r.Result, r.Error, *r.StopReason)
			}
			// poll 20 ms + grace 300 ms + drain; far below the 5 s production grace
			if took < c.minWait || took > 2*time.Second {
				t.Errorf("stop took %v", took)
			}
		})
	}
}

func TestSuperviseTimeoutStops(t *testing.T) {
	e := newSVEnv(t)
	bin, _ := e.fake("claude", "hang", "")
	ms := int64(150)
	id, lock := e.put("claude", bin, func(r *run.Run) { r.Request.TimeoutMS = &ms })
	began := time.Now()
	_, code := e.start(id, lock)
	if got := waitCode(t, code, 3*time.Second); got != 0 {
		t.Fatalf("Supervise = %d", got)
	}
	r := e.read(id)
	if r.State != run.StateStopped || r.StopReason == nil || *r.StopReason != run.StopTimeout || r.StopRequestedAt == nil {
		t.Fatalf("state %s reason %v requested %v, want stopped by timeout", r.State, r.StopReason, r.StopRequestedAt)
	}
	if took := time.Since(began); took < 150*time.Millisecond || took > 3*time.Second {
		t.Errorf("timeout run took %v", took)
	}
}

// A stop that lands after the worker already succeeded gives done.
// grandchild_setsid keeps the drain open for DrainBound after a successful
// exit, so the stop request is seen while the supervisor still runs; the
// run also carries the drain_bound_expired note.
func TestSuperviseStopAfterSuccessIsDone(t *testing.T) {
	e := newSVEnv(t)
	bin, rec := e.fake("codex", "grandchild_setsid", "10s")
	id, lock := e.put("codex", bin, nil)
	r, code := e.start(id, lock)
	// Wait until the fake has answered and exited (its live lock is gone).
	var fr testutil.FakeRecord
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("worker did not answer and exit")
		}
		if _, err := os.Stat(rec); err != nil {
			continue
		}
		if fr = testutil.ReadFakeRecord(t, rec); fr.Answer != nil && !slices.Contains(testutil.LiveFakes(), *r.Runtime.WorkerPID) {
			break
		}
	}
	testutil.KillOnCleanup(t, fr.GrandchildPID)
	e.requestStop(id)
	time.Sleep(100 * time.Millisecond) // several polls: the watcher sees it
	if got := e.read(id).State; got != run.StateStopping {
		t.Errorf("state after the late stop = %s, want stopping", got)
	}
	if got := waitCode(t, code, 5*time.Second); got != 0 {
		t.Fatalf("Supervise = %d", got)
	}
	r = checkDone(t, e, id, fr)
	if r.StopRequestedAt == nil || !slices.Contains(r.Notes, run.NoteDrainBoundExpired) {
		t.Errorf("stop_requested_at %v notes %v", r.StopRequestedAt, r.Notes)
	}
}

// The session the worker reports reaches the record while it still runs,
// before any terminal write.
func TestSuperviseRecordsSessionWhileRunning(t *testing.T) {
	e := newSVEnv(t)
	bin, rec := e.fake("codex", "hang", "")
	id, lock := e.put("codex", bin, nil)
	_, code := e.start(id, lock)
	var r *run.Run
	for deadline := time.Now().Add(5 * time.Second); r == nil || r.NativeSessionID == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no session in the record while running")
		}
		r = e.read(id)
	}
	if fr := testutil.ReadFakeRecord(t, rec); r.State != run.StateRunning || *r.NativeSessionID != fr.SessionID {
		t.Fatalf("state %s session %q, want running with the fake's %q", r.State, *r.NativeSessionID, fr.SessionID)
	}
	e.requestStop(id)
	waitCode(t, code, 5*time.Second)
}

func init() {
	// args: state home, run id, Self. Supervise runs as `orca supervise`
	// would: fds 3 and 4 inherited from StartDetached.
	registerHelper("v-supervise", func(args []string) error {
		st, err := run.Open(args[0])
		if err != nil {
			return err
		}
		d := Deps{Store: st, Self: args[2], StateHome: args[0], Env: config.EnvMap(os.Environ()),
			Adapters: map[string]worker.Adapter{"codex": worker.Codex{}, "claude": worker.Claude{}}}
		if code := Supervise(d, args[1], os.NewFile(run.LockFD, "lock"), os.NewFile(run.ReadyFD, "ready")); code != 0 {
			return fmt.Errorf("Supervise = %d", code)
		}
		return nil
	})
}

// A detached supervisor marks fds 3 and 4 close-on-exec before starting the
// worker, so a grandchild that outlives everything does not hold the run
// lock: once the supervisor exits, the run reads not live.
func TestSuperviseDetachedReleasesLock(t *testing.T) {
	e := newSVEnv(t)
	bin, rec := e.fake("codex", "grandchild_setsid", "10s")
	id, lock := e.put("codex", bin, nil)
	rr, rw, err := run.NewReadiness()
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Close()
	t.Setenv(helperEnv, "v-supervise")
	cmd, err := run.StartDetached(os.Args[0], []string{e.d.StateHome, id, e.d.Self}, []*os.File{lock, rw})
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	rw.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if got, err := run.WaitReady(rr, 10*time.Second); got != run.Ready || err != nil {
		t.Fatalf("readiness = %v, %v", got, err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("supervisor: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not exit")
	}
	fr := testutil.ReadFakeRecord(t, rec)
	testutil.KillOnCleanup(t, fr.GrandchildPID)
	if !slices.Contains(testutil.LiveFakes(), fr.GrandchildPID) {
		t.Fatal("the grandchild is gone; the test proves nothing")
	}
	if live, err := e.d.Store.ProbeLive(id); live || err != nil {
		t.Fatalf("run lock live=%v (%v) after the supervisor exited: the grandchild inherited it", live, err)
	}
	checkDone(t, e, id, fr)
}
