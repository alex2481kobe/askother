package worker

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/alex2481kobe/askother/internal/testutil"
)

const clSession = "00000000-0000-4000-8000-000000000001"

var _ Adapter = (*Claude)(nil)

func TestClaudeFacts(t *testing.T) {
	f := (Claude{}).Facts()
	if f.Name != "claude" || f.DefaultMode != "dontAsk" ||
		!slices.Contains(f.Modes, "bypassPermissions") || len(f.Modes) != 7 ||
		len(f.Pinned) != 1 || !strings.HasPrefix(f.Pinned[0], "--permission-prompts none (") {
		t.Fatalf("facts %+v", f)
	}
}

func TestClaudeBuildGolden(t *testing.T) {
	base := Invocation{Binary: "/bin/claude", Prompt: "hi", Mode: "dontAsk", TempAnswer: "/s/a.tmp"}
	full, resume := base, base
	full.Model, full.Effort = "m", "low"
	resume.ResumeID = clSession
	head := "--print --output-format stream-json --verbose --permission-mode dontAsk --permission-prompts none"
	for _, c := range []struct {
		in   Invocation
		want string
	}{
		{base, head},
		{full, head + " --model m --effort low"},
		{resume, head + " --resume " + clSession},
	} {
		cmd, err := (Claude{}).Build(c.in)
		if err != nil || cmd.Path != "/bin/claude" || string(cmd.Stdin) != "hi" || strings.Join(cmd.Args, " ") != c.want {
			t.Errorf("Build = %q %q, %v\nwant %q", cmd.Path, strings.Join(cmd.Args, " "), err, c.want)
		}
	}
	bad := []Invocation{
		{Binary: "c", Mode: "readonly", TempAnswer: "a"},
		{Binary: "c", Mode: "", TempAnswer: "a"},
		{Binary: "", Mode: "plan", TempAnswer: "a"},
		{Binary: "c", Mode: "plan"},
		{Binary: "c", Mode: "plan", TempAnswer: "a", ResumeID: "--continue"},
	}
	for _, in := range bad {
		var f *Failure
		if _, err := (Claude{}).Build(in); !errors.As(err, &f) || f.Code != "INVALID_INPUT" {
			t.Errorf("Build(%+v) = %v, want INVALID_INPUT", in, err)
		}
	}
}

type clRun struct {
	errs   []error
	final  Final
	ferr   error
	rec    testutil.FakeRecord
	answer []byte // nil when no answer file exists
}

// runClaude builds the command, plays the fake, and decodes its stdout the
// way the supervisor does.
func runClaude(t *testing.T, scenario string, in Invocation) clRun {
	t.Helper()
	_, bin := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	in.Binary, in.TempAnswer = bin, filepath.Join(dir, "answer.tmp")
	if in.Mode == "" {
		in.Mode = "dontAsk"
	}
	if in.Prompt == "" {
		in.Prompt = "synthetic prompt"
	}
	c, err := Claude{}.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	recPath := filepath.Join(dir, "rec.json")
	env := testutil.WithRecord(testutil.Scenario([]string{"PATH=" + os.Getenv("PATH")}, scenario, ""), recPath)
	cmd := testutil.GroupCommand(t, c.Path, env, c.Args...)
	cmd.Dir, cmd.Stdin = dir, bytes.NewReader(c.Stdin)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var r clRun
	d := Claude{}.NewDecoder(in)
	rerr := readLines(out, func(line []byte) {
		if _, err := d.Consume(line); err != nil {
			r.errs = append(r.errs, err)
		}
	})
	werr := cmd.Wait()
	var ee *exec.ExitError
	if rerr != nil || (werr != nil && !errors.As(werr, &ee)) {
		t.Fatalf("read %v wait %v", rerr, werr)
	}
	r.final, r.ferr = d.Finish(exitOf(cmd.ProcessState))
	r.rec = testutil.ReadFakeRecord(t, recPath)
	if b, err := os.ReadFile(in.TempAnswer); err == nil {
		r.answer = b
	}
	return r
}

// readLines delivers whole lines, and a last line without '\n'.
func readLines(r io.Reader, fn func([]byte)) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fn(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func exitOf(ps *os.ProcessState) Exit {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return Exit{Signal: ws.Signal().String()}
	}
	code := ps.ExitCode()
	return Exit{Code: &code}
}

// wantAnswer checks the published answer is byte-exact with what the fake
// sent, and the session is the one the fake reported.
func (r clRun) wantAnswer(t *testing.T) {
	t.Helper()
	if !r.final.Success || r.ferr != nil || r.final.Failure != nil || len(r.errs) > 0 || r.rec.Answer == nil {
		t.Fatalf("final %+v ferr %v errs %v", r.final, r.ferr, r.errs)
	}
	sum := sha256.Sum256(r.answer)
	if hex.EncodeToString(sum[:]) != r.rec.Answer.SHA256 || len(r.answer) != r.rec.Answer.Bytes {
		t.Fatalf("answer %d bytes, fake sent %d", len(r.answer), r.rec.Answer.Bytes)
	}
	if fi, err := os.Stat(r.final.AnswerPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("answer file %v %v", fi, err)
	}
	if r.final.SessionID == "" || r.final.SessionID != r.rec.SessionID {
		t.Fatalf("session %q, fake reported %q", r.final.SessionID, r.rec.SessionID)
	}
}

func TestClaudeFakeSuccess(t *testing.T) {
	for _, scenario := range []string{"ok", "progress_then_final", "huge_line", "split_utf8", "no_trailing_newline", "denied_write"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			r := runClaude(t, scenario, Invocation{})
			r.wantAnswer(t)
			if slices.Contains(r.rec.Argv, "--session-id") || slices.Contains(r.rec.Argv, "--mcp-config") {
				t.Fatalf("argv %q", r.rec.Argv)
			}
			if scenario == "progress_then_final" && r.rec.Answer.Runes != 36118 {
				t.Fatalf("runes %d", r.rec.Answer.Runes)
			}
			if scenario == "split_utf8" && r.rec.SplitWrites == 0 {
				t.Fatal("fake did not split writes")
			}
		})
	}
}

func TestClaudeFakeResume(t *testing.T) {
	t.Parallel()
	r := runClaude(t, "ok", Invocation{Mode: "manual", ResumeID: clSession})
	r.wantAnswer(t)
	if r.final.SessionID != clSession || r.rec.Invocation.ResumeID != clSession {
		t.Fatalf("session %q, fake resumed %q", r.final.SessionID, r.rec.Invocation.ResumeID)
	}
}

func TestClaudeFakeFailures(t *testing.T) {
	for _, c := range []struct{ scenario, code, msg string }{
		{"is_error_result", "WORKER_FAILED", "api_error_status 404, exited 1): There's an issue"},
		{"crash_midstream", "PROTOCOL", "malformed event"},
	} {
		t.Run(c.scenario, func(t *testing.T) {
			t.Parallel()
			r := runClaude(t, c.scenario, Invocation{})
			f := r.final.Failure
			if r.final.Success || r.ferr != nil || f == nil || f.Code != c.code || !strings.Contains(f.Message, c.msg) {
				t.Fatalf("final %+v failure %+v ferr %v", r.final, f, r.ferr)
			}
			if r.answer != nil || r.final.AnswerPath != "" {
				t.Fatalf("published %q", r.answer)
			}
		})
	}
}
