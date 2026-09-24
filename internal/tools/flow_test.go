package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alex2481kobe/orca/internal/mcp"
	"github.com/alex2481kobe/orca/internal/run"
	"github.com/alex2481kobe/orca/internal/testutil"
)

// run, wait, result through the real launcher and supervisor: the answer is
// byte-exact, and status flags the run unread until its result is served.
func TestRunWaitResult(t *testing.T) {
	for _, tc := range []struct {
		worker, scenario, arg string
		minPages              int
	}{
		{"codex", "progress_then_final", "", 2},
		{"claude", "ok", "synthetic answer: caf\u00e9 \u2713 \U0001F40B", 1},
	} {
		t.Run(tc.worker, func(t *testing.T) {
			e := newTEnv(t)
			record := e.fake(tc.worker, tc.scenario, tc.arg)
			h, o := e.handler(nil), callOpt{}
			args := map[string]any{"key": "k1", "worker": tc.worker, "prompt": "say it", "cwd": e.cwd}
			ro := must[mcp.RunOutput](t, h, o, "run", args)
			if ro.Reused || ro.Watch != "orca wait "+ro.ID {
				t.Fatalf("run output %+v", ro)
			}
			wo := must[mcp.WaitOutput](t, h, o, "wait", map[string]any{"ids": []string{ro.ID}})
			if !wo.Satisfied || wo.TimedOut || len(wo.Runs) != 1 || wo.Runs[0].State != run.StateDone {
				t.Fatalf("wait output %+v", wo)
			}
			if !wo.Runs[0].Unread {
				t.Fatalf("finished run not unread before result: %+v", wo.Runs[0])
			}
			again := must[mcp.RunOutput](t, h, o, "run", args)
			if !again.Reused || again.ID != ro.ID || again.State != run.StateDone {
				t.Fatalf("retry %+v", again)
			}

			var text strings.Builder
			var offset int64
			pages := 0
			for {
				p := must[mcp.ResultOutput](t, h, o, "result", map[string]any{"id": ro.ID, "offset": offset})
				pages++
				text.WriteString(p.Text)
				if !p.Available || p.Offset != offset {
					t.Fatalf("page %+v", p)
				}
				if p.EOF {
					break
				}
				offset = p.NextOffset
			}
			sum := sha256.Sum256([]byte(text.String()))
			want := testutil.ReadFakeRecord(t, record).Answer
			if want == nil || pages < tc.minPages || hex.EncodeToString(sum[:]) != want.SHA256 || text.Len() != want.Bytes {
				t.Fatalf("answer %d bytes over %d pages, sha %x; fake %+v", text.Len(), pages, sum, want)
			}
			st := must[mcp.StatusOutput](t, h, o, "status", map[string]any{})
			if len(st.Runs) != 1 || st.Runs[0].Unread {
				t.Fatalf("status after result %+v", st)
			}
		})
	}
}

// The caller that started a run is recorded for grouping only: status
// lists every run whoever asks.
func TestIdentity(t *testing.T) {
	e := newTEnv(t)
	e.fake("codex", "ok", "")
	start := func(h *Handler, o callOpt, key string) string {
		t.Helper()
		ro := must[mcp.RunOutput](t, h, o, "run", map[string]any{"key": key, "worker": "codex", "prompt": "p", "cwd": e.cwd})
		must[mcp.WaitOutput](t, h, o, "wait", map[string]any{"ids": []string{ro.ID}})
		return ro.ID
	}
	caller := func(id string) (string, run.CallerSource) {
		r, err := e.d.Store.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		return r.CallerID, r.CallerSource
	}
	claude := callOpt{client: mcp.ClientInfo{Name: "claude-code"}}
	codex := mcp.ClientInfo{Name: "codex-mcp-client"}
	a := callOpt{client: codex, meta: json.RawMessage(`{"threadId":"thread-a"}`)}
	b := callOpt{client: codex, meta: json.RawMessage(`{"x-codex-turn-metadata":{"thread_id":"thread-b"}}`)}

	h := e.handler(map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-session-synthetic", "ORCA_RUN_ID": "ignored"})
	ids := []string{start(h, claude, "claude-1"), start(h, a, "thread-a-1"), start(h, b, "thread-b-1"), start(h, callOpt{}, "plain-1")}
	want := []struct {
		id  string
		src run.CallerSource
	}{
		{"claude-session-synthetic", run.SourceClaude},
		{"thread-a", run.SourceCodex}, // one handler: identity is resolved per call
		{"thread-b", run.SourceCodex},
		{"instance-synthetic", run.SourceFallback},
	}
	for i, id := range ids {
		if cid, src := caller(id); cid != want[i].id || src != want[i].src {
			t.Errorf("run %d: caller %q %q, want %q %q", i, cid, src, want[i].id, want[i].src)
		}
	}
	st := must[mcp.StatusOutput](t, h, a, "status", map[string]any{})
	if len(st.Runs) != len(ids) {
		t.Fatalf("status for one caller saw %d runs, want all %d", len(st.Runs), len(ids))
	}
}

// wait with any returns once one run ends; stop on a hanging worker, then
// wait, gives stopped. Real 1 s stop poll
// and 5 s grace: the lifecycle seams are unexported.
func TestStopThenWait(t *testing.T) {
	e := newTEnv(t)
	e.fake("codex", "hang", "")
	e.fake("claude", "ok", "")
	h, o := e.handler(nil), callOpt{}
	ro := must[mcp.RunOutput](t, h, o, "run", map[string]any{"key": "hang-1", "worker": "codex", "prompt": "p", "cwd": e.cwd})
	quick := must[mcp.RunOutput](t, h, o, "run", map[string]any{"key": "ok-1", "worker": "claude", "prompt": "p", "cwd": e.cwd})
	anyOut := must[mcp.WaitOutput](t, h, o, "wait", map[string]any{"ids": []string{ro.ID, quick.ID}, "any": true})
	if !anyOut.Satisfied || anyOut.Runs[0].State != run.StateRunning || anyOut.Runs[1].State != run.StateDone {
		t.Fatalf("wait any %+v", anyOut.Runs)
	}
	so := must[mcp.StopOutput](t, h, o, "stop", map[string]any{"id": ro.ID})
	if !so.StopRequested || so.ID != ro.ID || run.IsTerminal(so.State) {
		t.Fatalf("stop output %+v", so)
	}
	wo := must[mcp.WaitOutput](t, h, o, "wait", map[string]any{"ids": []string{ro.ID}})
	if !wo.Satisfied || wo.Runs[0].State != run.StateStopped {
		t.Fatalf("wait output %+v", wo.Runs)
	}
	again := must[mcp.StopOutput](t, h, o, "stop", map[string]any{"id": ro.ID})
	if again.StopRequested || again.State != run.StateStopped {
		t.Fatalf("stop after end %+v", again)
	}
}
