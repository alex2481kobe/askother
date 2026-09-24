package tools

import (
	"context"
	"encoding/json"

	"github.com/alex2481kobe/askother/internal/lifecycle"
	"github.com/alex2481kobe/askother/internal/mcp"
	"github.com/alex2481kobe/askother/internal/run"
)

// The read tools: wait, result, status. None of them writes, except that
// serving a result records first_read_at.

// wait relies on lifecycle.Wait to refuse an empty ids list.
func (h *Handler) wait(ctx context.Context, args json.RawMessage) (any, error) {
	var in mcp.WaitInput
	if err := mcp.DecodeStrict(args, &in); err != nil {
		return nil, err
	}
	res, err := lifecycle.Wait(ctx, h.d, in.IDs, in.Any, mcp.WaitBudget)
	if err != nil {
		return nil, err
	}
	return mcp.WaitOutput{Satisfied: res.Satisfied, TimedOut: res.TimedOut, Runs: summaries(res.Runs)}, nil
}

func (h *Handler) result(args json.RawMessage) (any, error) {
	var in mcp.ResultInput
	if err := mcp.DecodeStrict(args, &in); err != nil {
		return nil, err
	}
	var offset, limit int64
	if in.Offset != nil {
		offset = *in.Offset
	}
	if in.Limit != nil {
		limit = *in.Limit
		if limit == 0 {
			// Result reads 0 as "default"; the schema says minimum 1.
			return nil, run.Errorf(run.CodeInvalidInput, "limit must be at least 1; omit it for the default page")
		}
	}
	p, err := lifecycle.Result(h.d, in.ID, offset, limit)
	if err != nil {
		return nil, err
	}
	return mcp.ResultOutput{ID: p.ID, State: p.State, Available: p.Available, Text: p.Text,
		Offset: p.Offset, NextOffset: p.NextOffset, TotalBytes: p.TotalBytes, EOF: p.EOF,
		Error: p.Error}, nil
}

func (h *Handler) status(args json.RawMessage) (any, error) {
	var in mcp.StatusInput
	if err := mcp.DecodeStrict(args, &in); err != nil {
		return nil, err
	}
	runs, err := lifecycle.Status(h.d, in.ID)
	if err != nil {
		return nil, err
	}
	return mcp.StatusOutput{Runs: summaries(runs)}, nil
}
