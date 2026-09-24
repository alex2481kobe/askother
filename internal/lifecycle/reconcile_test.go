package lifecycle

import (
	"testing"

	"github.com/alex2481kobe/orca/internal/run"
)

func TestDerive(t *testing.T) {
	cases := []struct {
		name      string
		state     run.State
		exec      run.Execution
		live      bool
		wantState run.State
		wantExec  run.Execution
	}{
		{"live run untouched", run.StateRunning, run.ExecLive, true, run.StateRunning, run.ExecLive},
		{"terminal untouched even if not live", run.StateDone, run.ExecExited, false, run.StateDone, run.ExecExited},
		{"launcher died before supervisor", run.StateStarting, run.ExecNotStarted, false, run.StateInterrupted, run.ExecNotStarted},
		{"supervisor died after intent", run.StateStarting, run.ExecUnknown, false, run.StateInterrupted, run.ExecUnknown},
		{"supervisor died while running", run.StateRunning, run.ExecLive, false, run.StateInterrupted, run.ExecUnknown},
		{"supervisor died while stopping", run.StateStopping, run.ExecLive, false, run.StateInterrupted, run.ExecUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := &run.Run{State: c.state, Execution: c.exec, Notes: []string{"keep"}}
			out := derive(in, c.live)
			if out.State != c.wantState || out.Execution != c.wantExec {
				t.Fatalf("got (%s, %s), want (%s, %s)", out.State, out.Execution, c.wantState, c.wantExec)
			}
			if in.State != c.state || in.Execution != c.exec {
				t.Fatal("input was modified")
			}
			// No completion time is invented and no note is added.
			if out.EndedAt != nil || len(out.Notes) != 1 {
				t.Fatalf("ended_at %v notes %v", out.EndedAt, out.Notes)
			}
		})
	}
}
