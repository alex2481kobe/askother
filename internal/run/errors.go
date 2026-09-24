package run

import "fmt"

// Code is a stable tool error code; the message carries the details.
// JSON-RPC errors are separate. A Code is itself an error, so it works as a
// sentinel: errors.Is(err, run.CodeNotFound). The worker package reports
// the same strings for the codes it can produce.
type Code string

const (
	CodeInvalidInput Code = "INVALID_INPUT"
	CodeNotFound     Code = "NOT_FOUND"
	CodeKeyConflict  Code = "KEY_CONFLICT"
	CodeBusy         Code = "BUSY"
	CodeStoreIO      Code = "STORE_IO"
	CodeCorruptState Code = "CORRUPT_STATE"
	CodeProtocol     Code = "PROTOCOL"      // malformed or oversized worker output
	CodeWorkerFailed Code = "WORKER_FAILED" // the worker reported failure or exited abnormally
)

func (c Code) Error() string { return string(c) }

// Error is a tool error with a stable code and a message that says why and
// what to change.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// Errorf builds an *Error with a formatted message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

// Is matches a bare Code or another *Error with the same code; messages are
// not compared.
func (e *Error) Is(target error) bool {
	switch t := target.(type) {
	case Code:
		return e.Code == t
	case *Error:
		return t != nil && e.Code == t.Code
	}
	return false
}
