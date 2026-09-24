package worker

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	cxPrompt = "Summarize the synthetic notes in one line."
	cxThread = "01900000-0000-7000-8000-00000000abcd"
)

var _ Adapter = (*Codex)(nil)

func TestCodexFacts(t *testing.T) {
	f := Codex{}.Facts()
	if f.Name != "codex" || f.DefaultMode != "read-only" ||
		!slices.Equal(f.Modes, []string{"read-only", "workspace-write", "danger-full-access"}) || len(f.Pinned) != 2 ||
		!strings.HasPrefix(f.Pinned[0], `approval_policy="never" (headless: `) ||
		!strings.HasPrefix(f.Pinned[1], "--skip-git-repo-check (") {
		t.Fatalf("facts %+v", f)
	}
}

func TestCodexBuildGolden(t *testing.T) {
	full := Invocation{Binary: "/bin/codex", Prompt: cxPrompt, CWD: "/w", Model: "m1", Effort: "low",
		Mode: "workspace-write", TempAnswer: "/s/a.tmp"}
	opt := []string{"-m", "m1", "-c", `model_reasoning_effort="low"`}
	resume := full
	resume.ResumeID = cxThread
	bare := Invocation{Binary: "/bin/codex", Prompt: cxPrompt, Mode: "read-only", TempAnswer: "/s/a.tmp"}
	cases := []struct {
		name string
		in   Invocation
		want []string
	}{
		{"initial", full, slices.Concat([]string{"exec", "--json", "-o", "/s/a.tmp", "--sandbox", "workspace-write",
			"-c", `approval_policy="never"`, "--skip-git-repo-check"}, opt, []string{"-"})},
		{"resume", resume, slices.Concat([]string{"exec", "resume", "--json", "-o", "/s/a.tmp",
			"-c", `sandbox_mode="workspace-write"`, "-c", `approval_policy="never"`, "--skip-git-repo-check"}, opt,
			[]string{cxThread, "-"})},
		{"bare", bare, []string{"exec", "--json", "-o", "/s/a.tmp", "--sandbox", "read-only",
			"-c", `approval_policy="never"`, "--skip-git-repo-check", "-"}},
	}
	for _, c := range cases {
		cmd, err := Codex{}.Build(c.in)
		if err != nil || cmd.Path != "/bin/codex" || string(cmd.Stdin) != cxPrompt || !slices.Equal(cmd.Args, c.want) {
			t.Errorf("%s: err %v path %q stdin %q\n got %q\nwant %q", c.name, err, cmd.Path, cmd.Stdin, cmd.Args, c.want)
		}
	}
}

func TestCodexBuildRejects(t *testing.T) {
	ok := Invocation{Binary: "/bin/codex", Prompt: cxPrompt, Mode: "read-only", TempAnswer: "/s/a.tmp", Effort: "x_high-2"}
	mut := func(f func(*Invocation)) Invocation { in := ok; f(&in); return in }
	cases := map[string]Invocation{
		"unknown mode":   mut(func(in *Invocation) { in.Mode = "full-auto" }),
		"empty mode":     mut(func(in *Invocation) { in.Mode = "" }),
		"no binary":      mut(func(in *Invocation) { in.Binary = "" }),
		"no temp answer": mut(func(in *Invocation) { in.TempAnswer = "" }),
		"flag thread id": mut(func(in *Invocation) { in.ResumeID = "--last" }),
		"quoted effort":  mut(func(in *Invocation) { in.Effort = `lo"w` }),
		"toml effort":    mut(func(in *Invocation) { in.Effort = "low\nsandbox_mode=x" }),
	}
	for name, in := range cases {
		_, err := Codex{}.Build(in)
		var f *Failure
		if !errors.As(err, &f) || f.Code != "INVALID_INPUT" {
			t.Errorf("%s: err %v, want INVALID_INPUT", name, err)
		}
	}
	if _, err := (Codex{}).Build(ok); err != nil {
		t.Fatalf("control: %v", err)
	}
}

