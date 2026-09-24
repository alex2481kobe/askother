package lifecycle

import (
	"sort"

	"github.com/alex2481kobe/askother/internal/run"
)

// Observe returns the view of run id: the stored record, or interrupted
// when the record has not ended but nothing holds its run lock. Observation
// never writes, so it works on a read-only or full disk.
func Observe(d Deps, id string) (*run.Run, error) {
	r, err := d.Store.Read(id)
	if err != nil {
		return nil, err
	}
	if run.IsTerminal(r.State) {
		return r, nil
	}
	live, err := d.Store.ProbeLive(id)
	if err != nil {
		return nil, err
	}
	return derive(r, live), nil
}

// Status returns one run (id set), or every run, newest first. Listed runs
// are viewed as by Observe, except that a lock that cannot be probed leaves
// the run as stored. Unreadable records are left out of the listing;
// admission refuses to run while any exist.
func Status(d Deps, id string) ([]*run.Run, error) {
	if id != "" {
		r, err := Observe(d, id)
		if err != nil {
			return nil, err
		}
		return []*run.Run{r}, nil
	}
	stored, _, err := d.Store.List()
	if err != nil {
		return nil, err
	}
	out := make([]*run.Run, len(stored))
	for i, r := range stored {
		out[i] = view(d, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	})
	return out, nil
}
