package lifecycle

import (
	"github.com/alex2481kobe/orca/internal/run"
)

// derive returns what r means given whether its run lock is held. A record
// that has not ended but whose lock is free lost its launcher or
// supervisor: it reads interrupted, never done. Its execution is
// not_started only if the worker was never launched; otherwise unknown,
// because the worker may still be alive. The view is never saved and gets
// no ended_at: nobody saw it end. r is not modified.
func derive(r *run.Run, live bool) *run.Run {
	if run.IsTerminal(r.State) || live {
		return r
	}
	c := *r
	if r.Execution != run.ExecNotStarted {
		c.Execution = run.ExecUnknown
	}
	c.State = run.StateInterrupted
	return &c
}

// view is r derived against its run lock. A lock that cannot be probed is
// not evidence of death, so r is then returned as stored.
func view(d Deps, r *run.Run) *run.Run {
	if run.IsTerminal(r.State) {
		return r
	}
	live, err := d.Store.ProbeLive(r.ID)
	if err != nil {
		return r
	}
	return derive(r, live)
}
