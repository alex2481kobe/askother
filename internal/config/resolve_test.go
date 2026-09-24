package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A name no real install has, so the real fixed dirs never match.
const fakeName = "askother-config-test-worker"

func fakeExe(t *testing.T, dir, name, script string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveConfiguredWins(t *testing.T) {
	root := t.TempDir()
	onPath := fakeExe(t, filepath.Join(root, "on path"), fakeName, "")
	// Configured copies are used even inside an .app bundle.
	configured := fakeExe(t, filepath.Join(root, "My App.app", "Contents", "Resources"), fakeName, "")
	got, err := Resolve(fakeName, configured, map[string]string{"PATH": filepath.Dir(onPath), "HOME": root})
	if err != nil || got != configured {
		t.Fatalf("got %q, %v; want %q", got, err, configured)
	}
}

func TestResolveConfiguredMustBeExecutable(t *testing.T) {
	root := t.TempDir()
	onPath := fakeExe(t, filepath.Join(root, "bin"), fakeName, "")
	noExec := filepath.Join(root, "plain")
	if err := os.WriteFile(noExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PATH": filepath.Dir(onPath), "HOME": root}
	for _, p := range []string{noExec, filepath.Join(root, "missing"), root} {
		// No fallback to PATH: the owner asked for this binary.
		if got, err := Resolve(fakeName, p, env); err == nil {
			t.Errorf("configured %q: got %q, want error", p, got)
		}
	}
}

func TestResolvePathOrder(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first dir")
	second := filepath.Join(root, "second dir")
	fakeExe(t, second, fakeName, "")
	want := fakeExe(t, first, fakeName, "")
	fakeExe(t, filepath.Join(root, ".local", "bin"), fakeName, "")
	env := map[string]string{"PATH": first + ":" + second, "HOME": root}
	if got, err := Resolve(fakeName, "", env); err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestResolveSkipsUnusable(t *testing.T) {
	root := t.TempDir()
	noExec := filepath.Join(root, "noexec")
	if err := os.MkdirAll(noExec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noExec, fakeName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	isDir := filepath.Join(root, "isdir")
	if err := os.MkdirAll(filepath.Join(isDir, fakeName), 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "Chat.app", "Contents", "Resources")
	fakeExe(t, bundle, fakeName, "")
	// A plain dir symlinking into a bundle is still the bundled copy.
	linkDir := filepath.Join(root, "links")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(bundle, fakeName), filepath.Join(linkDir, fakeName)); err != nil {
		t.Fatal(err)
	}
	// A relative PATH entry that does resolve from the current directory.
	t.Chdir(root)
	relDir := "rel"
	fakeExe(t, filepath.Join(root, relDir), fakeName, "")
	want := fakeExe(t, filepath.Join(root, ".local", "bin"), fakeName, "")
	env := map[string]string{"PATH": strings.Join([]string{noExec, isDir, bundle, linkDir, relDir, ""}, ":"), "HOME": root}
	if got, err := Resolve(fakeName, "", env); err != nil || got != want {
		t.Fatalf("got %q, %v; want the fixed-dir copy %q", got, err, want)
	}
}

func TestResolveFixedDirsWithMinimalPath(t *testing.T) {
	root := t.TempDir()
	want := fakeExe(t, filepath.Join(root, ".local", "bin"), fakeName, "")
	if got, err := Resolve(fakeName, "", map[string]string{"PATH": "/usr/bin:/bin", "HOME": root}); err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestResolveNotFound(t *testing.T) {
	_, err := Resolve(fakeName, "", map[string]string{"PATH": t.TempDir(), "HOME": t.TempDir()})
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "workers."+fakeName+".binary") {
		t.Fatalf("got %v", err)
	}
}
