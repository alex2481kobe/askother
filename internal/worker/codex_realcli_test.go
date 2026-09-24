//go:build realcli

package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/testutil"
)

// Four tiny `codex exec` calls against the user's own Codex config; two of
// them reach the model. Run with: go test -tags realcli -run RealCodex ./internal/worker

type cxReal struct {
	stdoutLines int
	stderr      string
	code        int
	final       Final
	session     string
	oAtTurn     bool          // -o existed when turn.completed was decoded
	oDelay      time.Duration // from decoding turn.completed to -o appearing
}

func cxRealRun(t *testing.T, a *Codex, in Invocation, argv func([]string) []string) cxReal {
	t.Helper()
	cmd, err := a.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	args := cmd.Args
	if argv != nil {
		args = argv(slices.Clone(args))
	}
	c := testutil.GroupCommand(t, cmd.Path, os.Environ(), args...)
	c.Dir, c.Stdin = in.CWD, bytes.NewReader(cmd.Stdin)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	var appeared atomic.Int64 // unix nanos when -o first existed
	stop := make(chan struct{})
	go func() {
		for {
			if _, err := os.Lstat(in.TempAnswer); err == nil {
				appeared.Store(time.Now().UnixNano())
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	timer := time.AfterFunc(3*time.Minute, func() { _ = c.Process.Kill() })
	defer timer.Stop()
	var r cxReal
	var turnAt time.Time
	d := a.NewDecoder(in)
	readErr := cxReadLines(stdout, func(line []byte, _ bool) {
		r.stdoutLines++
		u, err := d.Consume(line)
		if err != nil {
			t.Errorf("consume: %v", err)
		}
		if u.SessionID != "" {
			r.session = u.SessionID
		}
		var ev struct{ Type string }
		if json.Unmarshal(line, &ev) == nil && ev.Type == "turn.completed" && turnAt.IsZero() {
			turnAt = time.Now()
			_, err := os.Lstat(in.TempAnswer)
			r.oAtTurn = err == nil
		}
	})
	waitErr := c.Wait()
	close(stop)
	var exitErr *exec.ExitError
	if readErr != nil || (waitErr != nil && !errors.As(waitErr, &exitErr)) {
		t.Fatalf("read %v wait %v", readErr, waitErr)
	}
	r.code, r.stderr = c.ProcessState.ExitCode(), stderr.String()
	if r.final, err = d.Finish(cxCode(r.code)); err != nil {
		t.Fatal(err)
	}
	if ns := appeared.Load(); ns != 0 && !turnAt.IsZero() {
		r.oDelay = time.Unix(0, ns).Sub(turnAt)
	}
	return r
}

func TestRealCodex(t *testing.T) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not on PATH")
	}
	dir := t.TempDir()
	base := Invocation{Binary: bin, CWD: dir, Model: "gpt-6-luna", Effort: "low", Mode: "read-only"}
	answer := func(name string) string { return filepath.Join(dir, name) }

	// 1. Empty prompt: no model call, stderr only.
	in := base
	in.TempAnswer = answer("empty.tmp")
	r := cxRealRun(t, &Codex{}, in, nil)
	t.Logf("empty prompt: exit %d, %d stdout lines, stderr %q", r.code, r.stdoutLines, r.stderr)
	if r.code != 1 || r.stdoutLines != 0 || !strings.Contains(r.stderr, "No prompt provided via stdin.") ||
		r.final.Success || r.final.Failure.Code != "WORKER_FAILED" {
		t.Errorf("empty prompt shape changed")
	}

	// 2. Usage error: --sandbox after resume is rejected by the parser.
	in = base
	in.TempAnswer, in.ResumeID, in.Prompt = answer("usage.tmp"), "01900000-0000-7000-8000-00000000abcd", "unused"
	r = cxRealRun(t, &Codex{}, in, func(a []string) []string { return slices.Insert(a, 2, "--sandbox", "read-only") })
	t.Logf("usage error: exit %d, %d stdout lines, stderr %q", r.code, r.stdoutLines, r.stderr)
	if r.code != 2 || r.stdoutLines != 0 || !strings.Contains(r.stderr, "unexpected argument '--sandbox' found") {
		t.Errorf("usage error shape changed")
	}

	// 3. A real turn. -o is written after turn.completed, so it may only be
	// read after exit.
	in = base
	in.TempAnswer, in.Prompt = answer("first.tmp"), "Do not use any tools. Reply with exactly: OK"
	r = cxRealRun(t, &Codex{}, in, nil)
	t.Logf("initial: exit %d, success %v, -o at turn.completed %v, -o delay %v", r.code, r.final.Success, r.oAtTurn, r.oDelay)
	b, _ := os.ReadFile(in.TempAnswer)
	if !r.final.Success || strings.TrimSpace(string(b)) != "OK" || r.session == "" || r.oAtTurn {
		t.Fatalf("initial: final %+v failure %+v answer %q stderr tail %q", r.final, r.final.Failure, b, cxTail(r.stderr))
	}

	// 4. Resume the same thread with the mode in -c sandbox_mode.
	thread := r.session
	in.TempAnswer, in.ResumeID, in.Prompt = answer("second.tmp"), thread, "Do not use any tools. Reply with exactly: AGAIN"
	r = cxRealRun(t, &Codex{}, in, nil)
	b, _ = os.ReadFile(in.TempAnswer)
	t.Logf("resume: exit %d, success %v, same thread %v, -o delay %v", r.code, r.final.Success, r.session == thread, r.oDelay)
	if !r.final.Success || r.final.SessionID != thread || strings.TrimSpace(string(b)) != "AGAIN" {
		t.Fatalf("resume: final %+v failure %+v answer %q stderr tail %q", r.final, r.final.Failure, b, cxTail(r.stderr))
	}
}

func cxTail(s string) string {
	if len(s) > 2000 {
		return s[len(s)-2000:]
	}
	return s
}
