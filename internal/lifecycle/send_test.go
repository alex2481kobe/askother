package lifecycle

import (
	"os"
	"sync"
	"testing"

	"github.com/alex2481kobe/askother/internal/run"
)

func TestSendContinuesSource(t *testing.T) {
	d := rDeps(t, rModeReady)
	src := rDone(t, d)
	r, reused, err := Send(d, Caller{ID: "c2", Source: run.SourceFallback}, SendRequest{Key: "s1", ID: src.ID, Message: "more"})
	if err != nil || reused {
		t.Fatalf("Send = reused %v, err %v", reused, err)
	}
	in := src.Request
	want := run.Request{Worker: in.Worker, Prompt: "more", CWD: in.CWD, Model: in.Model, Effort: in.Effort, Mode: in.Mode}
	hash, _ := run.RequestHash(run.Request{Prompt: "more", CWD: src.ID})
	switch {
	case r.State != run.StateRunning:
		t.Fatalf("state %s, want running", r.State)
	case r.PreviousID == nil || *r.PreviousID != src.ID:
		t.Fatalf("previous_id %v, want %s", r.PreviousID, src.ID)
	case r.Request != want:
		t.Fatalf("request %+v, want %+v", r.Request, want)
	case r.RequestSHA256 != hash:
		t.Fatal("send hash must cover exactly the source id and the message")
	case r.NativeSessionID == nil || *r.NativeSessionID != *src.NativeSessionID:
		t.Fatalf("native_session_id %v, want the source's %s copied", r.NativeSessionID, *src.NativeSessionID)
	case r.CallerID != "c2":
		t.Fatalf("caller %q", r.CallerID)
	}
}

func TestSendDedupeKeyedOnSource(t *testing.T) {
	d := rDeps(t, rModeReady)
	a, b := rDone(t, d), rDone(t, d)
	first, _, err := Send(d, Caller{ID: "c"}, SendRequest{Key: "s1", ID: a.ID, Message: "m"})
	if err != nil {
		t.Fatal(err)
	}
	// The source is gone by the time the lost reply is retried: the key is
	// looked up before the source is read.
	if err := d.Store.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	again, reused, err := Send(d, Caller{ID: "c"}, SendRequest{Key: "s1", ID: a.ID, Message: "m"})
	if err != nil || !reused || again.ID != first.ID {
		t.Fatalf("retry = %v reused %v err %v, want %s reused", again, reused, err, first.ID)
	}
	_, _, err = Send(d, Caller{ID: "c"}, SendRequest{Key: "s1", ID: b.ID, Message: "m"})
	rCode(t, err, run.CodeKeyConflict)
	_, _, err = Send(d, Caller{ID: "c"}, SendRequest{Key: "s1", ID: a.ID, Message: "other"})
	rCode(t, err, run.CodeKeyConflict)
	// A start under a send's key is a different request.
	_, _, err = Start(d, Caller{ID: "c"}, StartRequest{Key: "s1", Worker: "codex", Prompt: "m", CWD: t.TempDir()})
	rCode(t, err, run.CodeKeyConflict)
}

// Concurrent sends on one session: one run, and the other is BUSY because
// the winner's continuation is still in flight.
func TestSendConcurrentOnOneSource(t *testing.T) {
	d := rDeps(t, rModeReady)
	src := rDone(t, d)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			_, _, errs[i] = Send(d, Caller{ID: "c"}, SendRequest{Key: string(rune('a' + i)), ID: src.ID, Message: "m"})
		})
	}
	wg.Wait()
	ok, busy := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case isCode(err, run.CodeBusy):
			busy++
		default:
			t.Fatalf("unexpected %v", err)
		}
	}
	if ok != 1 || busy != 1 {
		t.Fatalf("ok %d busy %d, want 1 and 1", ok, busy)
	}
}

