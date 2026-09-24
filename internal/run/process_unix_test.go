package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alex2481kobe/orca/internal/testutil"
	"github.com/alex2481kobe/orca/internal/worker"
)

type fake struct {
	w       *Worker
	recPath string
}

// startFake starts the fake worker through StartWorker. Cleanup kills its
// group if the leader has not been reaped.
func startFake(t *testing.T, dialect, scenario, arg string) fake {
	t.Helper()
	codex, claude := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	rec := filepath.Join(dir, "record.json")
	env := testutil.WithRecord(testutil.Scenario([]string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}, scenario, arg), rec)
	cmd := worker.Command{Path: codex, Args: []string{"exec", "--json", "-"}, Stdin: []byte("synthetic prompt")}
	if dialect == "claude" {
		cmd = worker.Command{Path: claude, Args: []string{"--print", "--output-format", "stream-json", "--verbose"},
			Stdin: []byte("synthetic prompt")}
	}
	w, err := StartWorker(cmd, dir, env)
	if err != nil {
		t.Fatal(err)
	}
	if w.Pgid != w.Pid {
		t.Fatalf("pgid %d, pid %d", w.Pgid, w.Pid)
	}
	t.Cleanup(func() {
		select {
		case <-w.Exited():
		default:
			_ = syscall.Kill(-w.Pgid, syscall.SIGKILL)
			<-w.Exited()
		}
	})
	return fake{w, rec}
}

// wait runs Wait with a hang guard and returns the outcome, the lines, and
// the time from the leader's exit to Wait's return.
func (f fake) wait(t *testing.T, max int, onLine func([]byte)) (Outcome, []string, time.Duration) {
	t.Helper()
	var lines []string
	done := make(chan Outcome, 1)
	go func() {
		done <- f.w.Wait(func(b []byte) error {
			lines = append(lines, string(b))
			if onLine != nil {
				onLine(b)
			}
			return nil
		}, max)
	}()
	var exitedAt time.Time
	select {
	case <-f.w.Exited():
		exitedAt = time.Now()
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not exit")
	}
	select {
	case o := <-done:
		return o, lines, time.Since(exitedAt)
	case <-time.After(10 * time.Second):
		t.Fatal("Wait blocked after the worker exited")
	}
	panic("unreachable")
}

func (f fake) record(t *testing.T) testutil.FakeRecord { return testutil.ReadFakeRecord(t, f.recPath) }

func gone(pid int) bool { return !slices.Contains(testutil.LiveFakes(), pid) }

// The group kill frees a same-group grandchild's hold on stdout at once,
// instead of waiting out the drain bound.
func TestWaitGrandchildHoldsPipe(t *testing.T) {
	t.Parallel()
	f := startFake(t, "codex", "grandchild_holds_pipe", "20s")
	o, lines, d := f.wait(t, 0, nil)
	gc := f.record(t).GrandchildPID
	testutil.KillOnCleanup(t, gc)
	t.Logf("exit to drained: %v", d)
	if d >= time.Second || o.DrainExpired {
		t.Fatalf("drain took %v (expired %v); the group kill should end it well under 1 s", d, o.DrainExpired)
	}
	if o.Exit.Code == nil || *o.Exit.Code != 0 || len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "turn.completed") {
		t.Fatalf("exit %+v, %d lines", o.Exit, len(lines))
	}
	if !eventually(time.Second, func() bool { return gone(gc) }) {
		t.Fatal("grandchild survived the group kill")
	}
}

// A setsid grandchild escapes the group kill; the drain bound ends the wait.
func TestWaitGrandchildSetsid(t *testing.T) {
	t.Parallel()
	f := startFake(t, "claude", "grandchild_setsid", "20s")
	o, lines, d := f.wait(t, 0, nil)
	gc := f.record(t).GrandchildPID
	testutil.KillOnCleanup(t, gc)
	t.Logf("exit to drained: %v", d)
	if !o.DrainExpired || d < DrainBound-100*time.Millisecond || d >= 2500*time.Millisecond {
		t.Fatalf("drain took %v, expired %v; want the %v bound, under 2.5 s", d, o.DrainExpired, DrainBound)
	}
	if o.ReadErr != nil || len(lines) == 0 || !strings.Contains(lines[len(lines)-1], `"type":"result"`) {
		t.Fatalf("read err %v, %d lines: the final event must survive the bound", o.ReadErr, len(lines))
	}
}

