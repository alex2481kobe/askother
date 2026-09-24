package main

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alex2481kobe/askother/internal/testutil"
)

func codexRun(t *testing.T, scenario, arg string) (fakeRun, string) {
	t.Helper()
	codex, _ := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "answer.tmp")
	return runFakeIn(t, dir, codex, scenario, arg, testPrompt, codexArgv(out)...), out
}

func TestCodexOK(t *testing.T) {
	t.Parallel()
	r, out := codexRun(t, "ok", "ANSWER=42")
	evs := events(t, r.stdout)
	want := []string{"thread.started", "turn.started", "item.started/command_execution",
		"item.completed/command_execution", "item.completed/agent_message", "turn.completed"}
	if r.code != 0 || !slices.Equal(kinds(evs), want) {
		t.Fatalf("code %d kinds %v stderr %s", r.code, kinds(evs), r.stderr)
	}
	if got := readFile(t, out); string(got) != "ANSWER=42" {
		t.Fatalf("-o = %q", got)
	}
	if thread := evs[0].str("thread_id"); len(thread) != 36 || thread != r.rec.SessionID {
		t.Fatalf("thread_id %q, record %q", thread, r.rec.SessionID)
	}
	rec := r.rec
	if rec.Dialect != "codex" || rec.Stdin != testPrompt || rec.Invocation.OutputFile != out ||
		rec.Invocation.Sandbox != "read-only" || !slices.Equal(rec.Argv[1:], codexArgv(out)) || !slices.Contains(rec.EnvNames, "HOME") {
		t.Fatalf("record %+v", rec)
	}
	if rd, _ := filepath.EvalSymlinks(r.dir); rec.Cwd != rd && rec.Cwd != r.dir {
		t.Fatalf("cwd %q, want %q", rec.Cwd, r.dir)
	}
	if rec.PGID != rec.PID {
		t.Fatalf("GroupCommand should make the fake a group leader: pid %d pgid %d", rec.PID, rec.PGID)
	}
}

// Ten progress agent_messages, then the long answer; -o holds only the last.
func TestCodexProgressThenFinal(t *testing.T) {
	t.Parallel()
	r, out := codexRun(t, "progress_then_final", "")
	var msgs []string
	for _, e := range events(t, r.stdout) {
		if e.kind() == "item.completed/agent_message" {
			msgs = append(msgs, e.obj("item").str("text"))
		}
	}
	if len(msgs) != 11 || !strings.HasPrefix(msgs[0], "Progress 1 of 10") {
		t.Fatalf("%d agent messages", len(msgs))
	}
	got := readFile(t, out)
	if string(got) != msgs[10] || string(got) != longAnswer() || utf8.RuneCount(got) != 36118 {
		t.Fatalf("-o is %d runes, equals last message: %v", utf8.RuneCount(got), string(got) == msgs[10])
	}
	if r.rec.Answer.SHA256 != sha(got) || r.rec.Answer.Runes != 36118 || r.rec.Answer.Bytes != len(got) {
		t.Fatalf("record answer %+v", r.rec.Answer)
	}
}

// Codex exits 0 when it could not write -o, whether the scenario asks for it
// or the path is really unwritable.
func TestCodexMissingOFile(t *testing.T) {
	t.Parallel()
	r, out := codexRun(t, "missing_o_file", "")
	evs := events(t, r.stdout)
	if r.code != 0 || evs[len(evs)-1].kind() != "turn.completed" || !bytes.Contains(r.stderr, []byte("Failed to write last message file")) {
		t.Fatalf("code %d kinds %v stderr %q", r.code, kinds(evs), r.stderr)
	}
	noFile(t, out)
	codex, _ := testutil.BuildFakeWorker(t)
	dir := t.TempDir()
	out = filepath.Join(dir, "nodir", "answer.tmp")
	r = runFakeIn(t, dir, codex, "ok", "", testPrompt, codexArgv(out)...)
	if r.code != 0 || !bytes.Contains(r.stderr, []byte("Failed to write last message file")) {
		t.Fatalf("unwritable: code %d stderr %q", r.code, r.stderr)
	}
}

func TestCodexTurnFailed(t *testing.T) {
	t.Parallel()
	r, out := codexRun(t, "turn_failed", "")
	evs := events(t, r.stdout)
	if r.code != 1 || !slices.Equal(kinds(evs), []string{"thread.started", "turn.started", "error", "turn.failed"}) {
		t.Fatalf("code %d kinds %v", r.code, kinds(evs))
	}
	if msg := evs[3].obj("error").str("message"); msg != evs[2].str("message") || !strings.HasPrefix(msg, `{"type":"error"`) {
		t.Fatalf("turn.failed message %q", msg)
	}
	noFile(t, out)
}

// A warning item and a top-level error event, then a successful turn.
func TestCodexWarningItem(t *testing.T) {
	t.Parallel()
	r, out := codexRun(t, "warning_item", "")
	ks := kinds(events(t, r.stdout))
	if r.code != 0 || ks[1] != "item.completed/error" || ks[2] != "turn.started" || ks[3] != "error" ||
		ks[len(ks)-1] != "turn.completed" {
		t.Fatalf("code %d kinds %v", r.code, ks)
	}
	if got := readFile(t, out); string(got) != "OK despite a warning" {
		t.Fatalf("-o = %q", got)
	}
}

func TestCodexResume(t *testing.T) {
	t.Parallel()
	codex, _ := testutil.BuildFakeWorker(t)
	for scenario, same := range map[string]bool{"resume_same_thread": true, "ok": true, "resume_new_thread": false} {
		dir := t.TempDir()
		out := filepath.Join(dir, "answer.tmp")
		r := runFakeIn(t, dir, codex, scenario, "", testPrompt, codexResumeArgv(out, testThread)...)
		thread := events(t, r.stdout)[0].str("thread_id")
		if r.code != 0 || (thread == testThread) != same || r.rec.SessionID != thread || !r.rec.Invocation.Resume {
			t.Fatalf("%s: code %d thread %q", scenario, r.code, thread)
		}
		readFile(t, out)
	}
	if r, _ := codexRun(t, "resume_same_thread", ""); r.code != exitMisuse {
		t.Fatalf("resume_same_thread without resume: code %d", r.code)
	}
}

// The rejection is the real CLI's: exit 2, nothing on stdout.
func TestCodexResumeRejectsSandboxFlag(t *testing.T) {
	t.Parallel()
	codex, _ := testutil.BuildFakeWorker(t)
	args := []string{"exec", "resume", "--json", "--sandbox", "read-only", testThread, "-"}
	r := runFake(t, codex, "ok", "", testPrompt, args...)
	if r.code != 2 || len(r.stdout) != 0 || !bytes.Contains(r.stderr, []byte("error: unexpected argument '--sandbox' found")) {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
}

func TestCodexMisuse(t *testing.T) {
	t.Parallel()
	codex, _ := testutil.BuildFakeWorker(t)
	for _, scenario := range []string{"denied_write", "no_such_scenario"} {
		if r := runFake(t, codex, scenario, "", testPrompt, codexArgv("/tmp/unused")...); r.code != exitMisuse {
			t.Fatalf("%s: code %d", scenario, r.code)
		}
	}
	if r := runFake(t, codex, "ok", "", testPrompt, "exec", "-"); r.code != exitMisuse {
		t.Fatalf("no --json: code %d", r.code)
	}
	if r := runFake(t, codex, "ok", "", " \n", codexArgv("/tmp/unused")...); r.code != 1 || len(r.stdout) != 0 {
		t.Fatalf("empty prompt: code %d", r.code)
	}
}
