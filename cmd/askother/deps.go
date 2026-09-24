package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alex2481kobe/askother/internal/config"
	"github.com/alex2481kobe/askother/internal/lifecycle"
	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/worker"
)

// adapters are the workers this build supports, keyed by Facts().Name.
func adapters() map[string]worker.Adapter {
	return map[string]worker.Adapter{
		"codex":  worker.Codex{},
		"claude": worker.Claude{},
	}
}

// selfPath is the absolute path of the running binary: supervisors are
// started from it and the registration lines print it.
func selfPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find own executable: %w", err)
	}
	return filepath.Abs(p)
}

// stateDeps assembles what every command needs: the store, the adapters and
// the environment. It never reads the config file, so a broken config
// cannot stop `askother wait` or a supervisor. Every error is a setup problem
// the owner must fix (bad ASKOTHER_HOME), reported with exit 4.
func stateDeps(environ []string, home string) (lifecycle.Deps, error) {
	env := config.EnvMap(environ)
	stateHome, err := config.StateHome(env, home)
	if err != nil {
		return lifecycle.Deps{}, err
	}
	store, err := run.Open(stateHome)
	if err != nil {
		return lifecycle.Deps{}, err
	}
	return lifecycle.Deps{Store: store, Adapters: adapters(), StateHome: stateHome, Env: env}, nil
}

// mcpDeps adds what admission needs: the config file's path, which is read
// on each run or send, and this binary, which supervisors are started from.
func mcpDeps(environ []string, home string) (lifecycle.Deps, error) {
	d, err := stateDeps(environ, home)
	if err != nil {
		return d, err
	}
	if d.ConfigPath, err = config.ConfigFile(d.Env, home); err != nil {
		return d, err
	}
	d.Self, err = selfPath()
	return d, err
}

// userHome is the home directory, which may be unknown when ASKOTHER_HOME and
// ASKOTHER_CONFIG are both set.
func userHome() string {
	home, _ := os.UserHomeDir()
	return home
}
