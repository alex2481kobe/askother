package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/lifecycle"
	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/worker"
)

var t0 = time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)

// kRecord is a valid record in state st. Terminal records end 4m12s after
// they start.
func kRecord(id string, st run.State) *run.Run {
	r := &run.Run{
		SchemaVersion: run.SchemaVersion, ID: id, Key: "k-" + id, RequestSHA256: "00",
		CallerID: "caller-k", CallerSource: run.SourceFallback,
		Request: run.Request{Worker: "codex", Prompt: "p", CWD: "/", Mode: "read-only"},
		State:   st, Execution: run.ExecLive, CreatedAt: t0,
	}
	if !run.IsTerminal(st) {
		return r
	}
	end := t0.Add(4*time.Minute + 12*time.Second)
	ms := int64(252_000)
	r.EndedAt, r.DurationMS, r.Execution = &end, &ms, run.ExecExited
	code := 0
	switch st {
	case run.StateDone:
		r.Result = run.Result{Available: true, Bytes: 2}
	case run.StateFailed:
		code = 1
		r.Error = &worker.Failure{Code: string(run.CodeWorkerFailed), Message: "codex: not logged in"}
	case run.StateStopped:
		sig := "SIGTERM"
		r.Exit = &run.Exit{Signal: &sig}
		return r
	case run.StateInterrupted:
		// Only ever a view: the record of a lost supervisor, as read.
		r.Execution, r.EndedAt, r.DurationMS = run.ExecUnknown, nil, nil
		return r
	}
	r.Exit = &run.Exit{Code: &code}
	return r
}

func kDeps(t *testing.T, runs ...*run.Run) lifecycle.Deps {
	t.Helper()
	st, err := run.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if err := st.WithIndexLock(func() error { return st.Put(r) }); err != nil {
			t.Fatal(err)
		}
	}
	return lifecycle.Deps{Store: st}
}

