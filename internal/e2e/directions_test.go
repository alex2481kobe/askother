package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/mcp"
	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/worker"
)

// A synthetic answer with multibyte text and inner newlines, but no trailing
// newline (the wrapper's $(cat) would strip one).
const answer = "e2e answer: \u732b\U0001F41F\nsecond line\n\ttabbed end"

type runArgs struct {
	Key    string `json:"key"`
	Worker string `json:"worker"`
	Prompt string `json:"prompt"`
	CWD    string `json:"cwd"`
	Role   string `json:"role,omitempty"`
	Task   string `json:"task,omitempty"`
}

// Claude Code calls AskOther; the caller is CLAUDE_CODE_SESSION_ID, the watch
// command wakes it as given, and the result tool delivers the exact answer.
func TestClaudeClientDirection(t *testing.T) {
	e := newEnv(t)
	const session = "00000000-0000-4000-8000-00000000e2e1"
	c := e.startMCP("claude-code", "CLAUDE_CODE_SESSION_ID="+session)
	cwd := e.workDir("ok", answer)

	var out mcp.RunOutput
	c.mustTool("run", runArgs{Key: "k1", Worker: "codex", Prompt: "say it", CWD: cwd, Role: "implementer", Task: "e2e task"}, nil, &out)
	started := time.Now()
	if out.Reused || out.Watch != "askother wait "+out.ID {
		t.Fatalf("run reply: %+v", out)
	}
	// Run the watch value exactly as given: it must be a shell command.
	words := strings.Fields(out.Watch)
	code, stdout, stderr := e.askother(words[1:]...)
	t.Logf("perf: run reply to `askother wait` exit for an ok run: %s", time.Since(started))
	want := regexp.MustCompile(`^askother: run ` + out.ID + ` done \(exit 0\) \d+(\.\d+)?m?s implementer "e2e task" -> result ` + out.ID + "\n$")
	if code != 0 || !want.MatchString(stdout) {
		t.Fatalf("askother wait: exit %d stdout %q stderr %q", code, stdout, stderr)
	}

	var st mcp.StatusOutput
	c.mustTool("status", map[string]any{"id": out.ID}, nil, &st)
	if len(st.Runs) != 1 || !st.Runs[0].Unread {
		t.Fatalf("status before reading: %+v", st)
	}
	var res mcp.ResultOutput
	c.mustTool("result", map[string]any{"id": out.ID}, nil, &res)
	if res.State != run.StateDone || !res.EOF || res.Text != answer || res.TotalBytes != int64(len(answer)) {
		t.Fatalf("result: %+v", res)
	}
	c.mustTool("status", map[string]any{}, nil, &st)
	if len(st.Runs) != 1 || st.Runs[0].Unread {
		t.Fatalf("status after reading: %+v", st)
	}

	r := e.record(out.ID)
	if r.CallerID != session || r.CallerSource != run.SourceClaude {
		t.Fatalf("caller: %q %q", r.CallerID, r.CallerSource)
	}
	fake := e.fakeRecord(out.ID)
	hasRunID := false
	for _, n := range fake.EnvNames {
		if n == "CLAUDE_CODE_SESSION_ID" {
			t.Fatalf("worker env has %s: %v", n, fake.EnvNames)
		}
		if n == "ASKOTHER_RUN_ID" {
			hasRunID = true
		}
	}
	if !hasRunID {
		t.Fatalf("worker env lacks ASKOTHER_RUN_ID: %v", fake.EnvNames)
	}
	c.close()
}

