package testutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if err := RemoveFakeWorker(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		code = 1
	}
	os.Exit(code)
}

func TestBuildFakeWorkerOnce(t *testing.T) {
	codex, claude := BuildFakeWorker(t)
	codex2, claude2 := BuildFakeWorker(t)
	if codex != codex2 || claude != claude2 {
		t.Fatal("second call rebuilt the fake worker")
	}
	if filepath.Base(codex) != "codex" || filepath.Base(claude) != "claude" {
		t.Fatalf("names %s %s", codex, claude)
	}
	for _, p := range []string{codex, claude} {
		if fi, err := os.Stat(p); err != nil || fi.Mode()&0o111 == 0 {
			t.Fatalf("%s not executable: %v", p, err)
		}
	}
}

func TestEnvHelpers(t *testing.T) {
	env := []string{"A=1", "ASKOTHER_FAKE_SCENARIO=old", "ASKOTHER_FAKE_ARG=old", "B=2"}
	got := Scenario(env, "hang", "")
	if want := []string{"A=1", "B=2", "ASKOTHER_FAKE_SCENARIO=hang"}; !slices.Equal(got, want) {
		t.Fatalf("Scenario no arg = %v", got)
	}
	got = WithRecord(Scenario(env, "silent", "0.2"), "/tmp/rec.json")
	want := []string{"A=1", "B=2", "ASKOTHER_FAKE_SCENARIO=silent", "ASKOTHER_FAKE_ARG=0.2", "ASKOTHER_FAKE_RECORD=/tmp/rec.json"}
	if !slices.Equal(got, want) {
		t.Fatalf("Scenario with arg = %v", got)
	}
	if env[1] != "ASKOTHER_FAKE_SCENARIO=old" {
		t.Fatal("Scenario modified its input")
	}
	if got := unsetEnv([]string{"AB=1", "A=2"}, "A"); !slices.Equal(got, []string{"AB=1"}) {
		t.Fatalf("unsetEnv matched a prefix: %v", got)
	}
}

// The record decodes into FakeRecord with no unknown fields, so the two
// definitions cannot drift apart silently.
func TestFakeRecordMatchesProgram(t *testing.T) {
	_, claude := BuildFakeWorker(t)
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec.json")
	env := WithRecord(Scenario([]string{"PATH=/usr/bin:/bin", "ASKOTHER_HOME=secret-value"}, "grandchild_holds_pipe", "5s"), rec)
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--permission-mode", "dontAsk",
		"--permission-prompts", "none", "--model", "m1"}
	cmd := GroupCommand(t, claude, env, args...)
	cmd.Dir, cmd.Stdin = dir, strings.NewReader("hello")
	if err := cmd.Run(); err != nil { // stdout is /dev/null, so the grandchild blocks nothing
		t.Fatal(err)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var r FakeRecord
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("record has fields FakeRecord lacks: %v", err)
	}
	KillOnCleanup(t, r.GrandchildPID)
	if r.Dialect != "claude" || r.Scenario != "grandchild_holds_pipe" || r.Arg != "5s" || r.Stdin != "hello" ||
		r.Invocation.Model != "m1" || r.Invocation.PermissionMode != "dontAsk" || r.SessionID == "" ||
		r.Answer == nil || r.Answer.Bytes == 0 || r.GrandchildPID == 0 || !slices.Contains(r.EnvNames, "ASKOTHER_HOME") ||
		!slices.Equal(r.EnvNames, slices.Sorted(slices.Values(r.EnvNames))) {
		t.Fatalf("record %+v", r)
	}
	if bytes.Contains(b, []byte("secret-value")) {
		t.Fatal("the record holds an environment value")
	}
}

// GroupCommand's cleanup kills a hung worker and a grandchild left in its
// group after the worker exited.
func TestGroupCommandCleanupKillsGroup(t *testing.T) {
	codex, _ := BuildFakeWorker(t)
	dir := t.TempDir()
	var hung, grandchild int
	t.Run("inner", func(t *testing.T) {
		for _, scenario := range []string{"hang", "grandchild_holds_pipe"} {
			rec := filepath.Join(dir, scenario+".json")
			env := WithRecord(Scenario([]string{"PATH=/usr/bin:/bin"}, scenario, "20s"), rec)
			cmd := GroupCommand(t, codex, env, "exec", "--json", "-")
			cmd.Stdin = strings.NewReader("synthetic prompt")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			go func() { _ = cmd.Wait() }()
			if scenario == "hang" {
				hung = cmd.Process.Pid
				waitFor(t, func() bool { _, err := os.Stat(rec); return err == nil })
				if !slices.Contains(LiveFakes(), hung) {
					t.Fatalf("running fake %d not in LiveFakes %v", hung, LiveFakes())
				}
				continue
			}
			waitFor(t, func() bool { b, _ := os.ReadFile(rec); return bytes.Contains(b, []byte("grandchild_pid")) })
			grandchild = ReadFakeRecord(t, rec).GrandchildPID
		}
		if syscall.Kill(hung, 0) != nil || syscall.Kill(grandchild, 0) != nil {
			t.Fatal("processes should be alive before cleanup")
		}
	})
	KillOnCleanup(t, hung) // only matters if the inner cleanup failed
	KillOnCleanup(t, grandchild)
	// Both are reaped once killed (the grandchild by init), so the pids vanish.
	waitFor(t, func() bool { return errors.Is(syscall.Kill(hung, 0), syscall.ESRCH) })
	waitFor(t, func() bool { return errors.Is(syscall.Kill(grandchild, 0), syscall.ESRCH) })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not met within 3s")
}

// LiveFakes sees a running fake and its grandchild, and drops them once they
// are dead. RemoveFakeWorker relies on this to detect leaks.
func TestLiveFakes(t *testing.T) {
	codex, _ := BuildFakeWorker(t)
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec.json")
	env := WithRecord(Scenario([]string{"PATH=/usr/bin:/bin"}, "grandchild_setsid", "20s"), rec)
	cmd := GroupCommand(t, codex, env, "exec", "--json", "-")
	cmd.Stdin = strings.NewReader("synthetic prompt")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	gc := ReadFakeRecord(t, rec).GrandchildPID
	KillOnCleanup(t, gc)
	waitFor(t, func() bool { return slices.Contains(LiveFakes(), gc) }) // it registers as it starts
	waitFor(t, func() bool { return !slices.Contains(LiveFakes(), cmd.Process.Pid) })
	_ = syscall.Kill(gc, syscall.SIGKILL)
	waitFor(t, func() bool { return !slices.Contains(LiveFakes(), gc) })
}
