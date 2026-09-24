package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if err := testutil.RemoveFakeWorker(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		code = 1
	}
	os.Exit(code)
}

const (
	testPrompt  = "Summarize the synthetic notes in one line."
	testSession = "0b9c6e1a-4f2d-4c8e-9a1b-2d3e4f5a6b7c"
	testThread  = "01900000-0000-7000-8000-00000000abcd"
)

// fakeRun is one finished fake invocation.
type fakeRun struct {
	dir    string
	stdout []byte
	stderr []byte
	code   int
	signal syscall.Signal
	rec    testutil.FakeRecord
}

func baseEnv(dir string) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
}

// runFake runs bin to completion in a fresh temp dir. It drains stdout and
// stderr concurrently, so it must not be used for scenarios that leave a
// grandchild holding the pipes.
func runFake(t *testing.T, bin, scenario, arg, stdin string, args ...string) fakeRun {
	t.Helper()
	dir := t.TempDir()
	return runFakeIn(t, dir, bin, scenario, arg, stdin, args...)
}

func runFakeIn(t *testing.T, dir, bin, scenario, arg, stdin string, args ...string) fakeRun {
	t.Helper()
	recPath := filepath.Join(dir, "record.json")
	env := testutil.WithRecord(testutil.Scenario(baseEnv(dir), scenario, arg), recPath)
	cmd := testutil.GroupCommand(t, bin, env, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run %s: %v", scenario, err)
	}
	r := fakeRun{dir: dir, stdout: out.Bytes(), stderr: errb.Bytes(), code: cmd.ProcessState.ExitCode()}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		r.signal = ws.Signal()
	}
	if _, err := os.Stat(recPath); err == nil {
		r.rec = testutil.ReadFakeRecord(t, recPath)
		testutil.KillOnCleanup(t, r.rec.GrandchildPID)
	}
	return r
}

func codexArgv(out string) []string {
	return []string{"exec", "--json", "-o", out, "--sandbox", "read-only", "-c", `approval_policy="never"`,
		"--skip-git-repo-check", "-"}
}

func codexResumeArgv(out, thread string) []string {
	return []string{"exec", "resume", "--json", "-o", out, "-c", `sandbox_mode="read-only"`,
		"-c", `approval_policy="never"`, "--skip-git-repo-check", thread, "-"}
}

func claudeArgv(extra ...string) []string {
	return append([]string{"--print", "--output-format", "stream-json", "--verbose", "--permission-mode", "dontAsk",
		"--permission-prompts", "none"}, extra...)
}

// lines splits stdout into events the way Orca must: ReadBytes('\n'), no
// size cap, and a final line without '\n' still counts.
func lines(t *testing.T, stdout []byte) [][]byte {
	t.Helper()
	r := bufio.NewReader(bytes.NewReader(stdout))
	var out [][]byte
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			out = append(out, bytes.TrimSuffix(line, []byte("\n")))
		}
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

type event map[string]any

func events(t *testing.T, stdout []byte) []event {
	t.Helper()
	var evs []event
	for i, l := range lines(t, stdout) {
		var e event
		if err := json.Unmarshal(l, &e); err != nil {
			t.Fatalf("line %d is not JSON: %v: %.200s", i, err, l)
		}
		evs = append(evs, e)
	}
	return evs
}

// kind is "type" plus "/subtype" or "/item.type" when present.
func (e event) kind() string {
	k, _ := e["type"].(string)
	if s, ok := e["subtype"].(string); ok {
		k += "/" + s
	}
	if it, ok := e["item"].(map[string]any); ok {
		k += "/" + it["type"].(string)
	}
	return k
}

func (e event) obj(key string) event {
	m, _ := e[key].(map[string]any)
	return m
}

func (e event) str(key string) string {
	s, _ := e[key].(string)
	return s
}

func kinds(evs []event) []string {
	var ks []string
	for _, e := range evs {
		ks = append(ks, e.kind())
	}
	return ks
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func noFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should not exist (err=%v)", path, err)
	}
}
