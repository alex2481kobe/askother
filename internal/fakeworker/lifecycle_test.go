package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/testutil"
)

// live is a fake started with an owned stdout pipe, as Orca's supervisor
// starts workers. done closes when the process has been reaped.
type live struct {
	cmd     *exec.Cmd
	stdout  *os.File
	lines   *bufio.Reader
	recPath string
	done    chan struct{}
}

func start(t *testing.T, scenario, arg string) *live {
	t.Helper()
	codex, _ := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	l := &live{recPath: filepath.Join(dir, "record.json"), done: make(chan struct{})}
	env := testutil.WithRecord(testutil.Scenario(baseEnv(dir), scenario, arg), l.recPath)
	l.cmd = testutil.GroupCommand(t, codex, env, codexArgv(filepath.Join(dir, "answer.tmp"))...)
	l.cmd.Dir, l.cmd.Stdin = dir, strings.NewReader(testPrompt)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	l.stdout, l.lines = pr, bufio.NewReader(pr)
	l.cmd.Stdout = pw
	if err := l.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	t.Cleanup(func() { pr.Close() })
	go func() { _ = l.cmd.Wait(); close(l.done) }()
	t.Cleanup(func() { // runs before GroupCommand's own kill (cleanups are LIFO)
		l.killGroup()
		if b, err := os.ReadFile(l.recPath); err == nil && bytes.Contains(b, []byte("grandchild_pid")) {
			if gc := testutil.ReadFakeRecord(t, l.recPath).GrandchildPID; gc > 1 {
				_ = syscall.Kill(gc, syscall.SIGKILL)
			}
		}
		select {
		case <-l.done:
		case <-time.After(5 * time.Second):
			t.Error("fake was not reaped")
		}
	})
	return l
}

func (l *live) readLine(t *testing.T) string {
	t.Helper()
	_ = l.stdout.SetReadDeadline(time.Now().Add(5 * time.Second))
	s, err := l.lines.ReadString('\n')
	if err != nil {
		t.Fatalf("read line: %v", err)
	}
	return s
}

func (l *live) exited(within time.Duration) bool {
	select {
	case <-l.done:
		return true
	case <-time.After(within):
		return false
	}
}

func (l *live) signal() syscall.Signal {
	ws, _ := l.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ws.Signaled() {
		return 0
	}
	return ws.Signal()
}

func (l *live) killGroup() { _ = syscall.Kill(-l.cmd.Process.Pid, syscall.SIGKILL) }

func TestHang(t *testing.T) {
	t.Parallel()
	l := start(t, "hang", "")
	l.readLine(t)
	if l.exited(200 * time.Millisecond) {
		t.Fatal("hang exited")
	}
	l.killGroup()
	if !l.exited(2*time.Second) || l.signal() != syscall.SIGKILL {
		t.Fatal("hang did not die of SIGKILL")
	}
}

func TestIgnoreTerm(t *testing.T) {
	t.Parallel()
	l := start(t, "ignore_term", "")
	l.readLine(t) // the trap is armed before the first event
	_ = syscall.Kill(-l.cmd.Process.Pid, syscall.SIGTERM)
	if l.exited(300 * time.Millisecond) {
		t.Fatal("ignore_term died of SIGTERM")
	}
	l.killGroup()
	if !l.exited(2*time.Second) || l.signal() != syscall.SIGKILL {
		t.Fatal("ignore_term did not die of SIGKILL")
	}
	// Control: plain hang dies of TERM.
	h := start(t, "hang", "")
	h.readLine(t)
	_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGTERM)
	if !h.exited(2*time.Second) || h.signal() != syscall.SIGTERM {
		t.Fatal("hang did not die of SIGTERM")
	}
}

func TestSilent(t *testing.T) {
	t.Parallel()
	l := start(t, "silent", "0.4")
	l.readLine(t)
	l.readLine(t) // thread.started, turn.started
	began := time.Now()
	last := l.readLine(t)
	if gap := time.Since(began); gap < 350*time.Millisecond || !strings.Contains(last, "agent_message") {
		t.Fatalf("next event after %v: %s", gap, last)
	}
	if !l.exited(2*time.Second) || l.cmd.ProcessState.ExitCode() != 0 {
		t.Fatal("silent did not finish with 0")
	}
}

func TestCrashMidstream(t *testing.T) {
	t.Parallel()
	codex, _ := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "answer.tmp")
	r := runFakeIn(t, dir, codex, "crash_midstream", "", testPrompt, codexArgv(out)...)
	all := lines(t, r.stdout)
	if r.code != 137 || bytes.HasSuffix(r.stdout, []byte("\n")) || !bytes.HasPrefix(all[len(all)-1], []byte(`{"type":"item.completed"`)) {
		t.Fatalf("code %d tail %q", r.code, all[len(all)-1])
	}
	events(t, bytes.Join(all[:len(all)-1], []byte("\n"))) // everything before the fragment is whole
	noFile(t, out)
	_, claude := testutil.BuildFakeWorker(t)
	r = runFake(t, claude, "crash_midstream", "kill", testPrompt, claudeArgv()...)
	if r.signal != syscall.SIGKILL || bytes.Contains(r.stdout, []byte(`"type":"result"`)) {
		t.Fatalf("kill variant: signal %v", r.signal)
	}
}

func TestStderrFlood(t *testing.T) {
	t.Parallel()
	both(t, "stderr_flood", "", func(t *testing.T, r fakeRun, answer []byte) {
		if r.code != 0 || len(r.stderr) < 2<<20 || string(answer) != "stdout survived the stderr flood" {
			t.Fatalf("code %d stderr %d bytes answer %q", r.code, len(r.stderr), answer)
		}
		if !bytes.HasPrefix(r.stderr, []byte("2026-01-01T00:00:00.000000Z ERROR ")) {
			t.Fatalf("stderr starts %.60q", r.stderr)
		}
	})
}

// A failure before any event: exit 1, nothing on stdout, the reason on stderr.
func TestStderrOnly(t *testing.T) {
	t.Parallel()
	codex, claude := testutil.BuildFakeWorker(t)
	for bin, args := range map[string][]string{codex: codexArgv("/tmp/unused"), claude: claudeArgv()} {
		r := runFake(t, bin, "stderr_only", "", testPrompt, args...)
		if r.code != 1 || len(r.stdout) != 0 || string(r.stderr) != "Error: synthetic startup failure\n" {
			t.Fatalf("%s: code %d stdout %q stderr %q", bin, r.code, r.stdout, r.stderr)
		}
		if r = runFake(t, bin, "stderr_only", "boom", testPrompt, args...); string(r.stderr) != "boom\n" {
			t.Fatalf("%s: arg ignored: %q", bin, r.stderr)
		}
	}
}
