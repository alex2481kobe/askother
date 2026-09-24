package config

import (
	"encoding/json"
	"testing"
)

// Shaped as a real Codex tools/call _meta, with synthetic ids.
const codexMeta = `{"threadId":"thread-a","sessionId":"thread-a","windowId":"thread-a:0",
 "x-codex-turn-metadata":{"thread_id":"thread-b","session_id":"thread-a","turn_id":"turn-1",
   "sandbox_mode":"workspace-write","turn_trigger":"exec"},
 "callId":"exec-1","itemId":"ctc_1","progressToken":1}`

const claudeMeta = `{"claudecode/toolUseId":"toolu_1","progressToken":2}`

func TestResolveCaller(t *testing.T) {
	cases := []struct {
		name, client, meta string
		env                map[string]string
		id, src            string
	}{
		{"claude-code env", ClientClaudeCode, claudeMeta,
			map[string]string{"CLAUDE_CODE_SESSION_ID": "cl"}, "cl", "claude"},
		{"claude-code empty env", ClientClaudeCode, claudeMeta,
			map[string]string{"CLAUDE_CODE_SESSION_ID": ""}, "fb", "fallback"},
		{"claude-code without env", ClientClaudeCode, claudeMeta, nil, "fb", "fallback"},
		// Removed overrides are ordinary env vars now.
		{"ORCA_CALLER and ORCA_RUN_ID are ignored", ClientClaudeCode, claudeMeta,
			map[string]string{"ORCA_CALLER": "me", "ORCA_RUN_ID": "run-1", "CLAUDE_CODE_SESSION_ID": "cl"},
			"cl", "claude"},
		{"ORCA_RUN_ID alone is not an identity", ClientCodex, `{}`,
			map[string]string{"ORCA_RUN_ID": "run-1"}, "fb", "fallback"},
		{"codex threadId", ClientCodex, codexMeta, nil, "thread-a", "codex"},
		{"codex turn metadata fallback", ClientCodex,
			`{"x-codex-turn-metadata":{"thread_id":"thread-b"}}`, nil, "thread-b", "codex"},
		{"codex empty threadId falls to turn metadata", ClientCodex,
			`{"threadId":"","x-codex-turn-metadata":{"thread_id":"thread-b"}}`, nil, "thread-b", "codex"},
		{"codex non-string threadId", ClientCodex,
			`{"threadId":42,"x-codex-turn-metadata":{"thread_id":"thread-b"}}`, nil, "thread-b", "codex"},
		{"codex keys are case-sensitive", ClientCodex, `{"threadid":"x","ThreadId":"y"}`, nil, "fb", "fallback"},
		{"codex malformed meta", ClientCodex, `{"threadId":`, nil, "fb", "fallback"},
		{"codex meta not an object", ClientCodex, `["thread-a"]`, nil, "fb", "fallback"},
		{"codex null meta", ClientCodex, `null`, nil, "fb", "fallback"},
		{"codex no meta", ClientCodex, ``, nil, "fb", "fallback"},
		{"codex turn metadata malformed", ClientCodex,
			`{"x-codex-turn-metadata":"thread-b"}`, nil, "fb", "fallback"},
		{"codex turn thread_id not string", ClientCodex,
			`{"x-codex-turn-metadata":{"thread_id":null}}`, nil, "fb", "fallback"},
		// A stale ancestor Claude id can leak into Codex's env; it must not be used.
		{"codex ignores claude env", ClientCodex, `{}`,
			map[string]string{"CLAUDE_CODE_SESSION_ID": "stale"}, "fb", "fallback"},
		{"claude ignores codex meta", ClientClaudeCode, codexMeta, nil, "fb", "fallback"},
		{"unknown client ignores harness ids", "other-client", codexMeta,
			map[string]string{"CLAUDE_CODE_SESSION_ID": "cl", "CODEX_THREAD_ID": "t"}, "fb", "fallback"},
	}
	for _, c := range cases {
		id, src := ResolveCaller(c.env, c.client, json.RawMessage(c.meta), "fb")
		if id != c.id || src != c.src {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, id, src, c.id, c.src)
		}
	}
}
