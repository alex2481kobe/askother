package lifecycle

import (
	"context"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
)

// waitPoll is how often Wait re-observes its runs. A variable so tests can
// shorten it.
var waitPoll = 100 * time.Millisecond

// WaitResult is the outcome of Wait. Runs are the latest views, in the order
// of the ids asked for.
type WaitResult struct {
	Satisfied, TimedOut bool
	Runs                []*run.Run
}

// Wait observes ids until all of them (or, with any, at least one) are
// terminal; interrupted counts as terminal. When budget elapses or ctx is
// done first it returns TimedOut with the latest views; ctx.Err() tells a
// cancelled waiter from an expired one. A budget of zero or less checks
// once. Wait never writes, so it never marks a run read.
func Wait(ctx context.Context, d Deps, ids []string, any bool, budget time.Duration) (WaitResult, error) {
	if len(ids) == 0 {
		return WaitResult{}, run.Errorf(run.CodeInvalidInput, "wait needs at least one run id")
	}
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	tick := time.NewTicker(waitPoll)
	defer tick.Stop()
	for {
		res := WaitResult{Runs: make([]*run.Run, len(ids))}
		ended := 0
		for i, id := range ids {
			r, err := Observe(d, id)
			if err != nil {
				return WaitResult{}, err
			}
			res.Runs[i] = r
			if run.IsTerminal(r.State) {
				ended++
			}
		}
		if ended == len(ids) || any && ended > 0 {
			res.Satisfied = true
			return res, nil
		}
		select {
		case <-tick.C:
			continue
		case <-deadline.C:
		case <-ctx.Done():
		}
		res.TimedOut = true
		return res, nil
	}
}
