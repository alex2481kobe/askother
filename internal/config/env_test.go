package config

import (
	"slices"
	"strings"
	"testing"
)

func callerEnv() map[string]string {
	return map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": "/synthetic/user", "USER": "tester", "SHELL": "/bin/zsh",
		"LANG": "en_US.UTF-8", "LC_ALL": "C", "LC_CTYPE": "UTF-8", "TMPDIR": "/tmp/x", "TERM": "xterm",
		"ASKOTHER_HOME": "/tmp/askother-state", "ASKOTHER_RUN_ID": "caller-run",
		// harness ids, never forwarded
		"CLAUDE_CODE_SESSION_ID": "sess-synthetic", "CLAUDE_CODE_MESSAGING_TOKEN": "tok-synthetic",
		"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/sock", "CLAUDE_PID": "123", "CLAUDECODE": "1",
		"CLAUDE_CODE_CHILD_SESSION": "1", "CODEX_THREAD_ID": "thr", "CODEX_SESSION_ID": "thr",
		"CODEX_SANDBOX": "seatbelt", "CODEX_CI": "1",
		// not on the allowlist
		"ASKOTHER_CALLER": "someone", "OPENAI_API_KEY": "k", "AWS_SECRET": "s", "SSH_AUTH_SOCK": "/s",
		"CLAUDE_EFFORT": "high", "LCX": "no", "lc_all": "no",
	}
}

func TestWorkerEnvAllowlist(t *testing.T) {
	got := EnvMap(WorkerEnv(callerEnv()))
	for _, k := range []string{"HOME", "USER", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TERM", "ASKOTHER_HOME"} {
		if got[k] != callerEnv()[k] {
			t.Errorf("allowed %s: got %q, want %q", k, got[k], callerEnv()[k])
		}
	}
	for _, k := range []string{
		"CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_MESSAGING_SOCKET",
		"CLAUDE_PID", "CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CODEX_THREAD_ID", "CODEX_SESSION_ID",
		"CODEX_SANDBOX", "CODEX_CI", "ASKOTHER_RUN_ID", "ASKOTHER_CALLER", "OPENAI_API_KEY", "AWS_SECRET", "SSH_AUTH_SOCK",
		"CLAUDE_EFFORT", "LCX", "lc_all",
	} {
		if v, ok := got[k]; ok {
			t.Errorf("denied %s present: %q", k, v)
		}
	}
	if len(got) != 10 {
		t.Errorf("got %d vars: %v", len(got), got)
	}
}

func TestWorkerEnvOptionalVars(t *testing.T) {
	got := EnvMap(WorkerEnv(map[string]string{"HOME": "/h"}))
	if _, ok := got["ASKOTHER_HOME"]; ok {
		t.Error("ASKOTHER_HOME set although absent")
	}
}

func TestWorkerEnvPathAppendsFixedDirs(t *testing.T) {
	// A GUI-launched caller has a minimal PATH.
	got := EnvMap(WorkerEnv(map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/synthetic/user"}))
	want := "/usr/bin:/bin:/synthetic/user/.local/bin:/opt/homebrew/bin:/usr/local/bin"
	if got["PATH"] != want {
		t.Errorf("PATH: got %q, want %q", got["PATH"], want)
	}
	// Dirs already present keep their position and are not duplicated.
	got = EnvMap(WorkerEnv(map[string]string{"PATH": "/usr/local/bin/:/usr/bin", "HOME": "/synthetic/user"}))
	want = "/usr/local/bin/:/usr/bin:/synthetic/user/.local/bin:/opt/homebrew/bin"
	if got["PATH"] != want {
		t.Errorf("PATH: got %q, want %q", got["PATH"], want)
	}
	// No PATH and no HOME at all.
	got = EnvMap(WorkerEnv(nil))
	if got["PATH"] != "/opt/homebrew/bin:/usr/local/bin" {
		t.Errorf("PATH: got %q", got["PATH"])
	}
}

func TestWorkerEnvIsSortedAndWellFormed(t *testing.T) {
	out := WorkerEnv(callerEnv())
	if !slices.IsSorted(out) {
		t.Errorf("not sorted: %v", out)
	}
	for _, kv := range out {
		if !strings.Contains(kv, "=") {
			t.Errorf("malformed %q", kv)
		}
	}
}

func TestEnvMap(t *testing.T) {
	m := EnvMap([]string{"A=1", "B=x=y", "A=2", "=bad", "noequals", "E="})
	if m["A"] != "2" || m["B"] != "x=y" || len(m) != 3 || m["E"] != "" {
		t.Errorf("got %v", m)
	}
}
