package config

import "encoding/json"

// Client names from initialize.clientInfo.name.
const (
	ClientClaudeCode = "claude-code"
	ClientCodex      = "codex-mcp-client"
)

// ResolveCaller names the agent behind one MCP call, only so runs can be
// grouped by who started them. Source is "claude", "codex" or "fallback".
// The harness id is taken only from the source that matches clientName,
// because an ancestor harness's ids can leak into a nested one's env. Empty
// values count as unset. Under Codex, callMeta is the per-call _meta, so
// resolve on every tools/call.
func ResolveCaller(env map[string]string, clientName string, callMeta json.RawMessage, fallbackID string) (id, source string) {
	switch clientName {
	case ClientClaudeCode:
		if v := env["CLAUDE_CODE_SESSION_ID"]; v != "" {
			return v, "claude"
		}
	case ClientCodex:
		if v := codexThreadID(callMeta); v != "" {
			return v, "codex"
		}
	}
	return fallbackID, "fallback"
}

// codexThreadID reads _meta.threadId, else _meta["x-codex-turn-metadata"].thread_id.
// These keys are undocumented; anything malformed yields "". Maps, not
// structs, so keys match exactly rather than case-insensitively.
func codexThreadID(meta json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(meta, &m) != nil {
		return ""
	}
	if v := jsonString(m["threadId"]); v != "" {
		return v
	}
	var turn map[string]json.RawMessage
	if json.Unmarshal(m["x-codex-turn-metadata"], &turn) != nil {
		return ""
	}
	return jsonString(turn["thread_id"])
}

// jsonString returns raw as a string if it is a JSON string, else "".
func jsonString(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
