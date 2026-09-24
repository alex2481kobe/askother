package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/lifecycle"
	"github.com/alex2481kobe/orca/internal/mcp"
	"github.com/alex2481kobe/orca/internal/run"
)

func TestFactsOrder(t *testing.T) {
	var names []string
	for _, f := range Facts(adapters()) {
		names = append(names, f.Name)
	}
	if want := "[claude codex]"; fmt.Sprint(names) != want {
		t.Fatalf("facts order %v, want %s", names, want)
	}
}

func TestInputValidation(t *testing.T) {
	e := newTEnv(t)
	e.fake("codex", "ok", "") // the run below is otherwise valid
	h := e.handler(nil)
	for _, c := range []struct{ tool, args string }{
		{"run", `{"key":"k","worker":"codex","prompt":"p","cwd":"` + e.cwd + `","bogus":1}`},
		{"workers", `{}`},
		{"wait", `{"ids":[]}`},
		{"wait", `{}`},
		{"status", `{"scope":"all"}`},
		{"result", `{"id":"x","limit":0}`},
		{"stop", `{"ID":"x"}`},
	} {
		_, err := call(t, h, callOpt{}, c.tool, c.args)
		wantCode(t, err, run.CodeInvalidInput)
	}
}

func TestToolError(t *testing.T) {
	h := New(lifecycle.Deps{StateHome: "/var/orca-state"}, "i")
	for _, c := range []struct {
		err  error
		code run.Code
		msg  string
	}{
		{run.Errorf(run.CodeNotFound, "no run"), run.CodeNotFound, "no run"},
		{fmt.Errorf("put: %w", run.Errorf(run.CodeStoreIO, "disk")), run.CodeStoreIO, "put: STORE_IO: disk"},
		{fmt.Errorf("sync: %w", run.CodeStoreIO), run.CodeStoreIO, "sync: STORE_IO"},
		{&run.StoreError{Path: "/x/runs/a.txt", Op: "rename", Err: syscall.EIO}, run.CodeStoreIO, "STORE_IO: rename a.txt (not visible): input/output error"},
		{fmt.Errorf("publish: %w", &run.StoreError{Path: "/x/a.json", Op: "dirsync", Visible: true, Err: syscall.EIO}), run.CodeStoreIO,
			"publish: STORE_IO: dirsync a.json (visible, not durable): input/output error"},
		{errors.New("start supervisor: exec failed"), run.CodeProtocol, "start supervisor: exec failed"},
		{errors.New("open /var/orca-state/runs/x.json: denied"), run.CodeProtocol, "open $ORCA_HOME/runs/x.json: denied"},
	} {
		got := h.toolError(c.err)
		if got.Code != c.code || got.Message != c.msg {
			t.Errorf("%v -> %s %q, want %s %q", c.err, got.Code, got.Message, c.code, c.msg)
		}
	}
}

// A supervisor that cannot start is a plain error from lifecycle.Start; the
// model gets a PROTOCOL tool error.
func TestStartFailureIsToolError(t *testing.T) {
	e := newTEnv(t)
	e.fake("codex", "ok", "")
	e.d.Self = filepath.Join(e.dir, "no-such-orca")
	_, err := call(t, e.handler(nil), callOpt{}, "run", map[string]any{"key": "k", "worker": "codex", "prompt": "p", "cwd": e.cwd})
	wantCode(t, err, run.CodeProtocol)
}

func TestSweepThrottle(t *testing.T) {
	e := newTEnv(t)
	h := e.handler(nil)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	h.now = func() time.Time { return now }
	swept, release := make(chan struct{}, 10), make(chan struct{})
	h.sweep = func(lifecycle.Deps) error { swept <- struct{}{}; <-release; return errors.New("ignored") }
	defer close(release)

	expect := func(tool string, advance time.Duration, want bool) {
		t.Helper()
		now = now.Add(advance)
		done := make(chan struct{})
		go func() { h.Call(context.Background(), tool, json.RawMessage(`{}`), nil, mcp.ClientInfo{}); close(done) }()
		select { // a blocked sweep never blocks the reply
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s blocked on the sweep", tool)
		}
		select {
		case <-swept:
			if !want {
				t.Fatalf("%s after %v swept", tool, advance)
			}
		case <-time.After(200 * time.Millisecond):
			if want {
				t.Fatalf("%s after %v did not sweep", tool, advance)
			}
		}
	}
	expect("run", 0, true)
	expect("send", 9*time.Minute, false)
	expect("status", 2*time.Minute, false) // due, but only run and send sweep
	expect("send", 0, true)
	expect("run", 10*time.Minute-time.Nanosecond, false)
	expect("run", time.Nanosecond, true)
}
