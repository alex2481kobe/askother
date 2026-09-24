package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alex2481kobe/orca/internal/testutil"
)

// both runs a scenario once per dialect and returns the answer the worker
// published: the -o file for Codex, result.result for Claude.
func both(t *testing.T, scenario, arg string, check func(t *testing.T, r fakeRun, answer []byte)) {
	codex, claude := testutil.BuildFakeWorker(t)
	t.Run("codex", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		out := filepath.Join(dir, "answer.tmp")
		r := runFakeIn(t, dir, codex, scenario, arg, testPrompt, codexArgv(out)...)
		check(t, r, readFile(t, out))
	})
	t.Run("claude", func(t *testing.T) {
		t.Parallel()
		r := runFake(t, claude, scenario, arg, testPrompt, claudeArgv()...)
		evs := events(t, r.stdout)
		check(t, r, []byte(resultOf(t, evs).str("result")))
	})
}

func TestHugeLine(t *testing.T) {
	t.Parallel()
	both(t, "huge_line", "", func(t *testing.T, r fakeRun, answer []byte) {
		longest := 0
		for _, l := range lines(t, r.stdout) {
			longest = max(longest, len(l))
		}
		if r.code != 0 || longest <= 1<<20 || len(answer) < 3<<20 || !utf8.Valid(answer) {
			t.Fatalf("code %d longest line %d answer %d bytes", r.code, longest, len(answer))
		}
		if r.rec.Answer.Bytes != len(answer) || r.rec.Answer.SHA256 != sha(answer) {
			t.Fatalf("answer differs from record %+v", r.rec.Answer)
		}
	})
}

func TestHugeLineSizeArg(t *testing.T) {
	t.Parallel()
	both(t, "huge_line", "70000", func(t *testing.T, r fakeRun, answer []byte) {
		if len(answer) < 70000 || len(answer) > 70200 {
			t.Fatalf("answer %d bytes for arg 70000", len(answer))
		}
	})
}

func TestNoTrailingNewline(t *testing.T) {
	t.Parallel()
	both(t, "no_trailing_newline", "", func(t *testing.T, r fakeRun, answer []byte) {
		if r.code != 0 || bytes.HasSuffix(r.stdout, []byte("\n")) || string(answer) != "no trailing newline" {
			t.Fatalf("code %d ends %q answer %q", r.code, r.stdout[max(0, len(r.stdout)-20):], answer)
		}
		evs := events(t, r.stdout) // the unterminated line still decodes
		if k := evs[len(evs)-1].kind(); k != "turn.completed" && k != "result/success" {
			t.Fatalf("last event %s", k)
		}
	})
}

func TestSplitUTF8(t *testing.T) {
	t.Parallel()
	both(t, "split_utf8", "", func(t *testing.T, r fakeRun, answer []byte) {
		if r.code != 0 || !utf8.Valid(r.stdout) || string(answer) != splitAnswer() {
			t.Fatalf("code %d valid %v answer %q", r.code, utf8.Valid(r.stdout), answer)
		}
		if !strings.Contains(string(answer), "\u732b\U0001F41F") || r.rec.SplitWrites < 100 {
			t.Fatalf("split writes %d", r.rec.SplitWrites)
		}
	})
}

// A reader sees chunks that end mid-codepoint, so it must not decode per read.
func TestSplitUTF8ReadsEndMidCodepoint(t *testing.T) {
	t.Parallel()
	codex, _ := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	cmd := testutil.GroupCommand(t, codex, testutil.Scenario(baseEnv(dir), "split_utf8", ""),
		codexArgv(filepath.Join(dir, "answer.tmp"))...)
	cmd.Dir, cmd.Stdin = dir, strings.NewReader(testPrompt)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	var all []byte
	broken := 0
	buf := make([]byte, 64<<10)
	_ = pr.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		n, err := pr.Read(buf)
		if n > 0 && !utf8.Valid(buf[:n]) {
			broken++
		}
		all = append(all, buf[:n]...)
		if err != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if broken == 0 || !utf8.Valid(all) {
		t.Fatalf("%d reads ended mid-codepoint; whole stream valid: %v", broken, utf8.Valid(all))
	}
}
