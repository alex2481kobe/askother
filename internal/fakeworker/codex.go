package main

import (
	"fmt"
	"io/fs"
	"os"
)

// cxEvent is a Codex `exec --json` event. Fields keep the CLI's order, with
// "type" first.
type cxEvent struct {
	Type     string   `json:"type"`
	ThreadID string   `json:"thread_id,omitempty"`
	Item     any      `json:"item,omitempty"`
	Message  string   `json:"message,omitempty"`
	Error    *cxError `json:"error,omitempty"`
}

type cxError struct {
	Message string `json:"message"`
}

type codex struct {
	e      *emitter
	in     invocation
	rec    *record
	thread string
	items  int
	skipO  bool // missing_o_file: behave like a failed -o write
}

func (c *codex) item(typ string, fields map[string]any) map[string]any {
	fields["id"], fields["type"] = fmt.Sprintf("item_%d", c.items), typ
	c.items++
	return fields
}

func (c *codex) threadStarted() {
	c.rec.SessionID = c.thread
	c.rec.save()
	c.e.event(cxEvent{Type: "thread.started", ThreadID: c.thread})
}

func (c *codex) start() {
	c.threadStarted()
	c.e.event(cxEvent{Type: "turn.started"})
}

func (c *codex) activity() {
	cmd := c.item("command_execution", map[string]any{"command": "/bin/sh -lc ls", "status": "in_progress"})
	c.e.event(cxEvent{Type: "item.started", Item: cmd})
	cmd["status"], cmd["exit_code"], cmd["aggregated_output"] = "completed", 0, "notes.txt\n"
	c.e.event(cxEvent{Type: "item.completed", Item: cmd})
}

// progress is an agent_message item. Codex progress notes look exactly like
// the final answer, which is why only -o holds the answer.
func (c *codex) progress(text string) {
	c.e.event(cxEvent{Type: "item.completed", Item: c.item("agent_message", map[string]any{"text": text})})
}

// finish emits the final agent_message and turn.completed, then writes -o
// after turn.completed, as the real CLI does.
func (c *codex) finish(answer string) int {
	c.rec.setAnswer(answer)
	c.progress(answer)
	c.e.last(cxEvent{Type: "turn.completed"})
	if c.in.OutputFile == "" {
		return 0
	}
	err := error(fs.ErrNotExist)
	if !c.skipO {
		err = os.WriteFile(c.in.OutputFile, []byte(answer), 0o644)
	}
	if err != nil { // Codex still exits 0 when the -o write fails
		fmt.Fprintf(os.Stderr, "Failed to write last message file %q: %v\n", c.in.OutputFile, err)
	}
	return 0
}

// partial writes the start of an event and no newline, as a crash would leave it.
func (c *codex) partial() {
	c.e.write([]byte(`{"type":"item.completed","item":{"id":"item_x","type":"agent_message","text":"cut sho`))
}

func (c *codex) special(name string) (int, bool) {
	switch name {
	case "missing_o_file":
		c.start()
		c.skipO = true
		return c.finish("OK"), true
	case "turn_failed":
		c.start()
		msg := `{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The 'fake-model' model is not supported."}}`
		c.e.event(cxEvent{Type: "error", Message: msg})
		c.e.last(cxEvent{Type: "turn.failed", Error: &cxError{Message: msg}})
		return 1, true
	case "warning_item":
		// A top-level error event and an error item are warnings: the run
		// still succeeds.
		c.threadStarted()
		c.e.event(cxEvent{Type: "item.completed", Item: c.item("error", map[string]any{
			"message": "Model metadata for `fake-model` not found. Defaulting to fallback metadata."})})
		c.e.event(cxEvent{Type: "turn.started"})
		c.e.event(cxEvent{Type: "error", Message: "Reconnecting... 1/5"})
		return c.finish("OK despite a warning"), true
	}
	return 0, false
}
