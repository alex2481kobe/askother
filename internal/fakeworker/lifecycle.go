package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// lifecycle holds the process-behavior scenarios that exercise the
// supervisor: silence, hangs, signals, crashes, grandchildren, pipes.
func lifecycle(name, arg string, w worker, e *emitter, rec *record) (int, bool) {
	switch name {
	case "silent":
		w.start()
		time.Sleep(parseDur(arg, time.Second))
		return w.finish("finished after a silence"), true
	case "hang":
		w.start()
		w.progress("working")
		sleepForever()
	case "ignore_term":
		signal.Ignore(syscall.SIGTERM) // before any output, so a reader knows it is armed
		w.start()
		w.progress("ignoring SIGTERM")
		sleepForever()
	case "crash_midstream":
		w.start()
		w.progress("halfway there")
		w.partial()
		if arg == "kill" {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			time.Sleep(time.Second)
		}
		return 137, true
	case "grandchild_holds_pipe", "grandchild_setsid":
		w.start()
		pid, err := spawnGrandchild(name == "grandchild_setsid", arg)
		if err != nil {
			return misuse("spawn grandchild: " + err.Error()), true
		}
		rec.GrandchildPID = pid
		rec.save()
		return w.finish("parent finished; a grandchild still holds stdout"), true
	case "stderr_flood":
		n := parseSize(arg, 2<<20)
		flood(n / 2)
		w.start()
		w.progress("still fine")
		flood(n - n/2)
		return w.finish("stdout survived the stderr flood"), true
	case "epipe_fatal", "epipe_tolerant":
		return epipe(name == "epipe_tolerant", parseDur(arg, 50*time.Millisecond), w, e, rec), true
	}
	return 0, false
}

// spawnGrandchild starts a copy of this program that sleeps while holding
// our stdout and stderr. Without setsid it stays in our process group, so a
// group kill reaches it; with setsid it escapes the group.
func spawnGrandchild(setsid bool, arg string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	if arg == "" {
		arg = "30s"
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), envChild+"="+arg)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: setsid}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	return pid, cmd.Process.Release()
}

// flood writes at least n bytes of log lines shaped like Codex's stderr
// ERROR lines, which appear on successful runs too.
func flood(n int) {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "2026-01-01T00:00:00.%06dZ ERROR fake_core::noise: synthetic stderr line %d\n", i%1000000, i)
		if b.Len() >= 64<<10 {
			os.Stderr.WriteString(b.String())
			n -= b.Len()
			b.Reset()
		}
	}
	os.Stderr.WriteString(b.String())
}

// epipe writes a progress event every interval for up to 400 ticks. The
// fatal variant keeps Go's default: a write to a broken stdout raises
// SIGPIPE and kills the process. The tolerant variant ignores SIGPIPE and
// write errors and keeps running, like a runtime with a stdout error
// handler.
func epipe(tolerant bool, interval time.Duration, w worker, e *emitter, rec *record) int {
	if tolerant {
		signal.Ignore(syscall.SIGPIPE)
		e.tolerant = true
	}
	w.start()
	seen := 0
	for i := range 400 {
		time.Sleep(interval)
		w.progress(fmt.Sprintf("tick %d", i))
		if e.writeErrors != seen {
			seen = e.writeErrors
			rec.WriteErrors = seen
			rec.save()
		}
	}
	return w.finish("ticks done")
}