// Codex calls AskOther; the caller is the thread from each call's _meta, and
// the wait tool is how it learns the run ended.
func TestCodexClientDirection(t *testing.T) {
	e := newEnv(t)
	c := e.startMCP("codex-mcp-client")
	threadA := map[string]any{"threadId": "thread-e2e-a"}
	threadB := map[string]any{"x-codex-turn-metadata": map[string]any{"thread_id": "thread-e2e-b"}}
	cwd := e.workDir("ok", answer)

	var out mcp.RunOutput
	c.mustTool("run", runArgs{Key: "k1", Worker: "claude", Prompt: "say it", CWD: cwd}, threadA, &out)
	var w mcp.WaitOutput
	for i := 0; !w.Satisfied; i++ {
		if i == 5 {
			t.Fatalf("wait never satisfied: %+v", w)
		}
		c.mustTool("wait", map[string]any{"ids": []string{out.ID}}, threadA, &w)
	}
	if w.Runs[0].State != run.StateDone {
		t.Fatalf("wait: %+v", w.Runs[0])
	}
	var res mcp.ResultOutput
	c.mustTool("result", map[string]any{"id": out.ID}, threadA, &res)
	if res.Text != answer || res.State != run.StateDone {
		t.Fatalf("result: %+v", res)
	}

	var second mcp.RunOutput
	c.mustTool("run", runArgs{Key: "k2", Worker: "claude", Prompt: "again", CWD: cwd}, threadB, &second)
	var st mcp.StatusOutput
	c.mustTool("status", map[string]any{}, threadB, &st)
	if len(st.Runs) != 2 || st.Runs[0].ID != second.ID || st.Runs[1].ID != out.ID {
		t.Fatalf("status lists every run, newest first: %+v", st.Runs)
	}
	for id, thread := range map[string]string{out.ID: "thread-e2e-a", second.ID: "thread-e2e-b"} {
		if r := e.record(id); r.CallerID != thread || r.CallerSource != run.SourceCodex {
			t.Fatalf("caller of %s: %q %q", id, r.CallerID, r.CallerSource)
		}
	}
	e.askother("wait", second.ID)
	c.close()
}

// Only the read tools carry readOnlyHint, and the run description carries
// every default.
func TestToolsList(t *testing.T) {
	e := newEnv(t)
	c := e.startMCP("codex-mcp-client")
	var list struct{ Tools []mcp.Tool }
	must(t, json.Unmarshal(c.request("tools/list", map[string]any{}), &list))
	readOnly := map[string]bool{"wait": true, "result": true, "status": true}
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
		if got := tl.Annotations != nil && tl.Annotations.ReadOnlyHint; got != readOnly[tl.Name] {
			t.Errorf("%s: readOnlyHint %v", tl.Name, got)
		}
		if tl.Name != "run" {
			continue
		}
		for _, f := range []worker.Facts{worker.Codex{}.Facts(), worker.Claude{}.Facts()} {
			for _, want := range append([]string{f.DefaultMode}, f.Pinned...) {
				if !strings.Contains(tl.Description, want) {
					t.Errorf("run description lacks %s fact %q", f.Name, want)
				}
			}
		}
	}
	slices.Sort(names)
	if strings.Join(names, " ") != "result run send status stop wait" {
		t.Errorf("tools: %v", names)
	}
	c.close()
}

func TestHelpAndUnknownCommand(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.askother("help")
	for _, want := range []string{"Usage:", "\nDefaults ", "default mode  read-only", "default mode  dontAsk",
		"\nSetup ", "claude mcp add -s user askother -- " + askotherBin + " mcp\n", "[mcp_servers.askother]\n", "command = " + strconv.Quote(askotherBin)} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	if code != 0 {
		t.Errorf("help exit %d", code)
	}
	t.Logf("askother help:\n%s", out)
	if code, out, errs := e.askother("bogus"); code != 4 || out != "" || !strings.Contains(errs, "Usage:") {
		t.Errorf("bogus: exit %d stdout %q stderr %q", code, out, errs)
	}
}

// Performance note: RSS of an idle `askother mcp` after initialize.
func TestIdleRSS(t *testing.T) {
	e := newEnv(t)
	c := e.startMCP("claude-code")
	c.request("tools/list", map[string]any{})
	time.Sleep(500 * time.Millisecond)
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(c.cmd.Process.Pid)).Output()
	if err != nil {
		t.Skipf("ps: %v", err)
	}
	t.Logf("perf: idle askother mcp RSS %s KiB", strings.TrimSpace(string(out)))
	c.close()
	if fi, err := os.Stat(askotherBin); err == nil {
		t.Logf("perf: askother binary %d bytes", fi.Size())
	}
}
