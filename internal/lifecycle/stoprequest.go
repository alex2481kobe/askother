package lifecycle

import (
	"errors"

	"github.com/alex2481kobe/orca/internal/run"
)

var errAlreadyEnded = errors.New("run already ended")

// RequestStop asks the supervisor of a non-terminal run to stop it by
// setting stop_requested_at and stop_reason "caller". The supervisor acts on
// it, and a worker that finishes its answer first still ends done, so this
// replies "requested", never "stopped". A terminal run, or one whose lock
// is free (it reads interrupted), is returned unchanged with
// requested=false. A repeated request keeps the first timestamp and reason.
func RequestStop(d Deps, id string) (*run.Run, bool, error) {
	var ended *run.Run
	r, err := d.Store.Update(id, func(r *run.Run) error {
		if v := view(d, r); run.IsTerminal(v.State) {
			ended = v
			return errAlreadyEnded
		}
		if r.StopRequestedAt == nil {
			t, reason := d.now(), run.StopCaller
			r.StopRequestedAt, r.StopReason = &t, &reason
		}
		return nil
	})
	switch {
	case errors.Is(err, errAlreadyEnded):
		return ended, false, nil
	case err != nil:
		return nil, false, err
	}
	return r, true, nil
}