// cxDecode feeds lines to a decoder built for an initial or resumed run
// whose answer path is answer.
func cxDecode(t *testing.T, resume, answer string, exit Exit, lines ...string) (Final, []error) {
	t.Helper()
	in := Invocation{Binary: "/bin/codex", Mode: "read-only", ResumeID: resume, TempAnswer: answer}
	if _, err := (Codex{}).Build(in); err != nil {
		t.Fatal(err)
	}
	d := Codex{}.NewDecoder(in)
	var errs []error
	for _, l := range lines {
		if _, err := d.Consume([]byte(l)); err != nil {
			errs = append(errs, err)
		}
	}
	f, err := d.Finish(exit)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return f, errs
}

func cxCode(n int) Exit { return Exit{Code: &n} }

const (
	cxStarted   = `{"type":"thread.started","thread_id":"` + cxThread + `"}`
	cxCompleted = `{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":2}}`
)

func TestCodexDecodeRules(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.tmp")
	if err := os.WriteFile(file, []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing, isDir := filepath.Join(dir, "none.tmp"), dir
	huge := `{"type":"turn.failed","error":{"message":"` + strings.Repeat("猫", 5000) + `"}}`
	other := `{"type":"thread.started","thread_id":"01900000-0000-7000-8000-0000000000ff"}`
	cases := []struct {
		name, resume, answer string
		exit                 Exit
		lines                []string
		code                 string // "" means success
		errs                 int
	}{
		{"success", "", file, cxCode(0), []string{cxStarted, `{"type":"turn.started"}`, cxCompleted}, "", 0},
		{"unknown types ignored", "", file, cxCode(0), []string{cxStarted, `{"type":"future.x","usage":"no"}`,
			`{"type":"item.completed","item":{"type":"reasoning","text":7}}`, cxCompleted}, "", 0},
		{"odd telemetry ignored", "", file, cxCode(0), []string{cxStarted,
			`{"type":"item.completed","item":{"type":"agent_message","text":7}}`,
			`{"type":"item.started","item":{"type":"command_execution","command":[]}}`,
			`{"type":"turn.completed","usage":{"input_tokens":"many"}}`}, "", 0},
		{"warning item", "", file, cxCode(0), []string{cxStarted,
			`{"type":"item.completed","item":{"id":"i","type":"error","message":"w"}}`, cxCompleted}, "", 0},
		{"malformed json", "", file, cxCode(0), []string{cxStarted, `{"type":`, cxCompleted}, "PROTOCOL", 1},
		{"no type", "", file, cxCode(0), []string{cxStarted, `{}`, cxCompleted}, "PROTOCOL", 1},
		{"no thread id", "", file, cxCode(0), []string{`{"type":"thread.started"}`, cxCompleted}, "PROTOCOL", 1},
		{"no thread.started", "", file, cxCode(0), []string{cxCompleted}, "PROTOCOL", 0},
		{"second thread", "", file, cxCode(0), []string{cxStarted, other, cxCompleted}, "PROTOCOL", 1},
		{"no turn.completed", "", file, cxCode(0), []string{cxStarted}, "PROTOCOL", 0},
		{"no -o file", "", missing, cxCode(0), []string{cxStarted, cxCompleted}, "PROTOCOL", 0},
		{"-o is a directory", "", isDir, cxCode(0), []string{cxStarted, cxCompleted}, "PROTOCOL", 0},
		{"nonzero exit", "", file, cxCode(1), []string{cxStarted, cxCompleted}, "WORKER_FAILED", 0},
		{"unknown exit", "", file, Exit{}, []string{cxStarted, cxCompleted}, "WORKER_FAILED", 0},
		{"resume same", cxThread, file, cxCode(0), []string{cxStarted, cxCompleted}, "", 0},
		{"resume without thread.started", cxThread, file, cxCode(0), []string{cxCompleted}, "PROTOCOL", 0},
		{"resume other", "01900000-0000-7000-8000-0000000000ff", file, cxCode(0), []string{cxStarted, cxCompleted},
			"PROTOCOL", 1},
		{"long failure", "", file, cxCode(1), []string{cxStarted, huge}, "WORKER_FAILED", 0},
	}
	for _, c := range cases {
		f, errs := cxDecode(t, c.resume, c.answer, c.exit, c.lines...)
		if len(errs) != c.errs {
			t.Errorf("%s: consume errors %v, want %d", c.name, errs, c.errs)
		}
		if c.code == "" {
			if !f.Success || f.AnswerPath != c.answer || f.Failure != nil || f.SessionID != cxThread {
				t.Errorf("%s: final %+v failure %+v", c.name, f, f.Failure)
			}
			continue
		}
		if f.Success || f.AnswerPath != "" || f.Failure == nil || f.Failure.Code != c.code {
			t.Errorf("%s: final %+v failure %+v, want %s", c.name, f, f.Failure, c.code)
		}
	}
	f, _ := cxDecode(t, "", file, cxCode(1), cxStarted, huge)
	if m := f.Failure.Message; !f.Failure.Truncated || len(m) > 8<<10 || len(m) < 8<<10-3 || !utf8.ValidString(m) {
		t.Errorf("long failure: %d bytes, truncated %v", len(m), f.Failure.Truncated)
	}
	// A resume that never reported its thread did not prove it continued it.
	if f, _ := cxDecode(t, cxThread, file, cxCode(0), cxCompleted); f.SessionID != "" {
		t.Errorf("unobserved resume reported session %q", f.SessionID)
	}
	if f, _ := (Codex{}).NewDecoder(Invocation{}).Finish(cxCode(0)); f.Success {
		t.Error("a decoder with no TempAnswer succeeded")
	}
}

// A top-level `error` alone never fails a run: it may be a retry notice. Its
// message becomes part of the failure message if the run fails anyway.
func TestCodexErrorEvent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.tmp")
	if err := os.WriteFile(file, []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	retry := `{"type":"error","message":"Reconnecting... 1/5"}`
	final := `{"type":"error","message":"stream disconnected"}`
	cases := []struct {
		name  string
		exit  Exit
		lines []string
		code  string // "" means success
		msgs  []string
	}{
		{"error then turn.completed", cxCode(0), []string{cxStarted, retry, cxCompleted}, "", nil},
		{"odd error then turn.completed", cxCode(0), []string{cxStarted, `{"type":"error","message":7}`, cxCompleted}, "", nil},
		{"error then turn.failed", cxCode(1), []string{cxStarted, retry,
			`{"type":"turn.failed","error":{"message":"quota exceeded"}}`}, "WORKER_FAILED", []string{"quota exceeded"}},
		{"turn.failed without message", cxCode(1), []string{cxStarted, retry, final, `{"type":"turn.failed"}`},
			"WORKER_FAILED", []string{"stream disconnected"}},
		{"error then exit 1", cxCode(1), []string{cxStarted, retry, final}, "WORKER_FAILED",
			[]string{"exited 1", "stream disconnected"}},
		{"error then signal", Exit{Signal: "killed"}, []string{cxStarted, final}, "WORKER_FAILED",
			[]string{"signal killed", "stream disconnected"}},
		{"error then no turn.completed", cxCode(0), []string{cxStarted, final}, "PROTOCOL",
			[]string{"turn.completed", "stream disconnected"}},
	}
	for _, c := range cases {
		f, errs := cxDecode(t, "", file, c.exit, c.lines...)
		if len(errs) != 0 {
			t.Errorf("%s: consume errors %v", c.name, errs)
		}
		if c.code == "" {
			if !f.Success || f.Failure != nil || f.AnswerPath != file {
				t.Errorf("%s: final %+v failure %+v", c.name, f, f.Failure)
			}
			continue
		}
		if f.Success || f.Failure == nil || f.Failure.Code != c.code {
			t.Errorf("%s: final %+v failure %+v, want %s", c.name, f, f.Failure, c.code)
			continue
		}
		for _, m := range c.msgs {
			if !strings.Contains(f.Failure.Message, m) {
				t.Errorf("%s: message %q lacks %q", c.name, f.Failure.Message, m)
			}
		}
	}
}
