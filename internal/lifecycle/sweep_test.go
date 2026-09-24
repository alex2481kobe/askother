package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/run"
)

func oHours(h float64) time.Time { return oT0.Add(time.Duration(h * float64(time.Hour))) }

// oSweepAt runs Sweep with the clock at t and returns the ids still stored.
func oSweepAt(t *testing.T, d Deps, clk *oClock, at time.Time) []string {
	t.Helper()
	clk.t = at
	if err := Sweep(d); err != nil {
		t.Fatalf("sweep at %v: %v", at, err)
	}
	runs, _, err := d.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	return oIDs(runs)
}

// oHeld creates id's run lock and holds it for the rest of the test, as a
// live supervisor would.
func oHeld(t *testing.T, d Deps, id string) {
	t.Helper()
	lock, err := d.Store.CreateRunLock(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
}

// The retention timeline: read runs go 24 h after first_read_at; ended or
// interrupted unread runs 7 days after they ended, or were created when no
// end was recorded; live runs never.
func TestSweepRetentionRules(t *testing.T) {
	d, _, clk := oDeps(t)
	read := oRecord("r1-read", run.StateDone, run.ExecExited, "c", oT0)
	ra := oHours(1)
	read.FirstReadAt = &ra
	oPut(t, d, read)
	oPut(t, d, oRecord("r2-done", run.StateDone, run.ExecExited, "c", oT0))
	oPut(t, d, oRecord("r3-failed", run.StateFailed, run.ExecExited, "c", oT0))
	oPut(t, d, oRecord("r4-notstarted", run.StateStarting, run.ExecNotStarted, "c", oT0)) // lock free: interrupted
	oPut(t, d, oRecord("r5-unknown", run.StateRunning, run.ExecLive, "c", oT0))           // lock free: interrupted
	for _, id := range []string{"r6-running", "r7-starting", "r8-stopping"} {
		oHeld(t, d, id)
	}
	oPut(t, d, oRecord("r6-running", run.StateRunning, run.ExecLive, "c", oT0))
	oPut(t, d, oRecord("r7-starting", run.StateStarting, run.ExecNotStarted, "c", oT0))
	oPut(t, d, oRecord("r8-stopping", run.StateStopping, run.ExecLive, "c", oT0))
	never := []string{"r6-running", "r7-starting", "r8-stopping"}
	unread := append([]string{"r2-done", "r3-failed", "r4-notstarted", "r5-unknown"}, never...)
	all := append([]string{"r1-read"}, unread...)

	for _, step := range []struct {
		at   float64
		want []string
	}{
		{23, all}, {24.9, all}, {25.1, unread}, {167.9, unread}, {168.1, never}, {8760, never},
	} {
		if got := oSweepAt(t, d, clk, oHours(step.at)); !slices.Equal(got, step.want) {
			t.Fatalf("T0+%vh: kept %v, want %v", step.at, got, step.want)
		}
	}
}

func TestSweepSkipsFutureTimestamps(t *testing.T) {
	d, _, clk := oDeps(t)
	b := oRecord("b-read", run.StateDone, run.ExecExited, "c", oT0)
	future := oHours(720) // read while the clock was ahead
	b.FirstReadAt = &future
	oPut(t, d, b)
	// Ended while the clock was ahead, read after it was corrected: the read
	// rule alone would delete it at T0+26h.
	e := oRecord("e-ended", run.StateDone, run.ExecExited, "c", oHours(720))
	eRead := oHours(2)
	e.FirstReadAt = &eRead
	oPut(t, d, e)

	both := []string{"b-read", "e-ended"}
	if got := oSweepAt(t, d, clk, oHours(48)); !slices.Equal(got, both) {
		t.Fatalf("clock behind: kept %v", got)
	}
	if got := oSweepAt(t, d, clk, oHours(743)); !slices.Equal(got, []string{"b-read"}) {
		t.Fatalf("caught up: kept %v", got)
	}
	if got := oSweepAt(t, d, clk, oHours(745)); len(got) != 0 {
		t.Fatalf("read expired: kept %v", got)
	}
}

func TestSweepRemovesAnswerAndKeepsLiveTerminal(t *testing.T) {
	d, _, clk := oDeps(t)
	oDoneRun(t, d, "run-a", "answer")
	lock, err := d.Store.CreateRunLock("run-b") // supervisor still exiting after publishing
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	oPut(t, d, oRecord("run-b", run.StateFailed, run.ExecExited, "c", oT0))
	if got := oSweepAt(t, d, clk, oHours(24*8)); !slices.Equal(got, []string{"run-b"}) {
		t.Fatalf("kept %v", got)
	}
	dir := filepath.Dir(d.Store.AnswerTempPath("x"))
	if _, err := os.Stat(filepath.Join(dir, "run-a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("answer left behind: %v", err)
	}
}

// The sweep does not repair anything: a run lock with no record, left by a
// launcher that died before writing its record, stays.
func TestSweepLeavesOrphanLocks(t *testing.T) {
	d, _, clk := oDeps(t)
	lock, err := d.Store.CreateRunLock("orphan")
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	oSweepAt(t, d, clk, oHours(24*365))
	if _, err := os.Stat(filepath.Join(filepath.Dir(d.Store.AnswerTempPath("x")), "orphan.lock")); err != nil {
		t.Fatalf("orphan lock: %v", err)
	}
}
