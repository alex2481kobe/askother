package worker

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/alex2481kobe/orca/internal/testutil"
)

// cxRun is one fake codex run driven through Build, Consume and Finish.
type cxRun struct {
	cmd       Command
	answer    string // Invocation.TempAnswer
	errs      []error
	final     Final
	rec       testutil.FakeRecord
	stderr    string
	maxLine   int
	noNewline bool // the last line had no trailing newline
}

func cxRunFake(t *testing.T, scenario, arg, resume string) cxRun {
	t.Helper()
	codex, _ := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	r := cxRun{answer: filepath.Join(dir, "answer.tmp")}
	in := Invocation{Binary: codex, Prompt: cxPrompt, CWD: dir, Mode: "read-only", ResumeID: resume, TempAnswer: r.answer}
	var err error
	r.cmd, err = Codex{}.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	recPath := filepath.Join(dir, "record.json")
	env := testutil.WithRecord(testutil.Scenario([]string{"PATH=/usr/bin:/bin", "HOME=" + dir}, scenario, arg), recPath)
	cmd := testutil.GroupCommand(t, r.cmd.Path, env, r.cmd.Args...)
	cmd.Dir, cmd.Stdin = dir, bytes.NewReader(r.cmd.Stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := Codex{}.NewDecoder(in)
	readErr := cxReadLines(stdout, func(line []byte, newline bool) {
		r.maxLine, r.noNewline = max(r.maxLine, len(line)), !newline
		if _, err := d.Consume(line); err != nil {
			r.errs = append(r.errs, err)
		}
	})
	_ = cmd.Wait()
	if readErr != nil {
		t.Fatal(readErr)
	}
	exit := Exit{}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		exit.Signal = ws.Signal().String()
	} else {
		exit = cxCode(cmd.ProcessState.ExitCode())
	}
	if r.final, err = d.Finish(exit); err != nil {
		t.Fatal(err)
	}
	r.rec, r.stderr = testutil.ReadFakeRecord(t, recPath), stderr.String()
	return r
}

// cxReadLines frames stdout the way the supervisor does: ReadBytes under a
// 64 MiB cap, and a last line without a newline is still delivered.
func cxReadLines(r io.Reader, fn func(line []byte, newline bool)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 64<<20 {
			return errors.New("line over 64 MiB")
		}
		if n := len(line); n > 0 {
			nl := line[n-1] == '\n'
			fn(bytes.TrimSuffix(line, []byte("\n")), nl)
		}
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
	}
}

// wantAnswer checks success and that -o holds exactly the fake's final answer.
func (r cxRun) wantAnswer(t *testing.T) []byte {
	t.Helper()
	f := r.final
	if !f.Success || f.Failure != nil || f.AnswerPath != r.answer || f.SessionID == "" || f.SessionID != r.rec.SessionID {
		t.Fatalf("final %+v failure %+v errs %v stderr %s", f, f.Failure, r.errs, r.stderr)
	}
	b, err := os.ReadFile(f.AnswerPath)
	sum := sha256.Sum256(b)
	if err != nil || r.rec.Answer == nil || hex.EncodeToString(sum[:]) != r.rec.Answer.SHA256 {
		t.Fatalf("answer %d bytes (%v) does not match the fake's %+v", len(b), err, r.rec.Answer)
	}
	return b
}

func (r cxRun) wantFailure(t *testing.T, code, msg string) {
	t.Helper()
	f := r.final
	if f.Success || f.AnswerPath != "" || f.Failure == nil || f.Failure.Code != code || !strings.Contains(f.Failure.Message, msg) {
		t.Fatalf("final %+v failure %+v, want %s containing %q", f, f.Failure, code, msg)
	}
}

func TestCodexFakeOK(t *testing.T) {
	t.Parallel()
	r := cxRunFake(t, "ok", "", "")
	if b := r.wantAnswer(t); string(b) != "OK" || len(r.errs) != 0 {
		t.Fatalf("answer %q errs %v", b, r.errs)
	}
	if !slices.Equal(r.rec.Argv[1:], r.cmd.Args) || r.rec.Stdin != cxPrompt || r.rec.Invocation.Sandbox != "read-only" {
		t.Fatalf("record %+v", r.rec)
	}
}

