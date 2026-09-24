package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/mcp"
	"github.com/alex2481kobe/askother/internal/run"
)

// A worker that fails with only stderr to explain it (a bad flag, a login
// problem) reaches the caller: the result's error carries the stderr text.
func TestStderrOnlyFailureThroughMCP(t *testing.T) {
	e := newEnv(t)
	c := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2e8")
	id, _ := startRun(c, e, "k1", "stderr_only", "error: synthetic unexpected argument")
	code, out, _ := e.askother("wait", id)
	if code != 1 || !strings.Contains(out, " failed (exit 1) ") || !strings.Contains(out, "synthetic unexpected argument") {
		t.Fatalf("askother wait: exit %d %q", code, out)
	}
	var res mcp.ResultOutput
	c.mustTool("result", map[string]any{"id": id}, nil, &res)
	if res.State != run.StateFailed || res.Available || res.Error == nil || res.Error.Code != string(run.CodeWorkerFailed) ||
		!strings.Contains(res.Error.Message, "stderr: error: synthetic unexpected argument") {
		t.Fatalf("result: %+v error %+v", res, res.Error)
	}
	c.close()
}

// A continuation whose worker fails at start does not strand the session:
// a send to that failed run resumes the original native session.
func TestFailedContinuationThenResend(t *testing.T) {
	e := newEnv(t)
	c := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2e9")
	first, cwd := startRun(c, e, "k1", "ok", "")
	if code, out, _ := e.askother("wait", first); code != 0 {
		t.Fatalf("first run: exit %d %q", code, out)
	}
	thread := *e.record(first).NativeSessionID

	e.scenario(cwd, "stderr_only", "error: synthetic auth failure")
	var failed mcp.RunOutput
	c.mustTool("send", map[string]any{"key": "k2", "id": first, "message": "next"}, nil, &failed)
	if code, out, _ := e.askother("wait", failed.ID); code != 1 {
		t.Fatalf("failed continuation: exit %d %q", code, out)
	}
	if r := e.record(failed.ID); r.NativeSessionID == nil || *r.NativeSessionID != thread {
		t.Fatalf("failed continuation lost the session: %v", r.NativeSessionID)
	}

	e.scenario(cwd, "ok", "resumed after a failure")
	var again mcp.RunOutput
	c.mustTool("send", map[string]any{"key": "k3", "id": failed.ID, "message": "next, again"}, nil, &again)
	if code, out, _ := e.askother("wait", again.ID); code != 0 {
		t.Fatalf("resend: exit %d %q", code, out)
	}
	fake := e.fakeRecord(again.ID)
	if !fake.Invocation.Resume || fake.Invocation.ResumeID != thread {
		t.Fatalf("resend did not resume %s: %+v", thread, fake.Invocation)
	}
	var res mcp.ResultOutput
	c.mustTool("result", map[string]any{"id": again.ID}, nil, &res)
	if res.Text != "resumed after a failure" {
		t.Fatalf("result: %+v", res)
	}
	c.close()
}

// A reply lost with its connection is recovered by retrying with the same
// key: the retry reports the run the first call started, and no second run
// exists.
func TestLostReplyRetryByKey(t *testing.T) {
	e := newEnv(t)
	cwd := e.workDir("silent", "1s")
	args := runArgs{Key: "k-lost", Worker: "codex", Prompt: "p", CWD: cwd}
	c := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2ea")
	c.send(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "tools/call",
		"params": map[string]any{"name": "run", "arguments": args}})
	records := func() []string {
		m, _ := filepath.Glob(filepath.Join(e.home, "runs", "*.json"))
		return m
	}
	// The launcher replies once the supervisor has committed running; kill
	// the server then, so the reply is lost rather than the launch.
	until(t, 10*time.Second, "the supervisor to take the run", func() bool {
		m := records()
		return len(m) == 1 && e.record(strings.TrimSuffix(filepath.Base(m[0]), ".json")).State != run.StateStarting
	})
	c.kill() // the reply is never read

	c2 := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID=00000000-0000-4000-8000-00000000e2ea")
	var out mcp.RunOutput
	c2.mustTool("run", args, nil, &out)
	if !out.Reused || filepath.Base(records()[0]) != out.ID+".json" {
		t.Fatalf("retry: %+v, records %v", out, records())
	}
	if code, stdout, _ := e.askother("wait", out.ID); code != 0 {
		t.Fatalf("askother wait: exit %d %q", code, stdout)
	}

	var sent mcp.RunOutput
	send := map[string]any{"key": "k-lost-send", "id": out.ID, "message": "more"}
	c2.mustTool("send", send, nil, &sent)
	var again mcp.RunOutput
	c2.mustTool("send", send, nil, &again)
	if sent.Reused || !again.Reused || again.ID != sent.ID || len(records()) != 2 {
		t.Fatalf("send retry: %+v then %+v, %d records", sent, again, len(records()))
	}
	e.askother("wait", sent.ID)
	c2.close()
}
