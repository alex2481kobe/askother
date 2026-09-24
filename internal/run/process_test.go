package run

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/testutil"
	"github.com/alex2481kobe/orca/internal/worker"
)

// The test binary doubles as a launcher and a supervisor, selected by env.
const (
	helperEnv    = "ORCA_RUN_TEST_HELPER" // "launcher" or "supervisor"
	helperLock   = "ORCA_RUN_TEST_LOCK"   // run lock path
	helperWorker = "ORCA_RUN_TEST_WORKER" // fake codex path; supervisor starts it hanging
	helperOut    = "ORCA_RUN_TEST_OUT"    // file the supervisor writes the worker pid to
	helperNoCOE  = "ORCA_RUN_TEST_NO_CLOEXEC"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(helperMain(mode))
	}
	code := m.Run()
	if err := testutil.RemoveFakeWorker(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		code = 1
	}
	os.Exit(code)
}

func helperMain(mode string) int {
	var err error
	switch mode {
	case "launcher":
		err = helperLaunch()
	case "supervisor":
		err = helperSupervise()
	default:
		err = fmt.Errorf("unknown helper mode %q", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		return 1
	}
	return 0
}

// helperLaunch plays the launcher (run lock, detached start, readiness
// wait), then prints
// "<readiness> <supervisor pid>" and exits without reaping.
func helperLaunch() error {
	lock, err := os.OpenFile(os.Getenv(helperLock), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	readyR, readyW, err := NewReadiness()
	if err != nil {
		return err
	}
	os.Setenv(helperEnv, "supervisor")
	cmd, err := StartDetached(os.Args[0], nil, []*os.File{lock, readyW})
	if err != nil {
		return err
	}
	lock.Close()
	readyW.Close()
	r, err := WaitReady(readyR, 10*time.Second)
	fmt.Printf("%s %d\n", r, cmd.Process.Pid)
	return err
}

// helperSupervise plays the supervisor (close-on-exec, start a worker,
// signal readiness), then sleeps.
func helperSupervise() error {
	if os.Getenv(helperNoCOE) == "" {
		CloseOnExecInherited(LockFD, ReadyFD)
	}
	if fake := os.Getenv(helperWorker); fake != "" {
		dir := filepath.Dir(os.Getenv(helperOut))
		env := testutil.Scenario([]string{"PATH=/usr/bin:/bin", "HOME=" + dir}, "hang", "")
		w, err := StartWorker(worker.Command{Path: fake, Args: []string{"exec", "--json", "-"},
			Stdin: []byte("synthetic prompt")}, dir, env)
		if err != nil {
			return err
		}
		// The hang fake writes "working" and then never again; wait for it, so
		// killing us later cannot make its output die of SIGPIPE.
		working := make(chan struct{})
		go w.Wait(func(line []byte) error {
			if strings.Contains(string(line), "working") {
				close(working)
			}
			return nil
		}, 0)
		<-working
		if err := os.WriteFile(os.Getenv(helperOut), []byte(strconv.Itoa(w.Pid)), 0o600); err != nil {
			return err
		}
	}
	ready := os.NewFile(ReadyFD, "ready")
	if _, err := ready.Write([]byte{1}); err != nil {
		return err
	}
	ready.Close()
	time.Sleep(60 * time.Second)
	return nil
}

// launch runs the launcher helper in its own group and returns the detached
// supervisor's pid, which the test kills on cleanup.
func launch(t *testing.T, extraEnv ...string) (lockPath string, supPid int) {
	t.Helper()
	dir := t.TempDir()
	lockPath = filepath.Join(dir, "run.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), helperEnv+"=launcher", helperLock+"="+lockPath, helperOut+"="+filepath.Join(dir, "worker.pid"))
	cmd := testutil.GroupCommand(t, os.Args[0], append(env, extraEnv...))
	start := time.Now()
	out, err := cmd.Output() // returns at the launcher's exit: the supervisor holds none of its stdio
	if err != nil {
		t.Fatalf("launcher: %v", err)
	}
	var ready string
	if _, err := fmt.Sscanf(string(out), "%s %d", &ready, &supPid); err != nil || ready != "ready" {
		t.Fatalf("launcher said %q (%v)", out, err)
	}
	testutil.KillOnCleanup(t, supPid)
	t.Logf("launch to readiness: %v", time.Since(start))
	return lockPath, supPid
}

// lockHeld probes like an observer: a non-blocking shared lock.
func lockHeld(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	return false
}

// eventually polls cond for up to d.
func eventually(d time.Duration, cond func() bool) bool {
	for end := time.Now().Add(d); ; time.Sleep(10 * time.Millisecond) {
		if cond() {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}

// The supervisor keeps the run lock after the launcher exits, closes its
// copy without LOCK_UN, and even after the launcher's whole group is killed
// (a harness may kill it on exit). Also logs the helper's RSS, for
// information only.
func TestDetachedSurvivesLauncher(t *testing.T) {
	lockPath, sup := launch(t)
	sid, err := supervisorSessionID(sup)
	if err != nil || sid != sup {
		t.Fatalf("supervisor sid %d (%v), want its own session %d", sid, err, sup)
	}
	time.Sleep(100 * time.Millisecond)
	if !lockHeld(t, lockPath) {
		t.Fatal("run lock free while the detached supervisor lives")
	}
	if out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(sup)).Output(); err == nil {
		t.Logf("detached helper RSS: %s KB (a test binary, not the real supervisor)", strings.TrimSpace(string(out)))
	}
	_ = syscall.Kill(sup, syscall.SIGKILL)
	if !eventually(2*time.Second, func() bool { return !lockHeld(t, lockPath) }) {
		t.Fatal("run lock still held after the supervisor died")
	}
}

// With its negative control: without CloseOnExecInherited the worker
// inherits fd 3 and keeps a dead supervisor's lock alive.
func TestCloseOnExecReleasesLock(t *testing.T) {
	codex, _ := testutil.BuildFakeWorker(t)
	for _, tc := range []struct {
		name     string
		env      []string
		wantHeld bool
	}{
		{"cloexec", nil, false},
		{"no_cloexec", []string{helperNoCOE + "=1"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lockPath, sup := launch(t, append(tc.env, helperWorker+"="+codex)...)
			b, err := os.ReadFile(filepath.Join(filepath.Dir(lockPath), "worker.pid"))
			if err != nil {
				t.Fatal(err)
			}
			wpid, _ := strconv.Atoi(string(b))
			if wpid <= 1 {
				t.Fatalf("worker pid %q", b)
			}
			t.Cleanup(func() { _ = syscall.Kill(-wpid, syscall.SIGKILL) })
			_ = syscall.Kill(sup, syscall.SIGKILL)
			released := eventually(time.Second, func() bool { return !lockHeld(t, lockPath) })
			if released == tc.wantHeld {
				t.Fatalf("lock held=%v after the supervisor died, want %v", !released, tc.wantHeld)
			}
			if err := syscall.Kill(wpid, 0); err != nil {
				t.Fatalf("worker should outlive its supervisor: %v", err)
			}
		})
	}
}

func TestWaitReady(t *testing.T) {
	r, w, _ := NewReadiness()
	defer r.Close()
	if got, err := WaitReady(r, 50*time.Millisecond); got != ReadyTimeout || err != nil {
		t.Fatalf("open, silent writer: %v %v", got, err)
	}
	w.Write([]byte{1})
	if got, err := WaitReady(r, time.Second); got != Ready || err != nil {
		t.Fatalf("after the byte: %v %v", got, err)
	}
	w.Close()
	if got, err := WaitReady(r, time.Second); got != ReadyEOF || err != nil {
		t.Fatalf("closed without a byte: %v %v", got, err)
	}
}
