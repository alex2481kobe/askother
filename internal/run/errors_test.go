package run

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorIsAndAs(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", Errorf(CodeNotFound, "run %q", "abc"))
	if !errors.Is(err, CodeNotFound) {
		t.Error("errors.Is(err, CodeNotFound) = false")
	}
	if !errors.Is(err, &Error{Code: CodeNotFound, Message: "other"}) {
		t.Error("errors.Is ignores message: got false")
	}
	if errors.Is(err, CodeBusy) || errors.Is(err, &Error{Code: CodeBusy}) {
		t.Error("errors.Is matched a different code")
	}
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeNotFound || e.Message != `run "abc"` {
		t.Errorf("errors.As = %+v", e)
	}
	if got := e.Error(); got != `NOT_FOUND: run "abc"` {
		t.Errorf("Error() = %q", got)
	}
	if got := (&Error{Code: CodeBusy}).Error(); got != "BUSY" {
		t.Errorf("Error() without message = %q", got)
	}
}

func TestCodeWorkerFailed(t *testing.T) {
	err := fmt.Errorf("run: %w", Errorf(CodeWorkerFailed, "codex exited with code 1"))
	if CodeWorkerFailed != "WORKER_FAILED" || !errors.Is(err, CodeWorkerFailed) || errors.Is(err, CodeProtocol) {
		t.Errorf("CodeWorkerFailed = %q, errors.Is = %v", CodeWorkerFailed, errors.Is(err, CodeWorkerFailed))
	}
}
