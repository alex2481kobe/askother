// Package config owns Orca's paths, the optional owner config file, worker
// binary resolution, the worker environment and caller identity.
// Everything here takes the environment and home directory as inputs, so tests
// never touch the real home.
package config

import (
	"errors"
	"fmt"
	"path/filepath"
)

// StateHome is where run files live: ORCA_HOME, else ~/.local/state/orca.
func StateHome(env map[string]string, home string) (string, error) {
	if v := env["ORCA_HOME"]; v != "" {
		// Every orca process must agree on the state dir; a relative one would
		// follow each process's cwd.
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("ORCA_HOME must be an absolute path, got %q", v)
		}
		return filepath.Clean(v), nil
	}
	if err := checkHome(home); err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "orca"), nil
}

// ConfigFile is the owner config: ORCA_CONFIG, else ~/.config/orca/config.json.
func ConfigFile(env map[string]string, home string) (string, error) {
	if v := env["ORCA_CONFIG"]; v != "" {
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("ORCA_CONFIG must be an absolute path, got %q", v)
		}
		return filepath.Clean(v), nil
	}
	if err := checkHome(home); err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "orca", "config.json"), nil
}

func checkHome(home string) error {
	if home == "" {
		return errors.New("home directory is unknown")
	}
	if !filepath.IsAbs(home) {
		return fmt.Errorf("home directory must be absolute, got %q", home)
	}
	return nil
}
