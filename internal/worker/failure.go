package worker

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Codes an adapter can report. They are the same strings as the run
// package's codes, which cannot be imported here because run imports worker.
const (
	codeInvalidInput = "INVALID_INPUT" // Build refused the Invocation
	codeProtocol     = "PROTOCOL"      // malformed or missing required output
	codeWorkerFailed = "WORKER_FAILED" // the worker reported failure or exited abnormally
	codeStoreIO      = "STORE_IO"      // the answer file could not be written
)

// Failure is a run failure: code, message, and whether the message was cut.
// It is also the error type every Build and Consume error carries, so
// errors.As(err, &f) with f of type *Failure yields the code.
type Failure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Truncated bool   `json:"truncated"`
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message }

func failf(code, format string, a ...any) *Failure {
	return &Failure{Code: code, Message: fmt.Sprintf(format, a...)}
}

// failCut is a failure whose message is cut to at most n bytes.
func failCut(code, msg string, n int) *Failure {
	m := cut(msg, n)
	return &Failure{Code: code, Message: m, Truncated: len(m) < len(msg)}
}

// cut returns at most n bytes of s, cut on a UTF-8 boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// checkInvocation holds the Build checks both adapters share.
func checkInvocation(name string, in Invocation, modes []string) error {
	switch {
	case in.Binary == "":
		return failf(codeInvalidInput, "%s: binary path is empty", name)
	case in.TempAnswer == "":
		return failf(codeInvalidInput, "%s: temp answer path is empty", name)
	case !slices.Contains(modes, in.Mode):
		return failf(codeInvalidInput, "%s: unknown mode %q (modes: %s)", name, in.Mode, strings.Join(modes, ", "))
	case strings.HasPrefix(in.ResumeID, "-"):
		return failf(codeInvalidInput, "%s: session id %q would parse as a flag", name, in.ResumeID)
	}
	return nil
}

func exitText(e Exit) string {
	switch {
	case e.Code != nil:
		return fmt.Sprintf("exited %d", *e.Code)
	case e.Signal != "":
		return "killed by signal " + e.Signal
	}
	return "exit status unknown"
}
