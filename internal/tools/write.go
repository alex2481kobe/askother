package tools

import (
	"encoding/json"

	"github.com/alex2481kobe/orca/internal/lifecycle"
	"github.com/alex2481kobe/orca/internal/mcp"
	"github.com/alex2481kobe/orca/internal/run"
)

// The write tools: run, send, stop.

func (h *Handler) run(c lifecycle.Caller, args json.RawMessage) (any, error) {
	var in mcp.RunInput
	if err := mcp.DecodeStrict(args, &in); err != nil {
		return nil, err
	}
	r, reused, err := lifecycle.Start(h.d, c, lifecycle.StartRequest{
		Key: in.Key, Worker: in.Worker, Prompt: in.Prompt, CWD: in.CWD, Model: in.Model,
		Effort: in.Effort, Mode: in.Mode, Role: in.Role, Task: in.Task, TimeoutMS: in.TimeoutMS,
	})
	if err != nil {
		return nil, err
	}
	return runOutput(r, reused), nil
}

func (h *Handler) send(c lifecycle.Caller, args json.RawMessage) (any, error) {
	var in mcp.SendInput
	if err := mcp.DecodeStrict(args, &in); err != nil {
		return nil, err
	}
	r, reused, err := lifecycle.Send(h.d, c, lifecycle.SendRequest{Key: in.Key, ID: in.ID, Message: in.Message})
	if err != nil {
		return nil, err
	}
	return runOutput(r, reused), nil
}

// runOutput's watch is exactly a shell command a caller can run as given.
func runOutput(r *run.Run, reused bool) mcp.RunOutput {
	return mcp.RunOutput{ID: r.ID, State: r.State, Reused: reused, Watch: "orca wait " + r.ID}
}

func (h *Handler) stop(args json.RawMessage) (any, error) {
	var in mcp.StopInput
	if err := mcp.DecodeStrict(args, &in); err != nil {
		return nil, err
	}
	r, requested, err := lifecycle.RequestStop(h.d, in.ID)
	if err != nil {
		return nil, err
	}
	return mcp.StopOutput{ID: r.ID, State: r.State, StopRequested: requested}, nil
}
