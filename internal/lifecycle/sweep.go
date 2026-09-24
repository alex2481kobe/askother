package lifecycle

import (
	"errors"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
)

// Retention: a run whose result was read is deleted KeepRead after
// first_read_at. A run that ended, or reads interrupted, and was never read
// is deleted KeepUnread after it ended, or after it was created when it
// never recorded an end.
const (
	KeepRead   = 24 * time.Hour
	KeepUnread = 7 * 24 * time.Hour
)

// Sweep applies retention under the index lock. A run whose lock is held is
// never deleted, and a run with a timestamp in the future (the clock went
// back) is skipped until the clock catches up. Store.Remove deletes the
// record first, then the answer, then the lock. Sweep is opportunistic:
// callers ignore its error, which joins every failure.
func Sweep(d Deps) error {
	return d.Store.WithIndexLock(func() error {
		now := d.now()
		runs, _, err := d.Store.List()
		if err != nil {
			return err
		}
		var errs []error
		for _, r := range runs {
			live, err := d.Store.ProbeLive(r.ID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if live || !expired(derive(r, false), now) {
				continue
			}
			// Remove probes the lock again and refuses a live run with BUSY.
			if err := d.Store.Remove(r.ID); err != nil && !errors.Is(err, run.CodeBusy) {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	})
}

// expired reports whether v, a run whose lock is free and so a terminal
// view, is past retention at now.
func expired(v *run.Run, now time.Time) bool {
	for _, t := range []*time.Time{&v.CreatedAt, v.EndedAt, v.FirstReadAt} {
		if t != nil && t.After(now) {
			return false
		}
	}
	switch {
	case v.FirstReadAt != nil:
		return now.Sub(*v.FirstReadAt) > KeepRead
	case v.EndedAt != nil:
		return now.Sub(*v.EndedAt) > KeepUnread
	}
	return now.Sub(v.CreatedAt) > KeepUnread
}
