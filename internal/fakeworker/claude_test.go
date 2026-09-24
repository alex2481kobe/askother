package main

import (
	"bytes"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/alex2481kobe/orca/internal/testutil"
)

func claudeRun(t *testing.T, scenario, arg string, args ...string) fakeRun {
	t.Helper()
	_, claude := testutil.BuildFakeWorker(t)
	if args == nil {
		args = claudeArgv()
	}
	return runFake(t, claude, scenario, arg, testPrompt, args...)
}

// resultOf returns the last event, which must be the result.
func resultOf(t *testing.T, evs []event) event {
	t.Helper()
	res := evs[len(evs)-1]
	if res.kind() != "result/success" {
		t.Fatalf("last event is %s", res.kind())
	}
	return res
}

// A new session: the fake picks the id and reports it in system/init.
func TestClaudeOK(t *testing.T) {
	t.Parallel()
	r := claudeRun(t, "ok", "", claudeArgv("--model", "fake-model")...)
	evs := events(t, r.stdout)
	want := []string{"system/init", "rate_limit_event", "assistant", "result/success"}
	if r.code != 0 || !slices.Equal(kinds(evs), want) {
		t.Fatalf("code %d kinds %v stderr %s", r.code, kinds(evs), r.stderr)
	}
	init, res := evs[0], resultOf(t, evs)
	if id := init.str("session_id"); len(id) != 36 || res.str("session_id") != id || r.rec.SessionID != id {
		t.Fatalf("session ids %q %q record %q", id, res.str("session_id"), r.rec.SessionID)
	}
	if init.str("model") != "fake-model" || res["is_error"] != false || res.str("result") != "OK" {
		t.Fatalf("init %v result %v", init, res)
	}
	all := lines(t, r.stdout)
	if bytes.HasPrefix(all[len(all)-1], []byte(`{"type"`)) {
		t.Fatalf("result line starts %.40s", all[len(all)-1])
	}
	if r.rec.Dialect != "claude" || r.rec.Stdin != testPrompt || r.rec.Invocation.PermissionMode != "dontAsk" {
		t.Fatalf("record %+v", r.rec)
	}
}

// A resume reports the exact session it was given, unless the scenario
// switches to a new one.
func TestClaudeResume(t *testing.T) {
	t.Parallel()
	for scenario, same := range map[string]bool{"resume_same_thread": true, "ok": true, "resume_new_thread": false} {
		r := claudeRun(t, scenario, "", claudeArgv("--resume", testSession)...)
		evs := events(t, r.stdout)
		id := evs[0].str("session_id")
		if r.code != 0 || (id == testSession) != same || resultOf(t, evs).str("session_id") != id ||
			r.rec.SessionID != id || r.rec.Invocation.ResumeID != testSession {
			t.Fatalf("%s: code %d session %q", scenario, r.code, id)
		}
	}
	if r := claudeRun(t, "resume_new_thread", ""); r.code != exitMisuse {
		t.Fatalf("resume_new_thread without --resume: code %d", r.code)
	}
}

func TestClaudeProgressThenFinal(t *testing.T) {
	t.Parallel()
	r := claudeRun(t, "progress_then_final", "")
	evs := events(t, r.stdout)
	if n := slices.Index(kinds(evs), "result/success"); n != 12 {
		t.Fatalf("kinds %v", kinds(evs))
	}
	answer := resultOf(t, evs).str("result")
	if answer != longAnswer() || utf8.RuneCountInString(answer) != 36118 || r.rec.Answer.SHA256 != sha([]byte(answer)) {
		t.Fatalf("result is %d runes, record %+v", utf8.RuneCountInString(answer), r.rec.Answer)
	}
}

// subtype "success" with is_error true, exit 1.
func TestClaudeIsErrorResult(t *testing.T) {
	t.Parallel()
	r := claudeRun(t, "is_error_result", "")
	evs := events(t, r.stdout)
	res := resultOf(t, evs)
	if r.code != 1 || res["is_error"] != true || res.str("terminal_reason") != "api_error" || res["api_error_status"] != 404.0 {
		t.Fatalf("code %d result %v", r.code, res)
	}
	if evs[1].obj("message").str("model") != "<synthetic>" || !bytes.Contains(r.stderr, []byte("unrecognized_model")) {
		t.Fatalf("assistant %v stderr %q", evs[1], r.stderr)
	}
}

func TestClaudeDeniedWrite(t *testing.T) {
	t.Parallel()
	r := claudeRun(t, "denied_write", "")
	evs := events(t, r.stdout)
	var denied []string
	for _, e := range evs {
		if e.kind() == "system/permission_denied" {
			denied = append(denied, e.str("tool_name"))
		}
	}
	res := resultOf(t, evs)
	denials, _ := res["permission_denials"].([]any)
	if r.code != 0 || !slices.Equal(denied, []string{"Write", "Bash"}) || len(denials) != 2 || res["is_error"] != false {
		t.Fatalf("code %d denied %v denials %v", r.code, denied, denials)
	}
	noFile(t, r.dir+"/fake_denied.txt")
}

func TestClaudeMisuse(t *testing.T) {
	t.Parallel()
	if r := claudeRun(t, "turn_failed", ""); r.code != exitMisuse {
		t.Fatalf("codex-only scenario: code %d", r.code)
	}
	if r := claudeRun(t, "ok", "", "--print", "--output-format", "stream-json"); r.code != 1 ||
		!bytes.Contains(r.stderr, []byte("requires --verbose")) {
		t.Fatalf("stream-json without --verbose: code %d stderr %q", r.code, r.stderr)
	}
	if r := claudeRun(t, "ok", "", claudeArgv("--session-id", testSession)...); r.code != 1 || len(r.stdout) != 0 {
		t.Fatalf("--session-id: code %d", r.code)
	}
}
