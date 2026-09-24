package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || c != (Config{}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestLoadFull(t *testing.T) {
	p := writeConfig(t, `{"workers":{"codex":{"binary":"/opt/x/codex"},"claude":{"binary":"/opt/y/claude"}}}`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Workers: Workers{Codex: Worker{Binary: "/opt/x/codex"}, Claude: Worker{Binary: "/opt/y/claude"}}}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadEmptyObject(t *testing.T) {
	c, err := Load(writeConfig(t, `{}`))
	if err != nil || c != (Config{}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		// Fields that used to exist are unknown now.
		"removed cap":       {`{"cap":4}`, `unknown field "cap"`},
		"removed keep read": {`{"keep_read_hours":2}`, `unknown field "keep_read_hours"`},
		"removed unread":    {`{"keep_unread_days":2}`, `unknown field "keep_unread_days"`},
		"unknown nested":    {`{"workers":{"codex":{"path":"/x"}}}`, `unknown field "path"`},
		"unknown worker":    {`{"workers":{"jev":{}}}`, `unknown field "jev"`},
		"relative binary":   {`{"workers":{"claude":{"binary":"bin/claude"}}}`, "workers.claude.binary must be an absolute path"},
		"relative codex":    {`{"workers":{"codex":{"binary":"codex"}}}`, "workers.codex.binary"},
		"wrong type":        {`{"workers":{"codex":{"binary":1}}}`, "cannot unmarshal"},
		"trailing data":     {`{} {}`, "after the top-level"},
		"empty file":        {``, "empty"},
		"not json":          {`cap=4`, "invalid character"},
		"array top level":   {`[]`, "cannot unmarshal"},
	}
	for name, c := range cases {
		p := writeConfig(t, c.body)
		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: got %v, want error containing %q and the path", name, err, c.want)
		}
	}
}

func TestLoadUnreadableIsError(t *testing.T) {
	// A directory at the config path is not "missing": it must not silently mean no overrides.
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("want error")
	}
}
