// Package lifecycle turns the run store, process mechanics and worker
// adapters into AskOther's operations: start, send, stop request, supervise,
// observe, wait, result and the retention sweep.
package lifecycle

import (
	"time"

	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/worker"
)

// Deps is everything the operations need. cmd/askother builds one per process.
type Deps struct {
	Store    *run.Store
	Adapters map[string]worker.Adapter // keyed by Facts().Name

	// ConfigPath is the owner config file. Only admission (Start and Send)
	// reads it, so a broken config never stops observing or supervising
	// existing runs. Empty means no config.
	ConfigPath string
	// Self is the absolute path of the askother binary; the launcher starts
	// `Self supervise <id>`.
	Self string
	// StateHome is the state directory, used to shorten messages.
	StateHome string
	// Env is the environment of the process calling AskOther, as a map; worker
	// environments are derived from it by config.WorkerEnv, never copied.
	Env map[string]string

	Now func() time.Time
}

// Caller identifies who is asking, as resolved by config.ResolveCaller. It
// only groups runs under the agent that started them.
type Caller struct {
	ID     string
	Source run.CallerSource
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}
