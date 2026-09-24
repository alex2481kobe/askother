package lifecycle

import (
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alex2481kobe/orca/internal/run"
	"github.com/alex2481kobe/orca/internal/worker"
)

// stderrInError bounds the stderr tail quoted in a failed run's error.
const stderrInError = 2 << 10

// finish decides the outcome after Wait, publishes the answer on success,
// and writes the terminal record. done is written only after PublishAnswer
// returned no error at all.
func (s *svRun) finish(out run.Outcome) int {
	fin, _ := s.dec.Finish(out.Exit) // on error the adapters also set Failure
	if fin.SessionID != "" {
		s.session = fin.SessionID
	}

	success, failure := fin.Success, fin.Failure
	var re *run.Error
	switch {
	case errors.As(out.ReadErr, &re):
		success, failure = false, &worker.Failure{Code: string(run.CodeProtocol), Message: re.Message}
	case out.ReadErr != nil:
		success, failure = false, &worker.Failure{Code: string(run.CodeProtocol), Message: "read worker stdout: " + out.ReadErr.Error()}
	case success && s.protoErr != nil:
		success = false
		if !errors.As(s.protoErr, &failure) {
			failure = &worker.Failure{Code: string(run.CodeProtocol), Message: s.protoErr.Error()}
		}
	}

	state := run.StateFailed
	var result run.Result
	if success {
		n, err := svPublish(s.d.Store, s.id, fin.AnswerPath)
		if err == nil {
			state, failure = run.StateDone, nil
			result = run.Result{Available: true, Bytes: n}
		} else {
			// Not visible, or visible but not durable: both are failed, and
			// a stray .txt beside a failed record is never served.
			failure = &worker.Failure{Code: string(run.CodeStoreIO), Message: "publish answer: " + err.Error()}
		}
	} else {
		_ = os.Remove(s.d.Store.AnswerTempPath(s.id)) // never published; nothing to roll back
		if s.signaled {
			state, failure = run.StateStopped, nil
		} else {
			failure = withStderr(failure, out.Stderr)
		}
	}

	exit := run.ExitOf(out.Exit)
	dur := time.Since(s.start).Milliseconds()
	session := s.session
	return s.commitTerminal(func(r *run.Run) {
		r.State, r.Execution = state, run.ExecExited
		r.Exit, r.Error, r.StderrTail = exit, failure, out.Stderr
		if state == run.StateDone {
			r.Result = result
		}
		if out.DrainExpired {
			r.Notes = append(r.Notes, run.NoteDrainBoundExpired)
		}
		r.DurationMS = &dur
		if session != "" {
			r.NativeSessionID = &session
		}
	})
}

// withStderr adds the end of the worker's stderr to a failure. A CLI that
// rejects its arguments or cannot authenticate often explains itself only
// on stderr, and the caller sees the error but not the record's stderr
// tail. Without a failure from the decoder, the stderr tail is the message.
func withStderr(f *worker.Failure, tail run.StderrTail) *worker.Failure {
	text := strings.TrimSpace(tail.Text)
	cut := tail.Truncated
	if len(text) > stderrInError {
		text, cut = text[len(text)-stderrInError:], true
		for len(text) > 0 && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
	}
	switch {
	case f == nil && text == "":
		return &worker.Failure{Code: string(run.CodeWorkerFailed), Message: "the worker did not succeed and gave no reason"}
	case f == nil:
		return &worker.Failure{Code: string(run.CodeWorkerFailed), Message: "stderr: " + text, Truncated: cut}
	case text == "":
		return f
	}
	return &worker.Failure{Code: f.Code, Message: f.Message + "; stderr: " + text, Truncated: f.Truncated || cut}
}

// failNotStarted ends a run whose worker was never started.
func (s *svRun) failNotStarted(f *worker.Failure) int {
	return s.commitTerminal(func(r *run.Run) {
		r.State, r.Execution, r.Error = run.StateFailed, run.ExecNotStarted, f
	})
}

// commitTerminal writes the terminal record, retrying 3 times over about
// 5 s. It returns the process exit code: 1 when every attempt failed, so the
// lock is released with the record non-terminal and readers see
// interrupted/unknown, never success.
func (s *svRun) commitTerminal(apply func(*run.Run)) int {
	for attempt := 0; ; attempt++ {
		ended := s.d.now()
		if written(s.d.Store.Update(s.id, func(r *run.Run) error {
			apply(r)
			r.EndedAt = &ended
			return nil
		})) {
			return 0
		}
		if attempt == 3 {
			return 1
		}
		time.Sleep(svRetryDelay)
	}
}
