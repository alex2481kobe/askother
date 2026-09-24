package main

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/testutil"
)

// The grandchild keeps stdout open after the worker exits; a group kill
// frees it.
func TestGrandchildHoldsPipe(t *testing.T) {
	t.Parallel()
	l := start(t, "grandchild_holds_pipe", "20s")
	if !l.exited(5*time.Second) || l.cmd.ProcessState.ExitCode() != 0 {
		t.Fatal("worker did not exit 0")
	}
	gc := testutil.ReadFakeRecord(t, l.recPath).GrandchildPID
	testutil.KillOnCleanup(t, gc)
	if pgid, err := syscall.Getpgid(gc); err != nil || pgid != l.cmd.Process.Pid {
		t.Fatalf("grandchild %d pgid %d (%v), want %d", gc, pgid, err, l.cmd.Process.Pid)
	}
	if drain(l, 300*time.Millisecond) {
		t.Fatal("stdout reached EOF while the grandchild holds it")
	}
	l.killGroup()
	if !drain(l, 2*time.Second) {
		t.Fatal("stdout still open after the group kill")
	}
}

func TestGrandchildSetsid(t *testing.T) {
	t.Parallel()
	l := start(t, "grandchild_setsid", "20s")
	if !l.exited(5 * time.Second) {
		t.Fatal("worker did not exit")
	}
	gc := testutil.ReadFakeRecord(t, l.recPath).GrandchildPID
	testutil.KillOnCleanup(t, gc)
	if err := syscall.Kill(-l.cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("group kill: %v (the grandchild should have left the group)", err)
	}
	if drain(l, 300*time.Millisecond) {
		t.Fatal("stdout reached EOF while the escaped grandchild holds it")
	}
	_ = syscall.Kill(gc, syscall.SIGKILL)
	if !drain(l, 2*time.Second) {
		t.Fatal("stdout still open after killing the grandchild")
	}
}

// drain reads until EOF (true) or the deadline (false).
func drain(l *live, d time.Duration) bool {
	_ = l.stdout.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 4096)
	for {
		if _, err := l.lines.Read(buf); err != nil {
			return !errors.Is(err, os.ErrDeadlineExceeded)
		}
	}
}

func TestEpipe(t *testing.T) {
	t.Parallel()
	fatal := start(t, "epipe_fatal", "20ms")
	tolerant := start(t, "epipe_tolerant", "20ms")
	for _, l := range []*live{fatal, tolerant} {
		l.readLine(t)
		l.stdout.Close() // the reader goes away, as when a supervisor dies
	}
	if !fatal.exited(2*time.Second) || fatal.signal() != syscall.SIGPIPE {
		t.Fatalf("epipe_fatal did not die of SIGPIPE")
	}
	if tolerant.exited(500 * time.Millisecond) {
		t.Fatal("epipe_tolerant died on a broken pipe")
	}
	if n := testutil.ReadFakeRecord(t, tolerant.recPath).WriteErrors; n < 1 {
		t.Fatalf("epipe_tolerant recorded %d write errors", n)
	}
}
