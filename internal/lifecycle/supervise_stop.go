package lifecycle

import (
	"errors"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
)

// errSvSkip aborts an Update without writing.
var errSvSkip = errors.New("skip")

// watch polls the record every svPoll until Wait returns. It acts once on a
// stop request or an elapsed timeout_ms, and otherwise records a newly
// reported native session, so a continuation can be sent as soon as the run
// ends even if the terminal write is delayed. It keeps running after the
// worker exits, through the drain: a stop that lands then still reaches
// Stop, which sends nothing, and the outcome is decided by the decoder.
func (s *svRun) watch(waitDone <-chan struct{}) {
	var timeout time.Duration
	if r, err := s.d.Store.Read(s.id); err == nil && r.Request.TimeoutMS != nil {
		timeout = time.Duration(*r.Request.TimeoutMS) * time.Millisecond
	}
	t := time.NewTicker(svPoll)
	defer t.Stop()
	for {
		select {
		case <-waitDone:
			return
		case <-t.C:
		}
		if !s.stopActed {
			timedOut := timeout > 0 && time.Since(s.start) >= timeout
			r, err := s.d.Store.Read(s.id)
			requested := err == nil && r.StopRequestedAt != nil
			if requested || timedOut {
				s.stopActed = true
				s.markStopping(!requested)
				s.signaled = run.Stop(s.w, svGrace)
				continue
			}
		}
		s.flushSession()
	}
}

// markStopping records that the supervisor is acting on a stop. A timeout
// is the supervisor's own stop request, so it also sets stop_requested_at
// and stop_reason "timeout".
func (s *svRun) markStopping(timeout bool) {
	now := s.d.now()
	_, _ = s.d.Store.Update(s.id, func(r *run.Run) error {
		if run.IsTerminal(r.State) {
			return errSvSkip
		}
		r.State = run.StateStopping
		if timeout && r.StopRequestedAt == nil {
			reason := run.StopTimeout
			r.StopRequestedAt, r.StopReason = &now, &reason
		}
		return nil
	})
}

// flushSession writes a newly reported native session to the record. The
// terminal record carries it regardless.
func (s *svRun) flushSession() {
	s.mu.Lock()
	session, dirty := s.session, s.dirty
	s.dirty = false
	s.mu.Unlock()
	if !dirty {
		return
	}
	_, _ = s.d.Store.Update(s.id, func(r *run.Run) error {
		if run.IsTerminal(r.State) {
			return errSvSkip
		}
		r.NativeSessionID = &session
		return nil
	})
}