// kHoldLock keeps a run live for the test's duration.
func kHoldLock(t *testing.T, d lifecycle.Deps, id string) {
	t.Helper()
	f, err := d.Store.CreateRunLock(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
}

// Ids are "YYYYMMDDTHHMMSSZ-" plus 32 hex digits in real use; the store
// checks only the charset, so short synthetic ids keep lines readable.
const idA, idB = "20260923T100000Z-aaaa", "20260923T100000Z-bbbb"

func TestStatusLine(t *testing.T) {
	now := t0.Add(90 * time.Second)
	withLabels := kRecord(idA, run.StateDone)
	withLabels.Request.Role, withLabels.Request.Task = "scoper", "scope \"auth\"\nbug"
	longFail := kRecord(idA, run.StateFailed)
	longFail.Error.Message = "line one\n\tline two " + strings.Repeat("x", 300)
	running := kRecord(idA, run.StateRunning)
	started := t0.Add(400 * time.Millisecond)
	fast := kRecord(idA, run.StateRunning)
	fast.StartedAt, fast.CreatedAt = &started, t0

	cases := []struct {
		name string
		r    *run.Run
		now  time.Time
		want string
	}{
		{"done", withLabels, now, `askother: run ` + idA + ` done (exit 0) 4m12s scoper "scope \"auth\"\nbug" -> result ` + idA},
		{"failed", kRecord(idA, run.StateFailed), now, `askother: run ` + idA + ` failed (exit 1) 4m12s: WORKER_FAILED: codex: not logged in -> result ` + idA},
		{"stopped", kRecord(idA, run.StateStopped), now, `askother: run ` + idA + ` stopped (signal SIGTERM) 4m12s -> result ` + idA},
		{"interrupted", kRecord(idA, run.StateInterrupted), now, `askother: run ` + idA + ` interrupted (execution unknown) 1m30s -> result ` + idA},
		{"running", running, now, `askother: run ` + idA + ` running 1m30s`},
		{"subsecond", fast, t0.Add(750 * time.Millisecond), `askother: run ` + idA + ` running 350ms`},
		{"long error", longFail, now, `askother: run ` + idA + ` failed (exit 1) 4m12s: WORKER_FAILED: line one line two ` + strings.Repeat("x", 182) + `... -> result ` + idA},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := statusLine(c.r, c.now); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestParseWaitArgs(t *testing.T) {
	w, err := parseWaitArgs([]string{"a", "--timeout", "90s", "b", "--any", "--", "--c"})
	if err != nil || !w.any || w.timeout != 90*time.Second || strings.Join(w.ids, ",") != "a,b,--c" {
		t.Fatalf("got %+v, %v", w, err)
	}
	if w, err := parseWaitArgs([]string{"--timeout=2m", "a"}); err != nil || w.timeout != 2*time.Minute || w.any {
		t.Fatalf("got %+v, %v", w, err)
	}
	for _, bad := range [][]string{nil, {"--any"}, {"a", "--timeout"}, {"a", "--timeout", "0s"}, {"a", "--timeout", "soon"}, {"a", "-x"}} {
		if _, err := parseWaitArgs(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}

func waitCode(t *testing.T, ctx context.Context, d lifecycle.Deps, w waitArgs) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := waitRuns(ctx, d, w, &out, &errb)
	return code, out.String(), errb.String()
}

func TestWaitRunsOutcomes(t *testing.T) {
	bg := context.Background()
	cases := []struct {
		name  string
		runs  []*run.Run
		want  int
		lines int
	}{
		{"done", []*run.Run{kRecord(idA, run.StateDone)}, 0, 1},
		{"failed beats stopped", []*run.Run{kRecord(idA, run.StateStopped), kRecord(idB, run.StateFailed)}, 1, 2},
		{"stopped", []*run.Run{kRecord(idA, run.StateStopped), kRecord(idB, run.StateDone)}, 2, 2},
		// A running record with a free lock is a lost supervisor: interrupted.
		{"interrupted beats failed", []*run.Run{kRecord(idA, run.StateFailed), kRecord(idB, run.StateRunning)}, 3, 2},
		{"orphan", []*run.Run{kRecord(idA, run.StateRunning)}, 3, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := kDeps(t, c.runs...)
			ids := make([]string, len(c.runs))
			for i, r := range c.runs {
				ids[i] = r.ID
			}
			code, out, _ := waitCode(t, bg, d, waitArgs{ids: ids})
			if code != c.want || strings.Count(out, "\n") != c.lines {
				t.Fatalf("exit %d, want %d; out:\n%s", code, c.want, out)
			}
		})
	}
}

func TestWaitRunsWaiterCodes(t *testing.T) {
	d := kDeps(t, kRecord(idA, run.StateRunning), kRecord(idB, run.StateDone))
	kHoldLock(t, d, idA)

	if code, _, errs := waitCode(t, context.Background(), d, waitArgs{ids: []string{"20260923T100000Z-cccc"}}); code != exitRefused || !strings.Contains(errs, "NOT_FOUND") {
		t.Errorf("unknown id: exit %d %q", code, errs)
	}
	if code, _, errs := waitCode(t, context.Background(), d, waitArgs{ids: []string{"20260923T100000Z-"}}); code != exitRefused || !strings.Contains(errs, "NOT_FOUND") {
		t.Errorf("id prefix: exit %d %q; ids are exact", code, errs)
	}
	start := time.Now()
	code, out, _ := waitCode(t, context.Background(), d, waitArgs{ids: []string{idA}, timeout: 300 * time.Millisecond})
	if code != exitTimeout || !strings.HasPrefix(out, "askother: run "+idA+" running ") || time.Since(start) > 2*time.Second {
		t.Errorf("timeout: exit %d %q", code, out)
	}
	code, out, _ = waitCode(t, context.Background(), d, waitArgs{ids: []string{idA, idB}, any: true})
	if code != 0 || strings.Count(out, "\n") != 2 {
		t.Errorf("--any: exit %d %q", code, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if code, out, _ := waitCode(t, ctx, d, waitArgs{ids: []string{idA}}); code != exitInterrupted || out != "" {
		t.Errorf("signal: exit %d %q", code, out)
	}
}

// Wait loops in slices: a run that ends after the first slice is still seen.
func TestWaitRunsAcrossSlices(t *testing.T) {
	defer func(s time.Duration) { waitSlice = s }(waitSlice)
	waitSlice = 150 * time.Millisecond
	d := kDeps(t, kRecord(idA, run.StateRunning))
	lock, err := d.Store.CreateRunLock(idA)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(500*time.Millisecond, func() {
		_ = d.Store.WithIndexLock(func() error { return d.Store.Put(kRecord(idA, run.StateDone)) })
		lock.Close()
	})
	if code, out, _ := waitCode(t, context.Background(), d, waitArgs{ids: []string{idA}}); code != 0 || !strings.Contains(out, " done ") {
		t.Fatalf("exit %d %q", code, out)
	}
}

// Observation never needs a write: with the state dir read-only, wait
// still answers with the derived view.
func TestWaitRunsReadOnlyStore(t *testing.T) {
	home := t.TempDir()
	st, err := run.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WithIndexLock(func() error { return st.Put(kRecord(idA, run.StateRunning)) }); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(home, "index.lock"))
	for _, p := range []string{filepath.Join(home, "runs"), home} {
		if err := os.Chmod(p, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(p, 0o700) })
	}
	code, out, errs := waitCode(t, context.Background(), lifecycle.Deps{Store: st}, waitArgs{ids: []string{idA}})
	if code != 3 || !strings.Contains(out, " interrupted ") {
		t.Fatalf("exit %d %q %q", code, out, errs)
	}
	if r, err := st.Read(idA); err != nil || r.State != run.StateRunning {
		t.Fatalf("record changed on a read-only store: %v %v", r.State, err)
	}
}
