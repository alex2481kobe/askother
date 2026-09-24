// Package mcp is Orca's MCP server over stdio: the tool inputs and outputs,
// strict input decoding, the tools/list payload and the JSON-RPC loop.
package mcp

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/alex2481kobe/orca/internal/run"
	"github.com/alex2481kobe/orca/internal/worker"
)

// Tool inputs. Optional strings are empty when absent.

type RunInput struct {
	Key       string `json:"key"`
	Worker    string `json:"worker"`
	Prompt    string `json:"prompt"`
	CWD       string `json:"cwd"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Role      string `json:"role,omitempty"`
	Task      string `json:"task,omitempty"`
	TimeoutMS *int64 `json:"timeout_ms,omitempty"`
}

type SendInput struct {
	Key     string `json:"key"`
	ID      string `json:"id"`
	Message string `json:"message"`
}

type WaitInput struct {
	IDs []string `json:"ids"`
	Any bool     `json:"any,omitempty"`
}

type ResultInput struct {
	ID     string `json:"id"`
	Offset *int64 `json:"offset,omitempty"`
	Limit  *int64 `json:"limit,omitempty"`
}

type StatusInput struct {
	ID string `json:"id,omitempty"`
}

type StopInput struct {
	ID string `json:"id"`
}

// Tool outputs. RunOutput is also the send output.

type RunOutput struct {
	ID     string    `json:"id"`
	State  run.State `json:"state"`
	Reused bool      `json:"reused"`
	Watch  string    `json:"watch"` // a shell command: orca wait <id>
}

type WaitOutput struct {
	Satisfied bool         `json:"satisfied"`
	TimedOut  bool         `json:"timed_out"`
	Runs      []RunSummary `json:"runs"`
}

type ResultOutput struct {
	ID         string          `json:"id"`
	State      run.State       `json:"state"`
	Available  bool            `json:"available"`
	Text       string          `json:"text"`
	Offset     int64           `json:"offset"`
	NextOffset int64           `json:"next_offset"`
	TotalBytes int64           `json:"total_bytes"`
	EOF        bool            `json:"eof"`
	Error      *worker.Failure `json:"error,omitempty"`
}

type StatusOutput struct {
	Runs []RunSummary `json:"runs"`
}

type StopOutput struct {
	ID            string    `json:"id"`
	State         run.State `json:"state"`
	StopRequested bool      `json:"stop_requested"`
}

// RunSummary is the per-run view in wait and status.
type RunSummary struct {
	ID              string          `json:"id"`
	Worker          string          `json:"worker"`
	Role            string          `json:"role,omitempty"`
	Task            string          `json:"task,omitempty"`
	State           run.State       `json:"state"`
	Execution       run.Execution   `json:"execution"`
	PreviousID      *string         `json:"previous_id"`
	CreatedAt       time.Time       `json:"created_at"`
	EndedAt         *time.Time      `json:"ended_at,omitempty"`
	DurationMS      *int64          `json:"duration_ms,omitempty"`
	Exit            *run.Exit       `json:"exit"`
	ResultAvailable bool            `json:"result_available"`
	Error           *worker.Failure `json:"error"`
	Unread          bool            `json:"unread"`
}

// Summarize projects a run view into a RunSummary. A run is unread when it
// has ended (or reads interrupted) and no result has been served yet.
func Summarize(r *run.Run) RunSummary {
	return RunSummary{
		ID: r.ID, Worker: r.Request.Worker, Role: r.Request.Role, Task: r.Request.Task,
		State: r.State, Execution: r.Execution, PreviousID: r.PreviousID,
		CreatedAt: r.CreatedAt, EndedAt: r.EndedAt, DurationMS: r.DurationMS,
		Exit: r.Exit, ResultAvailable: r.Result.Available, Error: r.Error,
		Unread: run.IsTerminal(r.State) && r.FirstReadAt == nil,
	}
}

// DecodeStrict decodes tool arguments into v, a pointer to one of the input
// structs. Beyond json.Decoder.DisallowUnknownFields it rejects keys that
// match a field only case-insensitively, duplicate keys, and trailing data.
// Absent or null arguments decode as {}. Errors are INVALID_INPUT.
func DecodeStrict(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return run.Errorf(run.CodeInvalidInput, "arguments must be a JSON object: %v", err)
	}
	known := jsonNames(reflect.TypeOf(v).Elem())
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.Token() // '{', already validated above
	seen := map[string]bool{}
	for dec.More() {
		tok, _ := dec.Token()
		key, _ := tok.(string)
		if !known[key] {
			return run.Errorf(run.CodeInvalidInput, "unknown field %q; valid fields: %s", key, strings.Join(sortedKeys(known), ", "))
		}
		if seen[key] {
			return run.Errorf(run.CodeInvalidInput, "duplicate field %q", key)
		}
		seen[key] = true
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return run.Errorf(run.CodeInvalidInput, "field %q: %v", key, err)
		}
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(v); err != nil {
		return run.Errorf(run.CodeInvalidInput, "%v", err)
	}
	return nil
}

func jsonNames(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
