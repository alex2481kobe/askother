// Package testutil holds helpers shared by Orca's tests.
package testutil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
)

const fakeWorkerPkg = "github.com/alex2481kobe/orca/internal/fakeworker"

var (
	buildOnce sync.Once
	buildDir  string
	buildErr  error
)

// BuildFakeWorker builds internal/fakeworker once per test binary and returns
// two paths to it, named codex and claude, so the argv[0] basename selects the
// dialect. The build lives in a temp dir; call RemoveFakeWorker from TestMain
// to delete it and to fail the package if any fake outlived its test.
func BuildFakeWorker(t testing.TB) (codexPath, claudePath string) {
	t.Helper()
	buildOnce.Do(build)
	if buildErr != nil {
		t.Fatalf("build fake worker: %v", buildErr)
	}
	return filepath.Join(buildDir, "codex"), filepath.Join(buildDir, "claude")
}

func build() {
	buildDir, buildErr = os.MkdirTemp("", "orca-fakeworker-")
	if buildErr != nil {
		return
	}
	if buildErr = os.Mkdir(filepath.Join(buildDir, "live"), 0o700); buildErr != nil {
		return
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	bin := filepath.Join(buildDir, "fakeworker")
	cmd := exec.Command(goBin, "build", "-o", bin, fakeWorkerPkg)
	if _, file, _, ok := runtime.Caller(0); ok {
		cmd.Dir = filepath.Dir(file) // inside the module, whatever the test's cwd
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		buildErr = fmt.Errorf("%v: %s", err, out)
		return
	}
	for _, name := range []string{"codex", "claude"} {
		if buildErr = linkOrCopy(bin, filepath.Join(buildDir, name)); buildErr != nil {
			return
		}
	}
}

func linkOrCopy(src, dst string) error {
	if os.Link(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Scenario returns a copy of env with ORCA_FAKE_SCENARIO set to name and
// ORCA_FAKE_ARG set to arg (removed when arg is empty).
func Scenario(env []string, name, arg string) []string {
	env = setEnv(env, "ORCA_FAKE_SCENARIO", name)
	if arg == "" {
		return unsetEnv(env, "ORCA_FAKE_ARG")
	}
	return setEnv(env, "ORCA_FAKE_ARG", arg)
}

// WithRecord returns a copy of env that makes the fake write its record to path.
func WithRecord(env []string, path string) []string {
	return setEnv(env, "ORCA_FAKE_RECORD", path)
}

// setEnv returns a copy of env with every key= entry replaced by one key=val
// at the end.
func setEnv(env []string, key, val string) []string {
	return append(unsetEnv(env, key), key+"="+val)
}

// unsetEnv returns a copy of env without any key= entries.
func unsetEnv(env []string, key string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// GroupCommand makes a command that runs in its own process group. Once the
// test ends, cleanup SIGKILLs the whole group, so hung workers and their
// same-group children never outlive the test. It does not reap: callers that
// Start must Wait.
func GroupCommand(t testing.TB, path string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	return cmd
}

// KillOnCleanup SIGKILLs pid when the test ends, for processes that escaped
// the group (such as the grandchild_setsid scenario's child). A pid of 1 or
// less is ignored: kill(0) or kill(-1) would hit the test itself.
func KillOnCleanup(t testing.TB, pid int) {
	if pid <= 1 {
		return
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
}
