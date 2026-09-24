package lifecycle

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
)

// o-hold-lock stands in for a supervisor: it creates and holds runs/<id>.lock
// until killed or until its stdin closes. args: store root, run id.
func init() {
	registerHelper("o-hold-lock", func(args []string) error {
		s, err := run.Open(args[0])
		if err != nil {
			return err
		}
		f, err := s.CreateRunLock(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		os.Stdout.WriteString("locked\n")
		_, err = io.Copy(io.Discard, os.Stdin)
		return err
	})
}

var oT0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// oClock is an injectable clock for Deps.Now.
type oClock struct{ t time.Time }

func (c *oClock) now() time.Time { return c.t }

func oDeps(t *testing.T) (Deps, string, *oClock) {
	t.Helper()
	root := t.TempDir()
	s, err := run.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	clk := &oClock{t: oT0}
	return Deps{Store: s, Now: clk.now}, root, clk
}

// oRecord builds a valid record in state st. Terminal records end at
// created; done records claim an empty answer (tests that page publish one).
func oRecord(id string, st run.State, ex run.Execution, caller string, created time.Time) *run.Run {
	r := &run.Run{
		SchemaVersion: run.SchemaVersion, ID: id, Key: "k-" + id, RequestSHA256: "h",
		CallerID: caller, CallerSource: run.SourceFallback,
		Request: run.Request{Worker: "codex", Prompt: "p", CWD: "/", Mode: "read-only"},
		State:   st, Execution: ex, CreatedAt: created,
	}
	if run.IsTerminal(st) {
		e := created
		r.EndedAt = &e
	}
	if st == run.StateDone {
		zero := 0
		r.Exit = &run.Exit{Code: &zero}
		r.Result = run.Result{Available: true, Bytes: 0}
	}
	return r
}

func oPut(t *testing.T, d Deps, r *run.Run) {
	t.Helper()
	if err := d.Store.WithIndexLock(func() error { return d.Store.Put(r) }); err != nil {
		t.Fatal(err)
	}
}

func oRead(t *testing.T, d Deps, id string) *run.Run {
	t.Helper()
	r, err := d.Store.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// oFinish publishes text as the answer and commits a done record, as the
// supervisor would.
func oFinish(t *testing.T, d Deps, id, text string) {
	t.Helper()
	tmp := d.Store.AnswerTempPath(id)
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := d.Store.PublishAnswer(id, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Store.Update(id, func(r *run.Run) error {
		zero, end := 0, d.now()
		r.State, r.Execution, r.EndedAt = run.StateDone, run.ExecExited, &end
		r.Exit = &run.Exit{Code: &zero}
		r.Result = run.Result{Available: true, Bytes: n}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// oHoldLock starts a helper process holding id's run lock and returns a
// function that kills it (the supervisor dies).
func oHoldLock(t *testing.T, root, id string) (kill func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], root, id)
	cmd.Env = append(os.Environ(), helperEnv+"=o-hold-lock")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := false
	kill = func() {
		if !done {
			done = true
			cmd.Process.Kill()
			cmd.Wait()
			stdin.Close()
		}
	}
	t.Cleanup(kill)
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "locked\n" {
		kill()
		t.Fatalf("helper did not lock: %q", line)
	}
	return kill
}

// A run whose supervisor died reads interrupted/unknown, and the view is
// never written back: the record on disk still says running.
func TestObserveDeadSupervisorDerivedNotSaved(t *testing.T) {
	d, root, _ := oDeps(t)
	oPut(t, d, oRecord("run-a", run.StateRunning, run.ExecLive, "c", oT0))
	kill := oHoldLock(t, root, "run-a")

	r, err := Observe(d, "run-a")
	if err != nil || r.State != run.StateRunning {
		t.Fatalf("live run: %v %v", r.State, err)
	}
	kill()
	r, err = Observe(d, "run-a")
	if err != nil || r.State != run.StateInterrupted || r.Execution != run.ExecUnknown || r.EndedAt != nil {
		t.Fatalf("dead run: %+v %v", r, err)
	}
	if got := oRead(t, d, "run-a"); got.State != run.StateRunning || got.Execution != run.ExecLive {
		t.Fatalf("observation wrote the record: %s/%s", got.State, got.Execution)
	}
}

func TestObserveLiveInProcessUnchanged(t *testing.T) {
	d, _, _ := oDeps(t)
	lock, err := d.Store.CreateRunLock("run-a")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	oPut(t, d, oRecord("run-a", run.StateStopping, run.ExecLive, "c", oT0))
	if r, err := Observe(d, "run-a"); err != nil || r.State != run.StateStopping {
		t.Fatalf("got %v %v", r, err)
	}
}

func TestObserveStartingWithoutHolderNotStarted(t *testing.T) {
	d, _, _ := oDeps(t)
	oPut(t, d, oRecord("run-a", run.StateStarting, run.ExecNotStarted, "c", oT0))
	r, err := Observe(d, "run-a")
	if err != nil || r.State != run.StateInterrupted || r.Execution != run.ExecNotStarted {
		t.Fatalf("got %+v %v", r, err)
	}
	if got := oRead(t, d, "run-a"); got.State != run.StateStarting {
		t.Fatalf("observation wrote the record: %s", got.State)
	}
}

// Observation needs no write, so it works on a read-only state dir.
func TestObserveReadOnlyDisk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	d, root, _ := oDeps(t)
	oPut(t, d, oRecord("run-a", run.StateRunning, run.ExecLive, "c", oT0))
	for _, dir := range []string{filepath.Join(root, "runs"), root} {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
	}
	r, err := Observe(d, "run-a")
	if err != nil || r.State != run.StateInterrupted || r.Execution != run.ExecUnknown {
		t.Fatalf("got %+v %v", r, err)
	}
	list, err := Status(d, "")
	if err != nil || len(list) != 1 || list[0].State != run.StateInterrupted {
		t.Fatalf("status: %v %v", list, err)
	}
}

// Ids are exact: a prefix is not a run.
func TestObserveFullIDOnly(t *testing.T) {
	d, _, _ := oDeps(t)
	oPut(t, d, oRecord("abc-1", run.StateFailed, run.ExecExited, "c", oT0))
	if r, err := Observe(d, "abc-1"); err != nil || r.ID != "abc-1" {
		t.Fatalf("exact: %v %v", r, err)
	}
	for _, id := range []string{"abc", "abc-", "nope"} {
		if _, err := Observe(d, id); !errors.Is(err, run.CodeNotFound) {
			t.Fatalf("%q: %v, want NOT_FOUND", id, err)
		}
	}
}

func oIDs(runs []*run.Run) []string {
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	return ids
}

// status lists every run, whoever started it, newest first.
func TestStatusListsAllNewestFirst(t *testing.T) {
	d, _, _ := oDeps(t)
	at := func(h int) time.Time { return oT0.Add(time.Duration(h) * time.Hour) }
	oPut(t, d, oRecord("a-failed", run.StateFailed, run.ExecExited, "A", at(3)))
	oPut(t, d, oRecord("a-dead", run.StateRunning, run.ExecLive, "A", at(4))) // no lock holder
	oPut(t, d, oRecord("a-done", run.StateDone, run.ExecExited, "A", at(5)))
	oPut(t, d, oRecord("b-done", run.StateDone, run.ExecExited, "B", at(6)))

	all, err := Status(d, "")
	if want := []string{"b-done", "a-done", "a-dead", "a-failed"}; err != nil || !slices.Equal(oIDs(all), want) {
		t.Fatalf("status %v, want %v (%v)", oIDs(all), want, err)
	}
	if all[2].State != run.StateInterrupted {
		t.Fatalf("listed run not derived: %s", all[2].State)
	}
	one, err := Status(d, "b-done")
	if err != nil || !slices.Equal(oIDs(one), []string{"b-done"}) {
		t.Fatalf("one: %v %v", oIDs(one), err)
	}
}

// A lock that cannot be probed is not evidence of death: listings report
// the stored state rather than recover it.
func TestStatusUnprobeableLockNotRecovered(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	d, _, _ := oDeps(t)
	lock, err := d.Store.CreateRunLock("run-a")
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	oPut(t, d, oRecord("run-a", run.StateRunning, run.ExecLive, "c", oT0))
	if err := os.Chmod(filepath.Join(filepath.Dir(d.Store.AnswerTempPath("x")), "run-a.lock"), 0); err != nil {
		t.Fatal(err)
	}
	list, err := Status(d, "")
	if err != nil || len(list) != 1 || list[0].State != run.StateRunning {
		t.Fatalf("got %v %v", list, err)
	}
	if _, err := Observe(d, "run-a"); !errors.Is(err, run.CodeStoreIO) {
		t.Fatalf("observe: %v", err)
	}
}
