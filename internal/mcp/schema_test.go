package mcp

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
)

func TestDecodeStrictAcceptsValidInput(t *testing.T) {
	var in RunInput
	err := DecodeStrict([]byte(`{"key":"k","worker":"codex","prompt":"p","cwd":"/tmp/p","timeout_ms":10}`), &in)
	if err != nil || in.Key != "k" || in.CWD != "/tmp/p" || in.TimeoutMS == nil || *in.TimeoutMS != 10 {
		t.Fatalf("in = %+v, err = %v", in, err)
	}
	for _, raw := range []string{"", "null", " {} "} {
		var st StatusInput
		if err := DecodeStrict([]byte(raw), &st); err != nil || st.ID != "" {
			t.Errorf("status args %q: %+v %v", raw, st, err)
		}
	}
	var wi WaitInput
	if err := DecodeStrict([]byte(`{"ids":["a","b"],"any":true}`), &wi); err != nil || len(wi.IDs) != 2 || !wi.Any {
		t.Errorf("wait = %+v, err = %v", wi, err)
	}
}

func TestDecodeStrictRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		v    any
	}{
		{"unknown field on run", `{"key":"k","worker":"codex","prompt":"p","cwd":"/","sandbox":"x"}`, &RunInput{}},
		{"unknown field on send", `{"key":"k","id":"i","message":"m","mode":"x"}`, &SendInput{}},
		{"unknown field on wait", `{"ids":["a"],"timeout":5}`, &WaitInput{}},
		{"unknown field on result", `{"id":"i","page":2}`, &ResultInput{}},
		{"unknown field on status", `{"all":true}`, &StatusInput{}},
		{"removed scope on status", `{"scope":"all"}`, &StatusInput{}},
		{"unknown field on stop", `{"id":"i","force":true}`, &StopInput{}},
		{"case-folded key", `{"ID":"i"}`, &StopInput{}},
		{"duplicate key", `{"id":"a","id":"b"}`, &StopInput{}},
		{"trailing data", `{"id":"a"} {"id":"b"}`, &StopInput{}},
		{"not an object", `["id"]`, &StopInput{}},
		{"wrong type", `{"id":5}`, &StopInput{}},
	}
	for _, c := range cases {
		err := DecodeStrict([]byte(c.raw), c.v)
		if !errors.Is(err, run.CodeInvalidInput) {
			t.Errorf("%s: err = %v, want INVALID_INPUT", c.name, err)
		}
	}
}

func TestSummarizeUnread(t *testing.T) {
	end := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := &run.Run{ID: "a", Request: run.Request{Worker: "codex", Role: "scoper"},
		State: run.StateDone, Execution: run.ExecExited, EndedAt: &end,
		Result: run.Result{Available: true}}
	s := Summarize(r)
	if !s.Unread || s.Worker != "codex" || s.Role != "scoper" || !s.ResultAvailable {
		t.Errorf("summary = %+v", s)
	}
	r.FirstReadAt = &end
	if Summarize(r).Unread {
		t.Error("read run reported unread")
	}
	r.State, r.FirstReadAt = run.StateRunning, nil
	if Summarize(r).Unread {
		t.Error("running run reported unread")
	}
	b, _ := json.Marshal(Summarize(r))
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"previous_id", "exit", "error"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("summary %q = %v (present %v), want null", k, v, ok)
		}
	}
	for _, k := range []string{"parent_id", "activity"} {
		if _, ok := m[k]; ok {
			t.Errorf("summary has removed field %q", k)
		}
	}
	// An interrupted view has no ended_at and was never read: unread.
	r.State, r.EndedAt = run.StateInterrupted, nil
	if !Summarize(r).Unread {
		t.Error("interrupted run reported read")
	}
}
