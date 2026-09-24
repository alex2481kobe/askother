package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is wrapped by Resolve when no usable binary exists.
var ErrNotFound = errors.New("worker binary not found")

// Resolve finds the binary for worker: the configured path if set (it must be
// executable; there is no fallback), else the first executable named worker in
// PATH, then in the fixed dirs. It never scans the disk and skips copies inside
// .app bundles, which are only used when configured explicitly. HOME and PATH
// are taken from env.
func Resolve(worker, configured string, env map[string]string) (string, error) {
	if configured != "" {
		if err := executable(configured); err != nil {
			return "", fmt.Errorf("%s: configured binary %q: %w", worker, configured, err)
		}
		return configured, nil
	}
	dirs := searchDirs(env)
	for _, dir := range dirs {
		// A relative PATH entry would resolve against whatever cwd askother has.
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, worker)
		if executable(p) != nil || inAppBundle(p) {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("%s: %w in PATH or %s; set workers.%s.binary in the config",
		worker, ErrNotFound, strings.Join(fixedDirs(env["HOME"]), ", "), worker)
}

func executable(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return errors.New("not executable")
	}
	return nil
}

// inAppBundle reports whether p, or what it links to, is inside a *.app
// directory (e.g. an alpha CLI bundled in a desktop app).
func inAppBundle(p string) bool {
	if real, err := filepath.EvalSymlinks(p); err == nil && hasAppComponent(real) {
		return true
	}
	return hasAppComponent(p)
}

func hasAppComponent(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(p)), "/") {
		if strings.HasSuffix(part, ".app") {
			return true
		}
	}
	return false
}
