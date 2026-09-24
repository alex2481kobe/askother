package mcp

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alex2481kobe/orca/internal/worker"
)

// Synthetic facts; the real ones come from the adapters.
var testFacts = []worker.Facts{
	{Name: "codex", DefaultMode: "read-only", Modes: []string{"read-only", "workspace-write", "danger-full-access"},
		Pinned: []string{`approval_policy="never"`}},
	{Name: "claude", DefaultMode: "dontAsk", Modes: []string{"dontAsk", "plan", "bypassPermissions"}},
}

type wireTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	} `json:"inputSchema"`
	Annotations *struct {
		ReadOnlyHint *bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

func listTools(t *testing.T) map[string]wireTool {
	t.Helper()
	b, err := json.Marshal(map[string]any{"tools": Tools(testFacts)})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatal("tools/list is not valid JSON")
	}
	var got struct{ Tools []wireTool }
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	byName := map[string]wireTool{}
	for _, tl := range got.Tools {
		byName[tl.Name] = tl
	}
	if len(got.Tools) != 6 || len(byName) != 6 {
		t.Fatalf("got %d tools (%d distinct), want 6", len(got.Tools), len(byName))
	}
	return byName
}

func TestToolsSchemasMatchContract(t *testing.T) {
	tools := listTools(t)
	want := map[string]struct {
		input    any
		required []string
		readOnly bool
	}{
		"run":    {RunInput{}, []string{"key", "worker", "prompt", "cwd"}, false},
		"send":   {SendInput{}, []string{"key", "id", "message"}, false},
		"wait":   {WaitInput{}, []string{"ids"}, true},
		"result": {ResultInput{}, []string{"id"}, true},
		"status": {StatusInput{}, nil, true},
		"stop":   {StopInput{}, []string{"id"}, false},
	}
	for name, w := range want {
		tl, ok := tools[name]
		if !ok {
			t.Errorf("missing tool %q", name)
			continue
		}
		s := tl.InputSchema
		if s.Type != "object" || s.AdditionalProperties == nil || *s.AdditionalProperties {
			t.Errorf("%s: schema must be an object with additionalProperties:false", name)
		}
		if !reflect.DeepEqual(s.Required, w.required) {
			t.Errorf("%s: required = %v, want %v", name, s.Required, w.required)
		}
		props := map[string]bool{}
		for k := range s.Properties {
			props[k] = true
		}
		if fields := jsonNames(reflect.TypeOf(w.input)); !reflect.DeepEqual(props, fields) {
			t.Errorf("%s: schema properties %v != input fields %v", name, sortedKeys(props), sortedKeys(fields))
		}
		hint := tl.Annotations != nil && tl.Annotations.ReadOnlyHint != nil && *tl.Annotations.ReadOnlyHint
		if hint != w.readOnly {
			t.Errorf("%s: readOnlyHint = %v, want %v", name, hint, w.readOnly)
		}
		if tl.Description == "" {
			t.Errorf("%s: empty description", name)
		}
	}
}

func TestRunDescriptionRendersFacts(t *testing.T) {
	tl := listTools(t)["run"]
	for _, want := range []string{
		"codex: default mode read-only", "danger-full-access", `approval_policy="never"`, "call the wait tool",
		"claude: default mode dontAsk", "bypassPermissions",
	} {
		if !strings.Contains(tl.Description, want) {
			t.Errorf("run description lacks %q:\n%s", want, tl.Description)
		}
	}
	var workerProp struct{ Enum []string }
	json.Unmarshal(tl.InputSchema.Properties["worker"], &workerProp)
	if !slices.Equal(workerProp.Enum, []string{"codex", "claude"}) {
		t.Errorf("worker enum = %v", workerProp.Enum)
	}
	other := Tools([]worker.Facts{{Name: "x", DefaultMode: "safe", Modes: []string{"safe"}}})[0].Description
	if strings.Contains(other, "codex") || !strings.Contains(other, "x: default mode safe") {
		t.Errorf("description is not rendered from facts:\n%s", other)
	}
}
