package lifecycle

import (
	"testing"

	"github.com/alex2481kobe/orca/internal/run"
)

func TestRequestStopLiveRun(t *testing.T) {
	d := rDeps(t, rModeReady)
	started, _, err := Start(d, Caller{ID: "c"}, rStartReq(t, "k1"))
	if err != nil {
		t.Fatal(err)
	}
	r, requested, err := RequestStop(d, started.ID)
	if err != nil || !requested {
		t.Fatalf("RequestStop = requested %v, err %v", requested, err)
	}
	if r.StopRequestedAt == nil || r.StopReason == nil || *r.StopReason != run.StopCaller {
		t.Fatalf("stop fields %v %v, want set with reason caller", r.StopRequestedAt, r.StopReason)
	}
	if r.State != run.StateRunning {
		t.Fatalf("state %s: a stop request changes nothing but the stop fields", r.State)
	}
	first := *r.StopRequestedAt
	again, requested, err := RequestStop(d, started.ID)
	if err != nil || !requested || !again.StopRequestedAt.Equal(first) {
		t.Fatalf("repeat = %v requested %v err %v, want the first timestamp kept", again.StopRequestedAt, requested, err)
	}
}

func TestRequestStopEndedRuns(t *testing.T) {
	d := rDeps(t, rModeReady)
	done := rDone(t, d)
	orphan := rPut(t, d, &run.Run{State: run.StateRunning, Execution: run.ExecLive})
	for _, c := range []struct {
		name  string
		id    string
		state run.State
	}{{"terminal", done.ID, run.StateDone}, {"lock free", orphan.ID, run.StateInterrupted}} {
		t.Run(c.name, func(t *testing.T) {
			r, requested, err := RequestStop(d, c.id)
			if err != nil || requested || r.State != c.state {
				t.Fatalf("RequestStop = %v requested %v err %v, want %s not requested", r, requested, err, c.state)
			}
			stored, err := d.Store.Read(c.id)
			if err != nil || stored.StopRequestedAt != nil || stored.StopReason != nil {
				t.Fatalf("stored %+v err %v: nothing may be written", stored, err)
			}
		})
	}
	_, _, err := RequestStop(d, "zz")
	rCode(t, err, run.CodeNotFound)
	_, _, err = RequestStop(d, done.ID[:20])
	rCode(t, err, run.CodeNotFound)
}