// answerOf returns the final answer carried in stdout events: the last
// Codex agent_message or the Claude result.
func answerOf(t *testing.T, lines []string) string {
	t.Helper()
	var answer string
	for _, l := range lines {
		var ev struct {
			Type   string `json:"type"`
			Result string `json:"result"`
			Item   struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("line is not JSON (framing broke it): %v", err)
		}
		switch {
		case ev.Type == "result":
			answer = ev.Result
		case ev.Type == "item.completed" && ev.Item.Type == "agent_message":
			answer = ev.Item.Text
		}
	}
	return answer
}

func TestFramingByteExact(t *testing.T) {
	for _, dialect := range []string{"codex", "claude"} {
		for _, sc := range []struct{ name, arg string }{{"huge_line", "3145728"}, {"split_utf8", ""}} {
			t.Run(dialect+"/"+sc.name, func(t *testing.T) {
				t.Parallel()
				f := startFake(t, dialect, sc.name, sc.arg)
				o, lines, _ := f.wait(t, 0, nil)
				rec := f.record(t)
				if o.ReadErr != nil || rec.Answer == nil {
					t.Fatalf("read err %v, answer %v", o.ReadErr, rec.Answer)
				}
				sum := sha256.Sum256([]byte(answerOf(t, lines)))
				if hex.EncodeToString(sum[:]) != rec.Answer.SHA256 {
					t.Fatal("framed answer differs from the one the fake sent")
				}
				if sc.name == "split_utf8" && rec.SplitWrites == 0 {
					t.Fatal("the fake made no mid-codepoint writes")
				}
			})
		}
	}
}

// An over-cap line fails with PROTOCOL, and the rest of stdout is still
// drained so the worker exits instead of blocking on a full pipe.
func TestFramingTooLarge(t *testing.T) {
	t.Parallel()
	f := startFake(t, "claude", "huge_line", "3145728")
	o, _, _ := f.wait(t, 64<<10, nil)
	if !errors.Is(o.ReadErr, CodeProtocol) {
		t.Fatalf("read err %v, want EVENT_TOO_LARGE", o.ReadErr)
	}
	if o.Exit.Code == nil || *o.Exit.Code != 0 || o.DrainExpired {
		t.Fatalf("exit %+v, expired %v", o.Exit, o.DrainExpired)
	}
}

func TestStderrFlood(t *testing.T) {
	t.Parallel()
	f := startFake(t, "codex", "stderr_flood", "2097152")
	o, lines, _ := f.wait(t, 0, nil)
	tl := o.Stderr
	if !tl.Truncated || len(tl.Text) == 0 || len(tl.Text) > stderrTailMax || !utf8.ValidString(tl.Text) {
		t.Fatalf("tail len %d truncated %v", len(tl.Text), tl.Truncated)
	}
	if !strings.HasSuffix(tl.Text, "\n") || !strings.Contains(tl.Text, "ERROR fake_core::noise") {
		t.Fatal("tail is not the end of the stderr stream")
	}
	if answerOf(t, lines) != "stdout survived the stderr flood" {
		t.Fatal("stdout lost under the stderr flood")
	}
}

func TestExitOfSignal(t *testing.T) {
	t.Parallel()
	f := startFake(t, "codex", "crash_midstream", "kill")
	o, _, _ := f.wait(t, 0, nil)
	if o.Exit.Code != nil || o.Exit.Signal != "SIGKILL" {
		t.Fatalf("exit %+v", o.Exit)
	}
}

// The worker gets exactly env, never the caller's environment, even when
// env is empty, and stdin byte for byte, then EOF.
func TestStartWorkerEnvAndStdin(t *testing.T) {
	t.Setenv("ORCA_RUN_TEST_CALLER_ONLY", "1")
	run := func(cmd worker.Command, env []string) []string {
		w, err := StartWorker(cmd, t.TempDir(), env)
		if err != nil {
			t.Fatal(err)
		}
		o, lines, _ := fake{w: w}.wait(t, 0, nil)
		if o.Exit.Code == nil || *o.Exit.Code != 0 {
			t.Fatalf("%s: exit %+v", cmd.Path, o.Exit)
		}
		return lines
	}
	if got := run(worker.Command{Path: "/usr/bin/env"}, nil); len(got) != 0 {
		t.Fatalf("nil env leaked %d variables", len(got))
	}
	if got := run(worker.Command{Path: "/usr/bin/env"}, []string{"ONLY=1"}); !slices.Equal(got, []string{"ONLY=1"}) {
		t.Fatalf("env %q", got)
	}
	in := strings.Repeat("\u732b line of stdin\n", 20000) // well past a pipe buffer
	got := run(worker.Command{Path: "/bin/cat", Stdin: []byte(in)}, nil)
	if strings.Join(got, "\n")+"\n" != in {
		t.Fatalf("stdin arrived as %d lines", len(got))
	}
}
