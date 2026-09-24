package main

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// liveFile stays referenced so it is never collected and closed before exit.
var liveFile *os.File

// registerLive lets the test harness find fakes that outlive their tests.
// When a "live" directory sits next to the executable (BuildFakeWorker makes
// one), the process creates live/<pid> and holds an exclusive flock on it
// until it dies. The kernel drops the lock however the process dies, and Go
// opens files close-on-exec, so no other process inherits it: a held lock
// means that exact pid is a live fake. No process-table scan is needed.
func registerLive() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	path := filepath.Join(filepath.Dir(exe), "live", strconv.Itoa(os.Getpid()))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return // not built by the harness
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return
	}
	liveFile = f
}
