package mcp

import (
	"fmt"
	"strings"

	"github.com/alex2481kobe/orca/internal/worker"
)

// Tool is one entry of the tools/list result.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations *Annotations   `json:"annotations,omitempty"`
}

// Annotations are MCP tool hints. Only read tools carry them.
type Annotations struct {
	ReadOnlyHint bool `json:"readOnlyHint"`
}

// resultPageMax is the result page cap in bytes (default page 16 KiB).
const resultPageMax = 64 << 10

// Tools returns the tools/list payload. Every default and pinned setting in
// it is rendered from facts, never written by hand, so it cannot drift from
// what the adapters do.
func Tools(facts []worker.Facts) []Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	id := str("Full run ID.")
	key := str("Caller-generated idempotency key. The same key with the same request returns the same run; different content returns KEY_CONFLICT.")

	workerProp := str("Worker CLI to run.")
	if names := workerNames(facts); len(names) > 0 {
		workerProp["enum"] = names
	}
	readOnly := &Annotations{ReadOnlyHint: true}

	return []Tool{
		{
			Name:        "run",
			Description: runDescription(facts),
			InputSchema: object([]string{"key", "worker", "prompt", "cwd"}, map[string]any{
				"key":        key,
				"worker":     workerProp,
				"prompt":     str("The task for the worker."),
				"cwd":        str("Absolute, existing directory the worker runs in."),
				"model":      str("Model passed to the worker; omit for its default."),
				"effort":     str("Reasoning effort passed to the worker; omit for its default."),
				"mode":       str("Permission or sandbox mode; omit for the worker's default (see description)."),
				"role":       str("Free-form label shown in status."),
				"task":       str("Free-form label shown in status."),
				"timeout_ms": map[string]any{"type": "integer", "minimum": 1, "description": "Request a stop after this many milliseconds. Absent means no timeout."},
			}),
		},
		{
			Name:        "send",
			Description: "Continue a finished run's native session as a new linked run. Inherits worker, cwd, model, effort and mode. Refused (BUSY) while the source, or another run on the same session, is still running or unresolved. Replies like run.",
			InputSchema: object([]string{"key", "id", "message"}, map[string]any{
				"key":     key,
				"id":      id,
				"message": str("The follow-up for the worker."),
			}),
		},
		{
			Name:        "wait",
			Description: "Wait up to 50 s for the runs to end (with any, for one of them). A timeout here does not stop runs. Does not mark runs read.",
			InputSchema: object([]string{"ids"}, map[string]any{
				"ids": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}, "description": "Full run IDs."},
				"any": map[string]any{"type": "boolean", "description": "Return when any one run ends instead of all."},
			}),
			Annotations: readOnly,
		},
		{
			Name:        "result",
			Description: "Read a run's final answer, paged by UTF-8 byte offset (16 KiB default page, 64 KiB max). A failed run's error carries the end of the worker's stderr. Serving a finished run marks it read: read runs are deleted after 24 hours, unread finished runs after 7 days.",
			InputSchema: object([]string{"id"}, map[string]any{
				"id":     id,
				"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Byte offset to start at; use next_offset from the previous page."},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": resultPageMax, "description": "Maximum bytes to return."},
			}),
			Annotations: readOnly,
		},
		{
			Name:        "status",
			Description: "Show one run, or every run newest first, each with an unread flag. Does not mark runs read.",
			InputSchema: object(nil, map[string]any{
				"id": str("Full run ID; omit to list every run."),
			}),
			Annotations: readOnly,
		},
		{
			Name:        "stop",
			Description: "Request a stop: the supervisor sends TERM, waits 5 s, then KILL. Replies once requested, not once stopped.",
			InputSchema: object([]string{"id"}, map[string]any{"id": id}),
		},
	}
}

func object(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func workerNames(facts []worker.Facts) []string {
	names := make([]string, 0, len(facts))
	for _, f := range facts {
		names = append(names, f.Name)
	}
	return names
}

func runDescription(facts []worker.Facts) string {
	var b strings.Builder
	b.WriteString("Start a worker run and reply once its supervisor owns it. The reply's watch field is a shell command that blocks until the run ends (or call the wait tool); then read it with result.")
	if len(facts) > 0 {
		b.WriteString("\nWorkers:")
	}
	for _, f := range facts {
		fmt.Fprintf(&b, "\n- %s: default mode %s; modes %s", f.Name, f.DefaultMode, strings.Join(f.Modes, ", "))
		if len(f.Pinned) > 0 {
			fmt.Fprintf(&b, "; always passes %s", strings.Join(f.Pinned, ", "))
		}
	}
	return b.String()
}
