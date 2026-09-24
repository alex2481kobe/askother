package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/worker"
)

// Stand-in supervisors for the launcher tests, so admission is tested
// without a worker. Each is started by Start as `<Self> supervise <id>`,
// where Self is a symlink to the test binary named after the mode, so
// `pgrep -f r-standin` finds them.
const (
	rModeReady  = "r-standin-supervisor" // running/live, signals ready, sleeps
	rModeExit   = "r-standin-exit"       // exits without signalling
	rModeSilent = "r-standin-silent"     // never signals, sleeps

	rStoreEnv = "ASKOTHER_R_TEST_STORE"
	rPidsEnv  = "ASKOTHER_R_TEST_PIDS"
)

func init() {
	registerHelper(rModeReady, func(args []string) error { return rStandin(args, true, true) })
	registerHelper(rModeExit, func(args []string) error { return rStandin(args, false, false) })
	registerHelper(rModeSilent, func(args []string) error { return rStandin(args, false, true) })
}

func rStandin(args []string, ready, sleep bool) error {
	lock := os.NewFile(run.LockFD, "lock")
	run.CloseOnExecInherited(run.LockFD, run.ReadyFD)
	defer runtime.KeepAlive(lock) // a collected *os.File closes fd 3
	if len(args) != 2 || args[0] != "supervise" {
		return errors.New("usage: supervise <id>")
	}
	pid := os.Getpid()
	pidFile := filepath.Join(os.Getenv(rPidsEnv), strconv.Itoa(pid))
	if err := os.WriteFile(pidFile, nil, 0o600); err != nil {
		return err
	}
	if !sleep {
		return nil
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	if ready {
		st, err := run.Open(os.Getenv(rStoreEnv))
		if err != nil {
			return err
		}
		_, err = st.Update(args[1], func(r *run.Run) error {
			now := time.Now().UTC()
			r.State, r.Execution, r.StartedAt = run.StateRunning, run.ExecLive, &now
			r.Runtime.SupervisorPID = &pid
			return nil
		})
		if err != nil {
			return err
		}
		ready := os.NewFile(run.ReadyFD, "ready")
		if _, err := ready.Write([]byte{1}); err != nil {
			return err
		}
		ready.Close()
	}
	<-sig
	return nil
}

// rDeps returns Deps on a fresh store whose Self runs stand-in mode. The
// config file names a script as both workers' binary. Every stand-in is
// killed on cleanup.
func rDeps(t *testing.T, mode string) Deps {
	t.Helper()
	root, bin, pids := t.TempDir(), t.TempDir(), t.TempDir()
	st, err := run.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(bin, "fake-worker")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(rStoreEnv, root)
	t.Setenv(rPidsEnv, pids)
	t.Cleanup(func() { rKillStandins(t, pids) })
	d := Deps{Store: st, ConfigPath: filepath.Join(bin, "config.json"), StateHome: root,
		Env:      map[string]string{"HOME": root, "PATH": "/usr/bin:/bin"},
		Adapters: map[string]worker.Adapter{"codex": worker.Codex{}, "claude": worker.Claude{}}}
	rConfig(t, d, fake)
	rMode(t, &d, mode)
	return d
}

// rMode makes d's Self run stand-in mode from now on.
func rMode(t *testing.T, d *Deps, mode string) {
	t.Helper()
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	d.Self = filepath.Join(filepath.Dir(d.ConfigPath), mode)
	if err := os.Symlink(exe, d.Self); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	t.Setenv(helperEnv, mode)
}

// rConfig writes d's config file with binary for both workers.
func rConfig(t *testing.T, d Deps, binary string) {
	t.Helper()
	body := fmt.Sprintf(`{"workers":{"codex":{"binary":%q},"claude":{"binary":%q}}}`, binary, binary)
	if err := os.WriteFile(d.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// rBinary is the worker binary d's config names.
func rBinary(d Deps) string { return filepath.Join(filepath.Dir(d.ConfigPath), "fake-worker") }

func rKillStandins(t *testing.T, dir string) {
	entries, _ := os.ReadDir(dir)
	var pids []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			pids = append(pids, pid)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Errorf("stand-in %d still alive", pid)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// rPut writes a record directly, as a finished or foreign run.
func rPut(t *testing.T, d Deps, r *run.Run) *run.Run {
	t.Helper()
	if r.ID == "" {
		id, err := run.NewID()
		if err != nil {
			t.Fatal(err)
		}
		r.ID = id
	}
	r.SchemaVersion = run.SchemaVersion
	if r.Key == "" {
		r.Key = "key-" + r.ID
	}
	if r.Request.Worker == "" {
		r.Request = run.Request{Worker: "codex", Prompt: "p", CWD: t.TempDir(), Mode: "workspace-write", Model: "m1", Effort: "high"}
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if run.IsTerminal(r.State) && r.EndedAt == nil {
		now := time.Now().UTC()
		r.EndedAt = &now
	}
	if r.State == run.StateDone {
		code := 0
		r.Exit = &run.Exit{Code: &code}
		r.Result = run.Result{Available: true}
	}
	if err := d.Store.WithIndexLock(func() error { return d.Store.Put(r) }); err != nil {
		t.Fatal(err)
	}
	return r
}

// rDone is a finished Codex run with a native session.
func rDone(t *testing.T, d Deps) *run.Run {
	sess := "thread-1"
	return rPut(t, d, &run.Run{State: run.StateDone, Execution: run.ExecExited, NativeSessionID: &sess})
}

func rCode(t *testing.T, err error, want run.Code) {
	t.Helper()
	var e *run.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("err = %v, want %s", err, want)
	}
}
