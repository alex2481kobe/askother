package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// claude emits Claude Code `--output-format stream-json --verbose` events.
// Events are maps, so keys come out sorted; like the real CLI, the result
// event therefore does not start with "type".
type claude struct {
	e       *emitter
	in      invocation
	rec     *record
	session string
	turns   int
	denials []any
}

type obj = map[string]any

func (c *claude) model() string {
	if c.in.Model == "" {
		return "claude-fake-model"
	}
	return c.in.Model
}

// start reports the session in system/init, the only place Orca reads it from.
func (c *claude) start() {
	c.rec.SessionID = c.session
	c.rec.save()
	cwd, _ := os.Getwd()
	c.e.event(obj{"type": "system", "subtype": "init", "session_id": c.session, "cwd": cwd, "model": c.model()})
}

func (c *claude) activity() {
	c.e.event(obj{"type": "rate_limit_event", "rate_limit_info": obj{"status": "allowed"}, "session_id": c.session})
}

func (c *claude) assistant(model string, block obj) {
	c.e.event(obj{"type": "assistant", "session_id": c.session,
		"message": obj{"model": model, "role": "assistant", "content": []any{block}}})
}

func (c *claude) progress(text string) {
	c.turns++
	c.assistant(c.model(), obj{"type": "text", "text": text})
}

func (c *claude) result(r obj) {
	r["type"], r["session_id"], r["num_turns"], r["permission_denials"] = "result", c.session, c.turns+1, c.denials
	c.e.last(r)
}

func (c *claude) finish(answer string) int {
	c.rec.setAnswer(answer)
	c.assistant(c.model(), obj{"type": "text", "text": answer})
	c.result(obj{"subtype": "success", "is_error": false, "terminal_reason": "completed", "result": answer})
	return 0
}

func (c *claude) partial() {
	c.e.write([]byte(`{"type":"assistant","message":{"model":"` + c.model() + `","content":[{"type":"text","text":"cut sho`))
}

func (c *claude) special(name string) (int, bool) {
	switch name {
	case "is_error_result":
		// subtype stays "success" even though is_error is true, and the
		// process exits 1.
		c.start()
		text := fmt.Sprintf("There's an issue with the selected model (%s). It may not exist or you may not have access to it.", c.model())
		c.assistant("<synthetic>", obj{"type": "text", "text": text})
		fmt.Fprintf(os.Stderr, "[claude-code:unrecognized_model] {\"model\":%q}\n", c.model())
		c.result(obj{"subtype": "success", "is_error": true, "terminal_reason": "api_error",
			"api_error_status": 404, "result": text})
		return 1, true
	case "denied_write":
		c.start()
		cwd, _ := os.Getwd()
		c.deny("Write", obj{"file_path": filepath.Join(cwd, "fake_denied.txt"), "content": "x"})
		c.deny("Bash", obj{"command": "touch fake_denied.txt"})
		return c.finish("WRITE=denied: both Write and Bash were refused."), true
	}
	return 0, false
}

// deny plays one refused tool call: tool_use, system/permission_denied and
// the error tool_result. The run itself still succeeds.
func (c *claude) deny(tool string, input obj) {
	id := fmt.Sprintf("toolu_fake%02d", len(c.denials)+1)
	msg := fmt.Sprintf("Permission to use %s has been denied.", tool)
	c.turns++
	c.assistant(c.model(), obj{"type": "tool_use", "id": id, "name": tool, "input": input})
	c.e.event(obj{"type": "system", "subtype": "permission_denied", "tool_name": tool, "tool_use_id": id,
		"message": msg, "session_id": c.session})
	c.e.event(obj{"type": "user", "session_id": c.session, "message": obj{"role": "user",
		"content": []any{obj{"type": "tool_result", "content": msg, "is_error": true, "tool_use_id": id}}}})
	c.denials = append(c.denials, obj{"tool_name": tool, "tool_use_id": id, "tool_input": input})
}
