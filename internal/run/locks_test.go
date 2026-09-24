package run

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestRunLockHelperProcess is not a test: it is the child process started by
// lockHelper. ORCA_LOCK_HELPER selects what it does with the lock on fd 3:
//   - hold: keep the inherited lock (like a supervisor) until killed
//   - observe: loop taking and dropping a shared lock (like a status reader)
func TestRunLockHelperProcess(t *testing.T) {
	mode := os.Getenv("ORCA_LOCK_HELPER")
	if mode == "" {
		return
	}
	f := os.NewFile(3, "lock")
	os.Stdout.WriteString("ready\n")
	for mode == "observe" {
		syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
		time.Sleep(500 * time.Microsecond)
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

// lockHelper starts the test binary as a helper holding lock as fd 3 and
// waits until it is running. Cleanup kills and reaps it.
func lockHelper(t *testing.T, mode string, lock *os.File) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunLockHelperProcess$")
	cmd.Env = append(os.Environ(), "ORCA_LOCK_HELPER="+mode)
	cmd.ExtraFiles = []*os.File{lock}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper not ready: %q, %v", line, err)
	}
	return cmd
}

func TestCreateRunLockIsExclusiveAndPrivate(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	lock, err := s.CreateRunLock(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if got := mode(t, s.file(id, ".lock")); got != 0o600 {
		t.Errorf("lock mode %v, want 0600", got)
	}
	if _, err := s.CreateRunLock(id); !errors.Is(err, CodeStoreIO) {
		t.Fatalf("second CreateRunLock: %v, want STORE_IO (O_EXCL)", err)
	}
	if live, err := s.ProbeLive(id); err != nil || !live {
		t.Fatalf("held lock probes live=%v, %v", live, err)
	}
}

// A missing lock file is never recreated: a probe must not bring an
// abandoned run back.
func TestProbeLiveNeverCreatesTheLock(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	if live, err := s.ProbeLive(id); err != nil || live {
		t.Fatalf("missing lock: live=%v, %v", live, err)
	}
	if _, err := os.Stat(s.file(id, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe created the lock file: %v", err)
	}
}

// The kernel releases the run lock when its holder dies. The helper
// inherits the lock the way a supervisor does, and the parent closes its copy.
func TestRunLockReleasedWhenHolderIsKilled(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	lock, err := s.CreateRunLock(id)
	if err != nil {
		t.Fatal(err)
	}
	cmd := lockHelper(t, "hold", lock)
	lock.Close() // the launcher closes its copy, never LOCK_UN
	if live, err := s.ProbeLive(id); err != nil || !live {
		t.Fatalf("while the child holds it: live=%v, %v", live, err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if live, err := s.ProbeLive(id); err != nil || live {
		t.Fatalf("after the child was killed: live=%v, %v", live, err)
	}
}

// An exclusive probe read running 4 times in 200 when readers overlapped.
// Here another process keeps taking and dropping a shared lock while
// 200 probes run from 8 goroutines; none may report the ownerless run live.
func TestProbeLiveNeverFalselyLiveAcross200Probes(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	lock, err := s.CreateRunLock(id)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close() // ownerless: the supervisor is gone
	observer, err := os.Open(s.file(id, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	lockHelper(t, "observe", observer)

	var falseLive, errs sync.Map
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 25 {
				live, err := s.ProbeLive(id)
				if err != nil {
					errs.Store([2]int{g, i}, err)
				}
				if live {
					falseLive.Store([2]int{g, i}, true)
				}
			}
		})
	}
	wg.Wait()
	n := 0
	falseLive.Range(func(any, any) bool { n++; return true })
	errs.Range(func(k, v any) bool { t.Errorf("probe %v: %v", k, v); return true })
	if n != 0 {
		t.Fatalf("%d of 200 probes falsely reported live", n)
	}
}
