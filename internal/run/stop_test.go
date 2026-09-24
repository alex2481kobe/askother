package run

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// stopAfterFirstLine stops the fake once it has written its first line, so
// scenarios that arm a signal handler first are armed.
func stopAfterFirstLine(t *testing.T, scenario string, grace time.Duration) (bool, Outcome, time.Duration) {
	t.Helper()
	f := startFake(t, "codex", scenario, "")
	first := make(chan struct{})
	res := make(chan bool, 1)
	var took time.Duration
	go func() {
		<-first
		start := time.Now()
		s := Stop(f.w, grace)
		took = time.Since(start)
		res <- s
	}()
	once := false
	o, _, _ := f.wait(t, 0, func([]byte) {
		if !once {
			once = true
			close(first)
		}
	})
	return <-res, o, took
}

func TestStopHang(t *testing.T) {
	t.Parallel()
	s, o, took := stopAfterFirstLine(t, "hang", 5*time.Second)
	if !s || o.Exit.Code != nil || o.Exit.Signal != "SIGTERM" || took > time.Second {
		t.Fatalf("signalled %v after %v, exit %+v", s, took, o.Exit)
	}
}

func TestStopIgnoreTerm(t *testing.T) {
	t.Parallel()
	const grace = 300 * time.Millisecond
	s, o, took := stopAfterFirstLine(t, "ignore_term", grace)
	if !s || o.Exit.Code != nil || o.Exit.Signal != "SIGKILL" || took < grace {
		t.Fatalf("signalled %v after %v, exit %+v", s, took, o.Exit)
	}
}

// A worker that already exited successfully is not signalled; the caller
// decides that the run is done.
func TestStopAfterExit(t *testing.T) {
	t.Parallel()
	f := startFake(t, "codex", "ok", "")
	o, _, _ := f.wait(t, 0, nil)
	if Stop(f.w, time.Second) {
		t.Fatal("Stop reported a signal to a reaped worker")
	}
	if o.Exit.Code == nil || *o.Exit.Code != 0 {
		t.Fatalf("exit %+v", o.Exit)
	}
}

// A TERM that the kernel refuses (the group is already gone, though the
// leader's exit has not been observed yet) is not a delivered signal, so it
// must not make the run read as stopped.
func TestStopReportsUndeliveredTerm(t *testing.T) {
	t.Parallel()
	c := exec.Command("/bin/sh", "-c", "exit 0")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	gone := &Worker{Pgid: c.Process.Pid, exited: make(chan struct{})}
	start := time.Now()
	if Stop(gone, 5*time.Second) {
		t.Fatal("Stop reported a signal to a group that no longer exists")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Stop waited %v for a group it could not signal", took)
	}
}
