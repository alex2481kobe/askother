package lifecycle

import (
	"github.com/alex2481kobe/askother/internal/run"
)

// SendRequest is the `send` tool input. ID is a full run id.
type SendRequest struct{ Key, ID, Message string }

// Send continues the native session of a finished run as a new run whose
// previous_id is the source. It inherits worker, cwd, model, effort and
// mode; the message is the prompt. The new record copies the source's
// native_session_id, and the supervisor resumes from its own record, so a
// continuation that fails to start never strands the session: the next
// send to either run resumes it again.
//
// Dedupe hashes run.Request{Prompt: message, CWD: source id} and nothing
// else, so the hash covers exactly the source id plus the message, and a
// retry is recognised before the source is even read. It never equals a
// start hash: a start request always has a worker and a mode.
func Send(d Deps, c Caller, req SendRequest) (*run.Run, bool, error) {
	switch {
	case req.Key == "":
		return nil, false, run.Errorf(run.CodeInvalidInput, "key is required")
	case req.Message == "":
		return nil, false, run.Errorf(run.CodeInvalidInput, "message is required")
	}
	hash, err := run.RequestHash(run.Request{Prompt: req.Message, CWD: req.ID})
	if err != nil {
		return nil, false, err
	}
	return admit(d, c, admission{key: req.Key, hash: hash, prepare: func(runs []*run.Run) (*run.Run, error) {
		return continuation(d, req.ID, req.Message, runs)
	}})
}

// continuation checks the source against fresh records, under the index
// lock, and returns the record to reserve.
//   - BUSY: the source has not ended or its execution is unknown, or
//     another run on the same native session is still active or unresolved.
//     Two concurrent sends on one session therefore yield one run and one
//     BUSY.
//   - INVALID_INPUT: the source never executed or has no native session.
func continuation(d Deps, srcID, message string, runs []*run.Run) (*run.Run, error) {
	var src *run.Run
	for _, r := range runs {
		if r.ID == srcID {
			src = view(d, r)
		}
	}
	switch {
	case src == nil:
		return nil, run.Errorf(run.CodeNotFound, "run %s not found", srcID)
	case !run.IsTerminal(src.State) || src.Execution == run.ExecUnknown:
		return nil, run.Errorf(run.CodeBusy, "run %s is %s (execution %s); wait for it to end", srcID, src.State, src.Execution)
	case src.Execution != run.ExecExited:
		return nil, run.Errorf(run.CodeInvalidInput, "run %s never executed (execution %s); start a new run", srcID, src.Execution)
	case src.NativeSessionID == nil || *src.NativeSessionID == "":
		return nil, run.Errorf(run.CodeInvalidInput, "run %s has no native session to continue", srcID)
	}
	session := *src.NativeSessionID
	for _, r := range runs {
		if r.NativeSessionID == nil || *r.NativeSessionID != session {
			continue
		}
		if v := view(d, r); !run.IsTerminal(v.State) || v.Execution == run.ExecUnknown {
			return nil, run.Errorf(run.CodeBusy, "the session of run %s is in use by run %s; wait for it to end", srcID, r.ID)
		}
	}
	in := src.Request
	if err := checkCWD(in.CWD); err != nil {
		return nil, err
	}
	return &run.Run{PreviousID: &srcID, NativeSessionID: &session,
		Request: run.Request{Worker: in.Worker, Prompt: message, CWD: in.CWD,
			Model: in.Model, Effort: in.Effort, Mode: in.Mode}}, nil
}