// A continuation whose supervisor never starts must not strand the
// session: a second send to the same source continues it, and it resumes
// the session the source recorded.
func TestSendAfterFailedContinuation(t *testing.T) {
	d := rDeps(t, rModeExit)
	src := rDone(t, d)
	failed, _, err := Send(d, Caller{ID: "c"}, SendRequest{Key: "s1", ID: src.ID, Message: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != run.StateInterrupted || failed.Execution != run.ExecNotStarted {
		t.Fatalf("first continuation %s/%s, want interrupted/not_started", failed.State, failed.Execution)
	}
	_, _, err = Send(d, Caller{ID: "c"}, SendRequest{Key: "s2", ID: failed.ID, Message: "m"})
	rCode(t, err, run.CodeInvalidInput) // it never executed; its source still can be sent to

	rMode(t, &d, rModeReady)
	next, _, err := Send(d, Caller{ID: "c"}, SendRequest{Key: "s3", ID: src.ID, Message: "m again"})
	if err != nil {
		t.Fatalf("send after a failed continuation: %v", err)
	}
	if next.State != run.StateRunning || next.NativeSessionID == nil || *next.NativeSessionID != *src.NativeSessionID {
		t.Fatalf("second continuation %s session %v, want running on %s", next.State, next.NativeSessionID, *src.NativeSessionID)
	}
}

func TestSendPreconditions(t *testing.T) {
	d := rDeps(t, rModeReady)
	sess := "thread-9"
	live := rPut(t, d, &run.Run{State: run.StateRunning, Execution: run.ExecLive, NativeSessionID: &sess})
	lock, err := d.Store.CreateRunLock(live.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	orphan := rPut(t, d, &run.Run{State: run.StateRunning, Execution: run.ExecLive})
	never := rPut(t, d, &run.Run{State: run.StateStarting, Execution: run.ExecNotStarted})
	noSess := rPut(t, d, &run.Run{State: run.StateFailed, Execution: run.ExecExited})
	emptySess := ""
	blankSess := rPut(t, d, &run.Run{State: run.StateFailed, Execution: run.ExecExited, NativeSessionID: &emptySess})
	continued := rDone(t, d)
	rPut(t, d, &run.Run{State: run.StateFailed, Execution: run.ExecExited, PreviousID: &continued.ID, NativeSessionID: continued.NativeSessionID})
	inFlight := rDone(t, d)
	other := "thread-2"
	inFlight.NativeSessionID = &other
	rPut(t, d, inFlight)
	rPut(t, d, &run.Run{State: run.StateRunning, Execution: run.ExecLive, PreviousID: &inFlight.ID, NativeSessionID: &other})
	cwdGone := rDone(t, d)
	gone := "thread-3"
	cwdGone.NativeSessionID = &gone
	rPut(t, d, cwdGone)
	if err := os.Remove(cwdGone.Request.CWD); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, id string
		code     run.Code
	}{
		{"live source", live.ID, run.CodeBusy},
		{"source whose supervisor is gone", orphan.ID, run.CodeBusy},
		{"never executed", never.ID, run.CodeInvalidInput},
		{"no session", noSess.ID, run.CodeInvalidInput},
		{"empty session", blankSess.ID, run.CodeInvalidInput},
		{"session unresolved in a continuation", inFlight.ID, run.CodeBusy},
		{"cwd gone", cwdGone.ID, run.CodeInvalidInput},
		{"unknown id", "zz", run.CodeNotFound},
		{"bad id", "../x", run.CodeNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Send(d, Caller{ID: "c"}, SendRequest{Key: "k-" + c.name, ID: c.id, Message: "m"})
			rCode(t, err, c.code)
		})
	}
	// A source whose continuation already ended can be continued again.
	if _, _, err := Send(d, Caller{ID: "c"}, SendRequest{Key: "k-continued", ID: continued.ID, Message: "m"}); err != nil {
		t.Fatalf("send to an already continued run: %v", err)
	}
	_, _, err = Send(d, Caller{ID: "c"}, SendRequest{Key: "", ID: continued.ID, Message: "m"})
	rCode(t, err, run.CodeInvalidInput)
	_, _, err = Send(d, Caller{ID: "c"}, SendRequest{Key: "k", ID: continued.ID, Message: ""})
	rCode(t, err, run.CodeInvalidInput)
}