// Progress messages come first; the answer is the -o file, which holds only
// the last message, byte-exact.
func TestCodexFakeProgressThenFinal(t *testing.T) {
	t.Parallel()
	r := cxRunFake(t, "progress_then_final", "", "")
	if b := r.wantAnswer(t); bytes.Contains(b, []byte("Progress")) {
		t.Fatal("the answer holds progress text")
	}
}

func TestCodexFakeScenarios(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ scenario, answer string }{
		{"warning_item", "OK despite a warning"},
		{"no_trailing_newline", "no trailing newline"},
		{"huge_line", ""},
	} {
		r := cxRunFake(t, c.scenario, "", "")
		b := r.wantAnswer(t)
		if len(r.errs) != 0 || (c.answer != "" && string(b) != c.answer) {
			t.Fatalf("%s: errs %v answer %q", c.scenario, r.errs, b)
		}
		switch c.scenario {
		case "no_trailing_newline":
			if !r.noNewline {
				t.Fatal("the fake ended with a newline")
			}
		case "huge_line":
			if r.maxLine < 3<<20 || len(b) < 3<<20 {
				t.Fatalf("longest line %d, answer %d bytes", r.maxLine, len(b))
			}
		}
	}
}

// Codex exits 0 when it cannot write -o; that is a failure, and Orca must
// not have created the file itself.
func TestCodexFakeMissingOFile(t *testing.T) {
	t.Parallel()
	r := cxRunFake(t, "missing_o_file", "", "")
	r.wantFailure(t, "PROTOCOL", "-o")
	if _, err := os.Lstat(r.answer); !errors.Is(err, os.ErrNotExist) || !strings.Contains(r.stderr, "Failed to write") {
		t.Fatalf("answer file: %v; stderr %q", err, r.stderr)
	}
}

func TestCodexFakeFailures(t *testing.T) {
	t.Parallel()
	r := cxRunFake(t, "turn_failed", "", "")
	r.wantFailure(t, "WORKER_FAILED", "The 'fake-model' model is not supported.")
	r = cxRunFake(t, "crash_midstream", "", "")
	r.wantFailure(t, "WORKER_FAILED", "exited 137")
	if len(r.errs) != 1 || !strings.HasPrefix(r.errs[0].Error(), "PROTOCOL") {
		t.Fatalf("the cut line should be a protocol error: %v", r.errs)
	}
	r = cxRunFake(t, "crash_midstream", "kill", "")
	r.wantFailure(t, "WORKER_FAILED", "killed by signal")
}

func TestCodexFakeResume(t *testing.T) {
	t.Parallel()
	r := cxRunFake(t, "resume_same_thread", "", cxThread)
	r.wantAnswer(t)
	in := r.rec.Invocation
	if r.final.SessionID != cxThread || !in.Resume || in.ResumeID != cxThread || in.Sandbox != "" ||
		!slices.Contains(in.Configs, `sandbox_mode="read-only"`) {
		t.Fatalf("session %q invocation %+v", r.final.SessionID, in)
	}
	// A new thread on resume fails even though Codex exited 0 and wrote -o.
	r = cxRunFake(t, "resume_new_thread", "", cxThread)
	r.wantFailure(t, "PROTOCOL", cxThread)
	if _, err := os.Stat(r.answer); err != nil || len(r.errs) != 1 || r.final.SessionID != "" {
		t.Fatalf("answer %v errs %v session %q", err, r.errs, r.final.SessionID)
	}
}

// resume rejects --sandbox, so the mode must go in -c sandbox_mode.
func TestCodexFakeResumeRejectsSandboxFlag(t *testing.T) {
	t.Parallel()
	codex, _ := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	cmd, err := Codex{}.Build(Invocation{Binary: codex, Mode: "read-only", ResumeID: cxThread, TempAnswer: filepath.Join(dir, "a")})
	if err != nil || slices.Contains(cmd.Args, "--sandbox") {
		t.Fatalf("resume argv %q (%v)", cmd.Args, err)
	}
	bad := slices.Insert(slices.Clone(cmd.Args), 2, "--sandbox", "read-only")
	c := testutil.GroupCommand(t, codex, []string{"PATH=/usr/bin:/bin", "HOME=" + dir}, bad...)
	c.Stdin = strings.NewReader(cxPrompt)
	out, err := c.CombinedOutput()
	if c.ProcessState.ExitCode() != 2 || !bytes.Contains(out, []byte("unexpected argument '--sandbox' found")) {
		t.Fatalf("exit %d (%v): %s", c.ProcessState.ExitCode(), err, out)
	}
}
