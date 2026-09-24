package config

import (
	"path/filepath"
	"sort"
	"strings"
)

// allowed is the worker environment allowlist (LC_* is matched by prefix).
// Everything else, including every CLAUDE_*, CLAUDECODE and CODEX_* variable,
// is dropped: the caller's harness ids would otherwise leak into the worker.
var allowed = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "SHELL": true, "LANG": true,
	"TMPDIR": true, "TERM": true, "ASKOTHER_HOME": true,
}

// EnvMap turns os.Environ-style entries into a map. Later entries win.
func EnvMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			m[k] = v
		}
	}
	return m
}

// WorkerEnv builds a worker's environment from env using the allowlist only.
// PATH gains the fixed CLI dirs it lacks, for GUI-launched callers.
func WorkerEnv(env map[string]string) []string {
	out := make([]string, 0, len(allowed)+1)
	for k, v := range env {
		if k == "PATH" || !(allowed[k] || strings.HasPrefix(k, "LC_")) {
			continue
		}
		out = append(out, k+"="+v)
	}
	out = append(out, "PATH="+strings.Join(searchDirs(env), string(filepath.ListSeparator)))
	sort.Strings(out)
	return out
}

// fixedDirs are the usual CLI install dirs, searched after PATH.
func fixedDirs(home string) []string {
	dirs := make([]string, 0, 3)
	if filepath.IsAbs(home) {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
}

// searchDirs is PATH (kept as given, in order) followed by each fixed dir
// that PATH does not already contain.
func searchDirs(env map[string]string) []string {
	var dirs []string
	seen := map[string]bool{}
	if p := env["PATH"]; p != "" {
		dirs = filepath.SplitList(p)
		for _, d := range dirs {
			seen[filepath.Clean(d)] = true
		}
	}
	for _, d := range fixedDirs(env["HOME"]) {
		if !seen[d] {
			dirs = append(dirs, d)
		}
	}
	return dirs
}
