//go:build realcli

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests call the real `claude` (4 processes, two tiny haiku turns) to pin
// shapes the fake worker imitates. Run: go test -tags realcli -run RealClaude.
// Only chosen fields are logged; never raw output (it can carry account data).

type realOut struct {
	stdout, stderr []byte
	code           int
}

func realClaude(t *testing.T, dir string, cmd Command, extra ...string) realOut {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, cmd.Path, append(cmd.Args, extra...)...)
	c.Dir = dir
	// An allowlisted environment, never the caller's whole environment.
	for _, k := range []string{"PATH", "HOME", "USER", "SHELL", "LANG", "TMPDIR", "TERM"} {
		if v, ok := os.LookupEnv(k); ok {
			c.Env = append(c.Env, k+"="+v)
		}
	}
	c.Stdin = bytes.NewReader(cmd.Stdin)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("run claude: %v", err)
	}
	return realOut{out.Bytes(), errb.Bytes(), c.ProcessState.ExitCode()}
}

func realBuild(t *testing.T, in Invocation) (*Claude, Command) {
	t.Helper()
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH")
	}
	in.Binary, in.Model = bin, "haiku"
	a := &Claude{}
	cmd, err := a.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	return a, cmd
}

func TestRealClaude(t *testing.T) {
	// 1. Argv error, no model call. `default` is in Facts.Modes; if the CLI
	// accepted it, the error names the bad output format instead.
	tmp := func() string { return filepath.Join(t.TempDir(), "answer.tmp") }
	_, cmd := realBuild(t, Invocation{Mode: "default", Prompt: "x", TempAnswer: tmp()})
	r := realClaude(t, t.TempDir(), cmd, "--output-format", "bogus")
	t.Logf("argv error: code %d stdout %d bytes stderr %q", r.code, len(r.stdout), r.stderr)
	if r.code != 1 || len(r.stdout) != 0 || !strings.Contains(string(r.stderr), "'--output-format <format>' argument 'bogus' is invalid") {
		t.Errorf("argv error shape changed")
	}

	// 2. Empty prompt.
	_, cmd = realBuild(t, Invocation{Mode: "dontAsk", TempAnswer: tmp()})
	r = realClaude(t, t.TempDir(), cmd)
	t.Logf("empty prompt: code %d stdout %d bytes stderr %q", r.code, len(r.stdout), r.stderr)
	if r.code != 1 || len(r.stdout) != 0 || !strings.Contains(string(r.stderr), "Error: Input must be provided either through stdin") {
		t.Errorf("empty prompt shape changed")
	}

	// 3. One tiny turn in a new session; Claude chooses the session id.
	// Claude keeps sessions per project directory, so both turns share a cwd.
	in := Invocation{Mode: "dontAsk", Prompt: "Reply with exactly: OK", CWD: t.TempDir(), TempAnswer: tmp()}
	f, answer := realTurn(t, in)
	if !f.Success || string(answer) != "OK" || f.SessionID == "" {
		t.Fatalf("turn shape changed")
	}

	// 4. Resume that session; it must report the same id.
	in.Prompt, in.ResumeID, in.TempAnswer = "Reply with exactly: AGAIN", f.SessionID, tmp()
	g, answer := realTurn(t, in)
	if !g.Success || string(answer) != "AGAIN" || g.SessionID != f.SessionID {
		t.Errorf("resume shape changed")
	}
}

// realTurn runs one turn through Build, Consume and Finish. Only chosen
// fields are logged.
func realTurn(t *testing.T, in Invocation) (Final, []byte) {
	t.Helper()
	a, cmd := realBuild(t, in)
	r := realClaude(t, in.CWD, cmd)
	d := a.NewDecoder(in)
	var denials string
	for _, line := range bytes.Split(r.stdout, []byte("\n")) {
		if _, err := d.Consume(line); err != nil {
			t.Errorf("consume: %v", err)
		}
		var raw struct {
			Type    string          `json:"type"`
			Denials json.RawMessage `json:"permission_denials"`
		}
		if json.Unmarshal(line, &raw) == nil && raw.Type == "result" {
			denials = string(raw.Denials)
		}
	}
	f, err := d.Finish(Exit{Code: &r.code})
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := os.ReadFile(in.TempAnswer)
	t.Logf("turn (resume %v): code %d success %v denials %s answer %q failure %+v",
		in.ResumeID != "", r.code, f.Success, denials, answer, f.Failure)
	return f, answer
}
