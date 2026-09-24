package worker

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

const codexMessageMax = 8 << 10 // bytes kept of a failure message, like the stderr tail

type codexDecoder struct {
	tempAnswer string
	session    string // the resumed thread, then the thread Codex reported
	observed   bool   // thread.started seen

	completed bool   // turn.completed seen
	failed    string // message of turn.failed
	lastError string // message of the last top-level `error` event
	protocol  string // first protocol failure
}

// Consume decodes one event. It decodes the type first, so an event Orca
// does not read can never fail on its other fields.
func (d *codexDecoder) Consume(line []byte) (Update, error) {
	if len(bytes.TrimSpace(line)) == 0 {
		return Update{}, nil
	}
	var ev struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return Update{}, d.protocolf("malformed event: %v", err)
	}
	switch ev.Type {
	case "":
		return Update{}, d.protocolf("event without a type")
	case "thread.started":
		var e struct {
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal(line, &e); err != nil || e.ThreadID == "" {
			return Update{}, d.protocolf("thread.started without a thread_id")
		}
		// A resume must continue the same thread, and a run has one thread.
		if d.session != "" && e.ThreadID != d.session {
			return Update{}, d.protocolf("thread %s started, but this run is thread %s", e.ThreadID, d.session)
		}
		d.session, d.observed = e.ThreadID, true
		return Update{SessionID: e.ThreadID}, nil
	case "turn.completed":
		d.completed = true
	case "turn.failed":
		d.failed = cmp.Or(codexMessage(line), d.lastError, "codex reported turn.failed without a message")
	case "error":
		// An `error` alone does not fail the run: it can be a transient retry
		// notice ("Reconnecting... n/5"). It becomes part of the failure
		// message only if the run fails.
		d.lastError = cmp.Or(codexMessage(line), "codex reported error without a message")
	}
	// Items, including an item of type "error" (a warning), are progress.
	// The answer is only ever the -o file.
	return Update{}, nil
}

// codexMessage is the message of an error or turn.failed event, or "". The
// message is often a JSON-encoded API error; it is kept verbatim.
func codexMessage(line []byte) string {
	var e struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(line, &e) // best effort: the message only explains a failure
	return cmp.Or(e.Message, e.Error.Message)
}

func (d *codexDecoder) protocolf(format string, a ...any) error {
	f := failf(codeProtocol, "codex: "+format, a...)
	if d.protocol == "" {
		d.protocol = f.Message
	}
	return f
}

// Finish: success is exit 0, turn.completed, an observed thread, and the -o
// file present. Codex exits 0 when the -o write fails, so the file is
// checked, never assumed. The error is non-nil only when the file cannot be
// checked.
func (d *codexDecoder) Finish(x Exit) (Final, error) {
	f := Final{}
	if d.observed {
		f.SessionID = d.session
	}
	fail := func(code, msg string) (Final, error) {
		f.Failure = failCut(code, msg, codexMessageMax)
		return f, nil
	}
	withError := func(msg string) string {
		if d.lastError == "" {
			return msg
		}
		return msg + "; last error: " + d.lastError
	}
	switch {
	case d.failed != "":
		return fail(codeWorkerFailed, d.failed)
	case x.Code == nil || *x.Code != 0:
		return fail(codeWorkerFailed, withError("codex: process "+exitText(x)))
	case d.protocol != "":
		return fail(codeProtocol, d.protocol)
	case !d.completed:
		return fail(codeProtocol, withError("codex: exited 0 without turn.completed"))
	case !d.observed:
		return fail(codeProtocol, "codex: exited 0 without reporting a thread_id")
	}
	fi, err := os.Lstat(d.tempAnswer)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fail(codeProtocol, "codex: exited 0 but wrote no -o answer file; see the stderr tail")
	case err != nil:
		f.Failure = failf(codeProtocol, "codex: cannot check the -o answer file: %v", err)
		return f, err
	case !fi.Mode().IsRegular():
		return fail(codeProtocol, "codex: the -o answer path is not a regular file")
	}
	f.Success, f.AnswerPath = true, d.tempAnswer
	return f, nil
}
