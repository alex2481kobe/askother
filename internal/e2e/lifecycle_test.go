package e2e

import (
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/mcp"
	"github.com/alex2481kobe/askother/internal/run"
)

// startRun runs a codex worker with the given scenario and returns its id
// and cwd.
func startRun(c *mcpClient, e *env, key, scenario, arg string) (id, cwd string) {
	e.t.Helper()
	cwd = e.workDir(scenario, arg)
	var out mcp.RunOutput
	c.mustTool("run", runArgs{Key: key, Worker: "codex", Prompt: "p", CWD: cwd}, nil, &out)
	return out.ID, cwd
}

// running waits until the supervisor has committed running/live with its
// pids.
func running(e *env, id string) *run.Run {
	e.t.Helper()
	var r *run.Run
	until(e.t, 10*time.Second, "running "+id, func() bool {
		r = e.record(id)
		return r.State == run.StateRunning && r.Runtime.SupervisorPID != nil && r.Runtime.WorkerPGID != nil
	})
	return r
}

// send on a finished run resumes its native session.
func TestSendResumes(t *testing.T) {
	e := newEnv(t)
	c := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2e2")
	first, _ := startRun(c, e, "k1", "ok", "")
	if code, out, _ := e.askother("wait", first); code != 0 {
		t.Fatalf("first run: exit %d %q", code, out)
	}
	thread := e.record(first).NativeSessionID
	if thread == nil || *thread == "" {
		t.Fatal("first run has no native session")
	}

	var out mcp.RunOutput
	c.mustTool("send", map[string]any{"key": "k2", "id": first, "message": "and then?"}, nil, &out)
	if code, stdout, _ := e.askother("wait", out.ID); code != 0 {
		t.Fatalf("send run: exit %d %q", code, stdout)
	}
	r := e.record(out.ID)
	if r.PreviousID == nil || *r.PreviousID != first || r.NativeSessionID == nil || *r.NativeSessionID != *thread {
		t.Fatalf("previous_id %v session %v", r.PreviousID, r.NativeSessionID)
	}
	fake := e.fakeRecord(out.ID)
	if !fake.Invocation.Resume || fake.Invocation.ResumeID != *thread || !slices.Contains(fake.Argv, "resume") || fake.Stdin != "and then?" {
		t.Fatalf("resume argv: %q (invocation %+v, stdin %q)", fake.Argv, fake.Invocation, fake.Stdin)
	}
	c.close()
}

// stop on a hung worker ends it stopped; askother wait exits 2.
func TestStopHang(t *testing.T) {
	e := newEnv(t)
	c := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2e3")
	id, _ := startRun(c, e, "k1", "hang", "")
	running(e, id)
	var stop mcp.StopOutput
	c.mustTool("stop", map[string]any{"id": id}, nil, &stop)
	if !stop.StopRequested {
		t.Fatalf("stop: %+v", stop)
	}
	code, out, _ := e.askother("wait", id)
	if code != 2 || !strings.HasPrefix(out, "askother: run "+id+" stopped ") {
		t.Fatalf("askother wait: exit %d %q", code, out)
	}
	c.close()
}

// Supervisors are detached, so killing askother mcp loses nothing; a fresh
// server sees the run done.
func TestMCPKilledMidRun(t *testing.T) {
	e := newEnv(t)
	c := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2e4")
	id, _ := startRun(c, e, "k1", "silent", "1500ms")
	running(e, id)
	c.kill()
	if code, out, _ := e.askother("wait", id); code != 0 {
		t.Fatalf("askother wait after the server died: exit %d %q", code, out)
	}
	c2 := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2e4")
	var st mcp.StatusOutput
	c2.mustTool("status", map[string]any{"id": id}, nil, &st)
	if len(st.Runs) != 1 || st.Runs[0].State != run.StateDone || !st.Runs[0].Unread {
		t.Fatalf("fresh server status: %+v", st.Runs)
	}
	var res mcp.ResultOutput
	c2.mustTool("result", map[string]any{"id": id}, nil, &res)
	if res.Text != "finished after a silence" {
		t.Fatalf("result: %q", res.Text)
	}
	c2.close()
}

// Supervisor loss reads as interrupted, never success; askother wait exits 3.
// The interrupted view is derived on read and never written.
func TestSupervisorKilled(t *testing.T) {
	e := newEnv(t)
	c := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2e5")
	id, _ := startRun(c, e, "k1", "hang", "")
	r := running(e, id)
	// The hung worker outlives its supervisor: AskOther never signals from
	// stored pids, so the test kills it after.
	defer syscall.Kill(-*r.Runtime.WorkerPGID, syscall.SIGKILL)
	must(t, syscall.Kill(*r.Runtime.SupervisorPID, syscall.SIGKILL))

	code, out, _ := e.askother("wait", id)
	if code != 3 || !strings.HasPrefix(out, "askother: run "+id+" interrupted (execution ") {
		t.Fatalf("askother wait: exit %d %q", code, out)
	}
	var st mcp.StatusOutput
	c.mustTool("status", map[string]any{"id": id}, nil, &st)
	if s := st.Runs[0]; s.State != run.StateInterrupted || s.Execution != run.ExecUnknown || s.EndedAt != nil || !s.Unread {
		t.Fatalf("status: %+v", s)
	}
	if got := e.record(id); got.State != run.StateRunning {
		t.Fatalf("stored state %s: the interrupted view must not be saved", got.State)
	}
	c.close()
}

// A configured binary that is missing refuses the run: there is no
// fallback to PATH or the fixed dirs, so a test can never reach a real CLI.
func TestMissingBinaryRefused(t *testing.T) {
	missing := map[string]string{"codex": "/nonexistent/e2e/codex", "claude": "/nonexistent/e2e/claude"}
	e := newEnvWith(t, missing)
	c := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2e7")
	for _, w := range []string{"codex", "claude"} {
		var refusal run.Error
		if !c.tool("run", runArgs{Key: "k-" + w, Worker: w, Prompt: "p", CWD: e.workDir("ok", "")}, nil, &refusal) ||
			refusal.Code != run.CodeInvalidInput || !strings.Contains(refusal.Message, "not available") {
			t.Fatalf("%s with a missing binary: %+v", w, refusal)
		}
	}
	if locks, _ := filepath.Glob(filepath.Join(e.home, "runs", "*")); len(locks) != 0 {
		t.Fatalf("a refused run left files: %v", locks)
	}
	c.close()
}
