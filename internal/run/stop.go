package run

import (
	"syscall"
	"time"
)

// DefaultStopGrace is the wait between TERM and KILL.
const DefaultStopGrace = 5 * time.Second

// Stop sends TERM to the worker's group, waits up to grace
// (DefaultStopGrace when grace <= 0) for the leader to exit, then sends
// KILL to the group. It does not wait for the exit after KILL; Wait reaps.
//
// It reports whether TERM was delivered. Nothing is sent when the leader
// was already reaped, and a TERM that fails (the group is already gone) is
// not a delivered signal. Signalled does not decide the run's state on its
// own: a worker that finishes its answer anyway may still be done, which
// the caller judges from the exit and the decoder.
func Stop(w *Worker, grace time.Duration) (signalled bool) {
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	select {
	case <-w.exited:
		return false
	default:
	}
	if syscall.Kill(-w.Pgid, syscall.SIGTERM) != nil {
		return false
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-w.exited:
		return true
	case <-timer.C:
	}
	_ = syscall.Kill(-w.Pgid, syscall.SIGKILL) // the group may be gone by now
	return true
}
