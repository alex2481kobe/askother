package ui

import (
	"time"

	"github.com/alex2481kobe/orca/internal/lifecycle"
	"github.com/alex2481kobe/orca/internal/run"
)

type runView struct {
	ID         string           `json:"id"`
	ParentID   *string          `json:"parent_id"`
	PreviousID *string          `json:"previous_id"`
	RootID     string           `json:"root_id"`
	RootSource run.CallerSource `json:"root_source"`
	Worker     string           `json:"worker"`
	Model      string           `json:"model"`
	Mode       string           `json:"mode"`
	Role       string           `json:"role"`
	Task       string           `json:"task"`
	CWD        string           `json:"cwd"`
	State      run.State        `json:"state"`
	DurationMS int64            `json:"duration_ms"`
	CreatedAt  time.Time        `json:"created_at"`
	Unread     bool             `json:"unread"`
	Error      string           `json:"error,omitempty"`
}

func (h *handler) snapshot() ([]runView, error) {
	runs, err := lifecycle.Status(h.d, "")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*run.Run, len(runs))
	for _, r := range runs {
		byID[r.ID] = r
	}
	now := time.Now().UTC()
	out := make([]runView, 0, len(runs))
	for _, r := range runs {
		if r.UIHiddenAt != nil {
			continue
		}
		root := r
		seen := map[string]bool{r.ID: true}
		for root.ParentID != nil {
			parent := byID[*root.ParentID]
			if parent == nil || seen[parent.ID] {
				break
			}
			root = parent
			seen[root.ID] = true
		}
		view := runView{
			ID: r.ID, ParentID: r.ParentID, PreviousID: r.PreviousID,
			RootID: root.CallerID, RootSource: root.CallerSource,
			Worker: r.Request.Worker, Model: r.Request.Model, Mode: r.Request.Mode,
			Role: r.Request.Role, Task: r.Request.Task, CWD: r.Request.CWD,
			State: r.State, CreatedAt: r.CreatedAt,
			Unread: run.IsTerminal(r.State) && r.FirstReadAt == nil,
		}
		if r.DurationMS != nil {
			view.DurationMS = *r.DurationMS
		} else if r.StartedAt != nil {
			view.DurationMS = now.Sub(*r.StartedAt).Milliseconds()
		}
		if r.Error != nil {
			view.Error = r.Error.Message
		}
		out = append(out, view)
	}
	return out, nil
}
