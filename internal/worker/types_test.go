package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// Failure is stored as-is in the run record and is the error type adapters
// return, so both its JSON and its use through errors.As are fixed.
func TestFailureShape(t *testing.T) {
	b, err := json.Marshal(Failure{Code: "PROTOCOL", Message: "m"})
	if err != nil || string(b) != `{"code":"PROTOCOL","message":"m","truncated":false}` {
		t.Errorf("Marshal = %s, %v", b, err)
	}
	_, err = Codex{}.Build(Invocation{})
	var f *Failure
	if !errors.As(fmt.Errorf("wrapped: %w", err), &f) || f.Code != "INVALID_INPUT" || err.Error() != "INVALID_INPUT: "+f.Message {
		t.Errorf("Build error %v", err)
	}
}
