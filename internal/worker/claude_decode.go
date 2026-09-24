package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

const claudeFailureMax = 500 // bytes kept of an is_error result text

// claudeDecoder reads `--output-format stream-json --verbose` events.
type claudeDecoder struct {
	tempAnswer string
	session    string // the resumed session, then the session Claude reported
	observed   bool   // system/init seen
	last       *claudeResult
	protoErr   *Failure // first protocol failure; sticky
}

type claudeResult struct {
	isError bool
	text    string
	line    []byte // the whole event, read again only to explain an error
}

func (d *claudeDecoder) Consume(line []byte) (Update, error) {
	u, f := d.consume(line)
	if f == nil {
		return u, nil
	}
	if d.protoErr == nil {
		d.protoErr = f
	}
	return u, f
}

// consume decodes the type first, so an event Orca does not read can never
// fail on its other fields.
func (d *claudeDecoder) consume(line []byte) (Update, *Failure) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Update{}, nil
	}
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return Update{}, failf(codeProtocol, "claude: malformed event: %v", err)
	}
	switch {
	case ev.Type == "system" && ev.Subtype == "init":
		var e struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(line, &e) != nil || e.SessionID == "" {
			return Update{}, failf(codeProtocol, "claude: system/init has no session_id")
		}
		// A resume must continue the same session, and a run has one session.
		if d.session != "" && e.SessionID != d.session {
			return Update{}, failf(codeProtocol, "claude: session_id %q, but this run is session %q", e.SessionID, d.session)
		}
		d.session, d.observed = e.SessionID, true
		return Update{SessionID: e.SessionID}, nil
	case ev.Type == "result":
		var r struct {
			IsError *bool   `json:"is_error"`
			Result  *string `json:"result"`
		}
		if json.Unmarshal(line, &r) != nil || r.IsError == nil || r.Result == nil {
			return Update{}, failf(codeProtocol, "claude: result event lacks a boolean is_error or a string result")
		}
		// The last result before exit decides; an earlier one can be superseded.
		d.last = &claudeResult{isError: *r.IsError, text: *r.Result, line: line}
	}
	return Update{}, nil
}

// Finish: success is the last result with is_error false, exit 0, and an
// observed session.
func (d *claudeDecoder) Finish(exit Exit) (Final, error) {
	f := Final{}
	if d.observed {
		f.SessionID = d.session
	}
	r := d.last
	switch {
	case d.protoErr != nil:
		p := *d.protoErr
		f.Failure = &p
	case r == nil:
		f.Failure = failf(codeProtocol, "claude: no result event before exit (%s)", exitText(exit))
	case r.isError:
		// subtype can be "success" here too. The text is never published.
		text := cut(r.text, claudeFailureMax)
		f.Failure = failf(codeWorkerFailed, "claude reported an error (%s, %s): %s", claudeReason(r.line), exitText(exit), text)
		f.Failure.Truncated = len(text) < len(r.text)
	case exit.Code == nil || *exit.Code != 0:
		f.Failure = failf(codeWorkerFailed, "claude: result was not an error but the process %s", exitText(exit))
	case !d.observed:
		f.Failure = failf(codeProtocol, "claude: exited 0 without a system/init session_id")
	default:
		if err := writeAnswer(d.tempAnswer, r.text); err != nil {
			f.Failure = failf(codeStoreIO, "claude: write answer: %v", err)
			return f, err
		}
		f.Success, f.AnswerPath = true, d.tempAnswer
	}
	return f, nil
}

// claudeReason names why an is_error result ended, as far as it says.
func claudeReason(line []byte) string {
	var e struct {
		TerminalReason string `json:"terminal_reason"`
		APIErrorStatus *int   `json:"api_error_status"`
	}
	_ = json.Unmarshal(line, &e) // best effort: this only explains a failure
	s := "terminal_reason " + e.TerminalReason
	if e.APIErrorStatus != nil {
		s += fmt.Sprintf(", api_error_status %d", *e.APIErrorStatus)
	}
	return s
}

// writeAnswer writes the result text exactly, with no added newline.
func writeAnswer(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
