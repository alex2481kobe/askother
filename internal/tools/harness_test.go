package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/lifecycle"
	"github.com/alex2481kobe/askother/internal/mcp"
	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/testutil"
)

// tEnv is a temp store, a t-supervisor Self and fake worker wrappers. The
// wrappers are needed because WorkerEnv drops ASKOTHER_FAKE_*.
type tEnv struct {
	t             *testing.T
	d             lifecycle.Deps
	dir, cwd      string
	codex, claude string            // the fake binaries
	binaries      map[string]string // the config file's worker binaries
}

func newTEnv(t *testing.T) *tEnv {
	codex, claude := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	home, bin, cwd := filepath.Join(dir, "state"), filepath.Join(dir, "bin"), filepath.Join(dir, "work")
	for _, p := range []string{bin, cwd} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	st, err := run.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(bin, tSupervisor)
	if err := os.Symlink(exe, self); err != nil {
		t.Fatal(err)
	}
	e := &tEnv{t: t, dir: dir, cwd: cwd, codex: codex, claude: claude, binaries: map[string]string{}, d: lifecycle.Deps{
		Store: st, ConfigPath: filepath.Join(dir, "config.json"), Adapters: adapters(), Self: self, StateHome: home,
		// PATH and HOME never reach a real worker CLI: only configured fakes resolve.
		Env: map[string]string{"PATH": "/usr/bin:/bin", "HOME": dir, "LANG": "C.UTF-8"},
	}}
	t.Cleanup(e.killAll)
	return e
}

// fake configures dialect's binary as a wrapper that plays scenario with
// arg, and returns the path of the fake's record.
func (e *tEnv) fake(dialect, scenario, arg string) (record string) {
	fakeBin := e.codex
	if dialect == "claude" {
		fakeBin = e.claude
	}
	dir, err := os.MkdirTemp(e.dir, "wrap-")
	if err != nil {
		e.t.Fatal(err)
	}
	record = filepath.Join(dir, "fake-record.json")
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := fmt.Sprintf("#!/bin/sh\nASKOTHER_FAKE_SCENARIO=%s ASKOTHER_FAKE_ARG=%s ASKOTHER_FAKE_RECORD=%s\n"+
		"export ASKOTHER_FAKE_SCENARIO ASKOTHER_FAKE_ARG ASKOTHER_FAKE_RECORD\nexec %s \"$@\"\n",
		q(scenario), q(arg), q(record), q(fakeBin))
	wrapper := filepath.Join(dir, dialect)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		e.t.Fatal(err)
	}
	e.configure(dialect, wrapper)
	return record
}

// configure makes the config file name binary for dialect.
func (e *tEnv) configure(dialect, binary string) {
	e.binaries[dialect] = binary
	body, err := json.Marshal(map[string]any{"workers": map[string]any{
		"codex": map[string]string{"binary": e.binaries["codex"]}, "claude": map[string]string{"binary": e.binaries["claude"]}}})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.d.ConfigPath, body, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// handler builds a Handler over a copy of e.d with extra env entries.
func (e *tEnv) handler(env map[string]string) *Handler {
	d := e.d
	d.Env = maps.Clone(e.d.Env)
	maps.Copy(d.Env, env)
	return New(d, "instance-synthetic")
}

// killAll lets finished supervisors exit, then kills whatever is left: live
// fake worker groups and supervisors of every run in the store.
func (e *tEnv) killAll() {
	runs, _, _ := e.d.Store.List()
	deadline := time.Now().Add(5 * time.Second)
	for _, r := range runs {
		if pg := r.Runtime.WorkerPGID; pg != nil && !run.IsTerminal(r.State) && slices.Contains(testutil.LiveFakes(), *pg) {
			_ = syscall.Kill(-*pg, syscall.SIGKILL)
		}
		pid := r.Runtime.SupervisorPID
		if pid == nil || *pid <= 0 {
			continue
		}
		for syscall.Kill(*pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(*pid, syscall.SIGKILL) == nil {
			e.t.Errorf("supervisor %d of run %s outlived its run; killed", *pid, r.ID)
		}
	}
}

type callOpt struct {
	client mcp.ClientInfo
	meta   json.RawMessage
}

// call invokes a tool with args marshalled to JSON (a string is sent raw).
func call(t *testing.T, h *Handler, o callOpt, name string, args any) (any, error) {
	t.Helper()
	raw, ok := args.(string)
	if !ok {
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	out, err := h.Call(context.Background(), name, json.RawMessage(raw), o.meta, o.client)
	if err != nil {
		var re *run.Error
		if !errors.As(err, &re) || error(re) != err {
			t.Fatalf("%s: error %v is not a bare *run.Error", name, err)
		}
	}
	return out, err
}

// must calls a tool that must succeed and returns its typed output.
func must[T any](t *testing.T, h *Handler, o callOpt, name string, args any) T {
	t.Helper()
	out, err := call(t, h, o, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	v, ok := out.(T)
	if !ok {
		t.Fatalf("%s: output %T", name, out)
	}
	return v
}

func wantCode(t *testing.T, err error, code run.Code) {
	t.Helper()
	var re *run.Error
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}
