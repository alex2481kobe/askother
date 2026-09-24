package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alex2481kobe/askother/internal/run"
)

// oLive writes a running record whose lock this process holds (live).
func oLive(t *testing.T, d Deps, id string) {
	t.Helper()
	lock, err := d.Store.CreateRunLock(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	oPut(t, d, oRecord(id, run.StateRunning, run.ExecLive, "c", oT0))
}

func TestWaitAllAnyTimeoutNoReceipt(t *testing.T) {
	d, _, _ := oDeps(t)
	oLive(t, d, "run-a")
	oLive(t, d, "run-b")
	ids := []string{"run-a", "run-b"}

	res, err := Wait(context.Background(), d, ids, true, 150*time.Millisecond)
	if err != nil || !res.TimedOut || res.Satisfied || len(res.Runs) != 2 {
		t.Fatalf("nothing ended: %+v %v", res, err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		oFinish(t, d, "run-a", "a")
	}()
	res, err = Wait(context.Background(), d, ids, true, 5*time.Second)
	if err != nil || !res.Satisfied || res.TimedOut || res.Runs[0].State != run.StateDone || res.Runs[1].State != run.StateRunning {
		t.Fatalf("any: %+v %v", res, err)
	}
	res, err = Wait(context.Background(), d, ids, false, 150*time.Millisecond)
	if err != nil || !res.TimedOut || res.Runs[0].State != run.StateDone {
		t.Fatalf("all with one pending: %+v %v", res, err)
	}
	oFinish(t, d, "run-b", "b")
	res, err = Wait(context.Background(), d, ids, false, time.Second)
	if err != nil || !res.Satisfied {
		t.Fatalf("all: %+v %v", res, err)
	}
	for _, id := range ids {
		if oRead(t, d, id).FirstReadAt != nil {
			t.Fatalf("wait marked %s read", id)
		}
	}
	if _, err := Wait(context.Background(), d, nil, false, time.Second); !errors.Is(err, run.CodeInvalidInput) {
		t.Fatalf("no ids: %v", err)
	}
}

func TestWaitContextCancelled(t *testing.T) {
	d, _, _ := oDeps(t)
	oLive(t, d, "run-a")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res, err := Wait(ctx, d, []string{"run-a"}, false, time.Minute)
	if err != nil || !res.TimedOut || ctx.Err() == nil {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestWaitInterruptedIsTerminalAndSeenByTwoWaiters(t *testing.T) {
	d, root, _ := oDeps(t)
	oPut(t, d, oRecord("run-a", run.StateRunning, run.ExecLive, "c", oT0))
	kill := oHoldLock(t, root, "run-a")

	var wg sync.WaitGroup
	results := make([]WaitResult, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = Wait(context.Background(), d, []string{"run-a"}, false, 5*time.Second)
		}()
	}
	time.Sleep(150 * time.Millisecond)
	kill()
	wg.Wait()
	for i, res := range results {
		if errs[i] != nil || !res.Satisfied || res.Runs[0].State != run.StateInterrupted || res.Runs[0].Execution != run.ExecUnknown {
			t.Fatalf("waiter %d: %+v %v", i, res, errs[i])
		}
	}
	if got := oRead(t, d, "run-a"); got.State != run.StateRunning || got.FirstReadAt != nil {
		t.Fatalf("record %s read=%v: waiting must not write", got.State, got.FirstReadAt)
	}
}

// Detection latency from a committed terminal record to Wait returning, at
// the production poll interval. Target: well under 1 s.
func TestWaitDetectionLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	d, _, _ := oDeps(t)
	const n = 20
	var lat []time.Duration
	for i := range n {
		id := fmt.Sprintf("run-%d", i)
		oLive(t, d, id)
		published := make(chan time.Time, 1)
		go func() {
			time.Sleep(time.Duration(37*i%90+10) * time.Millisecond)
			oFinish(t, d, id, "x")
			published <- time.Now()
		}()
		res, err := Wait(context.Background(), d, []string{id}, false, 5*time.Second)
		back := time.Now()
		if err != nil || !res.Satisfied {
			t.Fatalf("%s: %+v %v", id, res, err)
		}
		lat = append(lat, back.Sub(<-published))
	}
	slices.Sort(lat)
	p50, p95 := lat[n/2], lat[n*95/100]
	t.Logf("wait detection latency over %d runs: p50 %v, p95 %v, max %v (poll %v)", n, p50, p95, lat[n-1], waitPoll)
	if p95 > 500*time.Millisecond {
		t.Fatalf("p95 detection latency %v", p95)
	}
}
