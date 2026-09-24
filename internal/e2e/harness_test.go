package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/testutil"
)

// env is one test's AskOther installation: its own ASKOTHER_HOME, config and
// worker wrappers.
type env struct {
	t    *testing.T
	dir  string
	home string
	base []string // environment of every askother process the test starts
	work int
}

// wrapper is a /bin/sh stand-in for a worker CLI. The worker environment is
// an allowlist that drops ASKOTHER_FAKE_*, so the scenario comes from files in
// the run's cwd, and the fake records into that cwd. The wrapper execs the
// fake, so $$ is the worker pid the run record holds: one file per run.
const wrapper = `#!/bin/sh
s=$(cat .askother-scenario 2>/dev/null)
export ASKOTHER_FAKE_SCENARIO="${s:-ok}"
if [ -f .askother-arg ]; then export ASKOTHER_FAKE_ARG="$(cat .askother-arg)"; fi
export ASKOTHER_FAKE_RECORD="$PWD/rec-$$.json"
exec %q "$@"
`

// minimalPath is the only PATH askother processes get. Worker binaries come
// only from the temp config: an inherited PATH could resolve a real, paid
// CLI. Note config.Resolve also searches /opt/homebrew/bin and
// /usr/local/bin when no binary is configured, so every env configures both.
const minimalPath = "/usr/bin:/bin"

// newEnv sets up an installation.
func newEnv(t *testing.T) *env {
	return newEnvWith(t, nil)
}

// newEnvWith is newEnv with some worker binaries replaced by the given
// absolute paths.
func newEnvWith(t *testing.T, binaries map[string]string) *env {
	t.Helper()
	dir, err := os.MkdirTemp(buildDir, "t-"+strings.ReplaceAll(t.Name(), "/", "_")+"-")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dir: dir, home: filepath.Join(dir, "state")}
	fakeCodex, fakeClaude := testutil.BuildFakeWorker(t)
	bin := filepath.Join(dir, "bin")
	cfg := map[string]any{}
	workers := map[string]any{}
	for name, fake := range map[string]string{"codex": fakeCodex, "claude": fakeClaude} {
		p := filepath.Join(bin, name)
		must(t, os.MkdirAll(bin, 0o700))
		must(t, os.WriteFile(p, fmt.Appendf(nil, wrapper, fake), 0o700))
		if b, ok := binaries[name]; ok {
			p = b
		}
		workers[name] = map[string]string{"binary": p}
	}
	cfg["workers"] = workers
	cfgPath := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(cfg)
	must(t, os.WriteFile(cfgPath, b, 0o600))
	userHome := filepath.Join(dir, "home")
	must(t, os.Mkdir(userHome, 0o700))
	e.base = []string{
		"PATH=" + minimalPath, "HOME=" + userHome, "TMPDIR=" + os.TempDir(),
		"ASKOTHER_HOME=" + e.home, "ASKOTHER_CONFIG=" + cfgPath,
	}
	t.Cleanup(func() {
		if leaked := killLiveRuns(e.home); len(leaked) > 0 {
			t.Errorf("runs still live at test end (killed): %v", leaked)
		}
	})
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// workDir makes a fresh run cwd that selects the fake's scenario and arg.
func (e *env) workDir(scenario, arg string) string {
	e.t.Helper()
	e.work++
	d := filepath.Join(e.dir, fmt.Sprintf("work-%d", e.work))
	must(e.t, os.Mkdir(d, 0o700))
	e.scenario(d, scenario, arg)
	return d
}

// scenario makes later runs in cwd play scenario with arg.
func (e *env) scenario(cwd, scenario, arg string) {
	e.t.Helper()
	must(e.t, os.WriteFile(filepath.Join(cwd, ".askother-scenario"), []byte(scenario), 0o600))
	must(e.t, os.RemoveAll(filepath.Join(cwd, ".askother-arg")))
	if arg != "" {
		must(e.t, os.WriteFile(filepath.Join(cwd, ".askother-arg"), []byte(arg), 0o600))
	}
}

// fakeRecord reads what the fake worker of run id recorded in its cwd.
func (e *env) fakeRecord(id string) testutil.FakeRecord {
	e.t.Helper()
	r := e.record(id)
	if r.Runtime.WorkerPID == nil {
		e.t.Fatalf("run %s has no worker pid", id)
	}
	return testutil.ReadFakeRecord(e.t, filepath.Join(r.Request.CWD, fmt.Sprintf("rec-%d.json", *r.Runtime.WorkerPID)))
}

// askother runs the CLI and returns its exit code and output.
func (e *env) askother(args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, askotherBin, args...)
	cmd.Env = e.base
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		e.t.Fatalf("askother %v: %v", args, err)
	}
	return code, out.String(), errb.String()
}

// record reads a run record straight from the state dir.
func (e *env) record(id string) *run.Run {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.home, "runs", id+".json"))
	must(e.t, err)
	var r run.Run
	must(e.t, json.Unmarshal(b, &r))
	return &r
}

// until polls cond every 20 ms for up to d.
func until(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
	}
}

// exitLag is how long a supervisor may keep its lock after its terminal
// record is visible: the record is readable once renamed, while the
// supervisor still syncs the directory and exits (measured about 20 ms
// under -race and load).
const exitLag = 5 * time.Second

// killLiveRuns SIGKILLs the supervisor and worker group of every run in home
// whose lock is still held, waits for the locks to free, and returns
// their ids. A run whose record is terminal gets exitLag to release its lock
// first. Tests only: AskOther itself never signals from stored pids.
func killLiveRuns(home string) []string {
	st, err := run.Open(home)
	if err != nil {
		return nil
	}
	locks, _ := filepath.Glob(filepath.Join(home, "runs", "*.lock"))
	var leaked []string
	for _, l := range locks {
		id := strings.TrimSuffix(filepath.Base(l), ".lock")
		if live, _ := st.ProbeLive(id); !live {
			continue
		}
		if r, err := st.Read(id); err == nil && run.IsTerminal(r.State) && lockFreed(st, id, exitLag) {
			continue
		}
		leaked = append(leaked, id)
		if r, err := st.Read(id); err == nil {
			if p := r.Runtime.SupervisorPID; p != nil && *p > 1 {
				_ = syscall.Kill(*p, syscall.SIGKILL)
			}
			if g := r.Runtime.WorkerPGID; g != nil && *g > 1 {
				_ = syscall.Kill(-*g, syscall.SIGKILL)
			}
		}
		lockFreed(st, id, 3*time.Second)
	}
	return leaked
}

// lockFreed polls the run lock every 10 ms for up to d.
func lockFreed(st *run.Store, id string, d time.Duration) bool {
	for end := time.Now().Add(d); ; time.Sleep(10 * time.Millisecond) {
		if live, _ := st.ProbeLive(id); !live {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}
