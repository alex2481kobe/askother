package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alex2481kobe/orca/internal/run"
)

func TestMCPDeps(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "state")
	cfg := filepath.Join(dir, "config.json")
	d, err := mcpDeps([]string{"ORCA_HOME=" + home, "ORCA_CONFIG=" + cfg, "X=1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.ConfigPath != cfg || d.StateHome != home || d.Env["X"] != "1" || !filepath.IsAbs(d.Self) {
		t.Fatalf("deps: config %q home %q env %v self %q", d.ConfigPath, d.StateHome, d.Env, d.Self)
	}
	if _, ok := d.Adapters["codex"]; !ok || len(d.Adapters) != 2 {
		t.Fatalf("adapters: %v", d.Adapters)
	}
	fi, err := os.Stat(filepath.Join(home, "runs"))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("runs dir: %v %v", fi, err)
	}
}

// A setup problem is reported by name and refused with exit 4.
func TestSetupErrorsExit4(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][4]string{
		"relative home":   {"wait", "rel/state", filepath.Join(dir, "c.json"), "ORCA_HOME"},
		"relative config": {"mcp", filepath.Join(dir, "s"), "rel.json", "ORCA_CONFIG"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("ORCA_HOME", c[1])
			t.Setenv("ORCA_CONFIG", c[2])
			args := []string{c[0]}
			if c[0] == "wait" {
				args = append(args, "x")
			}
			var out, errb bytes.Buffer
			if code := dispatch(args, &out, &errb); code != exitRefused || !strings.HasPrefix(errb.String(), "orca: ") || !strings.Contains(errb.String(), c[3]) {
				t.Fatalf("exit %d, stderr %q", code, errb.String())
			}
		})
	}
}

// wait never reads the config: a broken config file, or a relative
// ORCA_CONFIG, does not stop it from reporting a finished run.
func TestWaitIgnoresConfig(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "state")
	st, err := run.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WithIndexLock(func() error { return st.Put(kRecord(idA, run.StateDone)) }); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "config.json")
	if err := os.WriteFile(broken, []byte(`{"cap": 0`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCA_HOME", home)
	for _, cfg := range []string{broken, "rel.json"} {
		t.Setenv("ORCA_CONFIG", cfg)
		var out, errb bytes.Buffer
		if code := dispatch([]string{"wait", idA}, &out, &errb); code != 0 || !strings.Contains(out.String(), " done ") {
			t.Fatalf("config %s: exit %d, out %q, stderr %q", cfg, code, out.String(), errb.String())
		}
	}
}
