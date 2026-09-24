package testutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"
)

// LiveFakes returns the pids of fake worker processes (workers and their
// grandchildren) from this test binary's build that are still alive. Each
// fake holds a flock on live/<pid> in the build dir for its whole life, so a
// lock we cannot take is a live fake; that pid cannot have been reused.
// Entries of dead fakes are removed.
func LiveFakes() []int {
	if buildDir == "" {
		return nil
	}
	dir := filepath.Join(buildDir, "live")
	entries, _ := os.ReadDir(dir)
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			pids = append(pids, pid)
		} else if err == nil {
			_ = os.Remove(path)
		}
		f.Close()
	}
	slices.Sort(pids)
	return pids
}

// RemoveFakeWorker is for TestMain, after m.Run. It gives fakes that are
// already dying 2 s to go, then SIGKILLs any that remain, deletes the build,
// and returns an error naming the survivors. Group cleanup in each test is
// the real mechanism; this is the backstop and the leak detector.
//
//	code := m.Run()
//	if err := testutil.RemoveFakeWorker(); err != nil {
//		fmt.Fprintln(os.Stderr, err)
//		code = 1
//	}
func RemoveFakeWorker() error {
	if buildDir == "" {
		return nil
	}
	leaked := waitGone(2 * time.Second)
	for _, pid := range leaked {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	stuck := waitGone(2 * time.Second)
	_ = os.RemoveAll(buildDir)
	switch {
	case len(stuck) > 0:
		return fmt.Errorf("fake workers outlived their tests and survived SIGKILL: %v (all leaked: %v)", stuck, leaked)
	case len(leaked) > 0:
		return fmt.Errorf("fake workers outlived their tests (killed now): %v", leaked)
	}
	return nil
}

func waitGone(d time.Duration) []int {
	deadline := time.Now().Add(d)
	for {
		pids := LiveFakes()
		if len(pids) == 0 || time.Now().After(deadline) {
			return pids
		}
		time.Sleep(20 * time.Millisecond)
	}
}
