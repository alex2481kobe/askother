package lifecycle

import (
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/alex2481kobe/orca/internal/run"
)

// StartRequest is the `run` tool input. Mode empty means the worker's
// Facts.DefaultMode.
type StartRequest struct {
	Key, Worker, Prompt, CWD, Model, Effort, Mode, Role, Task string
	TimeoutMS                                                 *int64
}

// MaxTimeout is the longest timeout_ms a run accepts.
const MaxTimeout = 7 * 24 * time.Hour

// Start admits a new run and hands it to `d.Self supervise <id>`. An
// identical retry of a key returns the existing run with reused=true, even
// if its cwd or worker binary has disappeared since; a different request
// under the same key is KEY_CONFLICT.
func Start(d Deps, c Caller, req StartRequest) (*run.Run, bool, error) {
	if req.Key == "" {
		return nil, false, run.Errorf(run.CodeInvalidInput, "key is required")
	}
	a, ok := d.Adapters[req.Worker]
	if !ok {
		return nil, false, run.Errorf(run.CodeInvalidInput, "unknown worker %q (workers: %v)", req.Worker, workerNames(d))
	}
	facts := a.Facts()
	switch {
	case req.Prompt == "":
		return nil, false, run.Errorf(run.CodeInvalidInput, "prompt is required")
	case req.TimeoutMS != nil && (*req.TimeoutMS < 1 || *req.TimeoutMS > MaxTimeout.Milliseconds()):
		return nil, false, run.Errorf(run.CodeInvalidInput,
			"timeout_ms must be from 1 to %d (7 days); omit it for no timeout", MaxTimeout.Milliseconds())
	}
	mode := req.Mode
	if mode == "" {
		mode = facts.DefaultMode // before hashing, so omitting it and naming it are the same request
	}
	if !slices.Contains(facts.Modes, mode) {
		return nil, false, run.Errorf(run.CodeInvalidInput, "unknown mode %q for %s (modes: %v)", mode, req.Worker, facts.Modes)
	}
	rq := run.Request{Worker: req.Worker, Prompt: req.Prompt, CWD: req.CWD, Model: req.Model,
		Effort: req.Effort, Mode: mode, Role: req.Role, Task: req.Task, TimeoutMS: req.TimeoutMS}
	hash, err := run.RequestHash(rq)
	if err != nil {
		return nil, false, err
	}
	return admit(d, c, admission{key: req.Key, hash: hash, prepare: func([]*run.Run) (*run.Run, error) {
		if err := checkCWD(rq.CWD); err != nil {
			return nil, err
		}
		return &run.Run{Request: rq}, nil
	}})
}

func checkCWD(cwd string) error {
	if !filepath.IsAbs(cwd) {
		return run.Errorf(run.CodeInvalidInput, "cwd must be an absolute path, got %q", cwd)
	}
	fi, err := os.Stat(cwd)
	if err != nil || !fi.IsDir() {
		return run.Errorf(run.CodeInvalidInput, "cwd %q is not an existing directory", cwd)
	}
	return nil
}

func workerNames(d Deps) []string {
	var names []string
	for n := range d.Adapters {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
