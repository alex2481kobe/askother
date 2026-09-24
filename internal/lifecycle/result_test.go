package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/worker"
)

// A mix of 1-, 2-, 3- and 4-byte code points.
const oMultibyte = "a\u00e9\u65e5\U0001F389 z\u00a0\u20ac\U0001F600\u00df"

func oDoneRun(t *testing.T, d Deps, id, text string) {
	t.Helper()
	oPut(t, d, oRecord(id, run.StateRunning, run.ExecLive, "c", oT0))
	oFinish(t, d, id, text) // lock file absent: the record is already terminal
}

func TestResultPagesReassembleByteExact(t *testing.T) {
	d, _, _ := oDeps(t)
	answer := strings.Repeat(oMultibyte, 7)
	oDoneRun(t, d, "run-a", answer)
	for _, limit := range []int64{4, 5, 6, 7, 11, 64} {
		var got strings.Builder
		var off int64
		for pages := 0; ; pages++ {
			if pages > len(answer) {
				t.Fatalf("limit %d: no progress", limit)
			}
			p, err := Result(d, "run-a", off, limit)
			if err != nil {
				t.Fatalf("limit %d offset %d: %v", limit, off, err)
			}
			if !p.Available || p.Offset != off || p.TotalBytes != int64(len(answer)) {
				t.Fatalf("limit %d: page %+v", limit, p)
			}
			if int64(len(p.Text)) > limit || !utf8.ValidString(p.Text) || p.NextOffset != off+int64(len(p.Text)) {
				t.Fatalf("limit %d: bad page %q next %d", limit, p.Text, p.NextOffset)
			}
			got.WriteString(p.Text)
			off = p.NextOffset
			if p.EOF {
				break
			}
		}
		if got.String() != answer {
			t.Fatalf("limit %d: reassembled %q", limit, got.String())
		}
	}
}

func TestResultOffsetAndLimitRules(t *testing.T) {
	d, _, _ := oDeps(t)
	oDoneRun(t, d, "run-a", "a\u00e9")
	for _, c := range []struct{ off, limit int64 }{{2, 0}, {-1, 0}, {4, 0}, {0, 3}, {0, -5}} {
		if _, err := Result(d, "run-a", c.off, c.limit); !errors.Is(err, run.CodeInvalidInput) {
			t.Fatalf("offset %d limit %d: %v", c.off, c.limit, err)
		}
	}
	if p, err := Result(d, "run-a", 3, 0); err != nil || !p.EOF || p.Text != "" || p.NextOffset != 3 {
		t.Fatalf("offset at end: %+v %v", p, err)
	}

	big := strings.Repeat("x", 100<<10)
	oDoneRun(t, d, "run-b", big)
	if p, _ := Result(d, "run-b", 0, 0); len(p.Text) != DefaultResultLimit {
		t.Fatalf("default page %d bytes", len(p.Text))
	}
	if p, _ := Result(d, "run-b", 0, 1<<20); len(p.Text) != MaxResultLimit {
		t.Fatalf("capped page %d bytes", len(p.Text))
	}
}

func TestResultFirstReadAtSetOnce(t *testing.T) {
	d, _, clk := oDeps(t)
	oDoneRun(t, d, "run-a", "hello")
	if oRead(t, d, "run-a").FirstReadAt != nil {
		t.Fatal("read before result")
	}
	clk.t = oT0.Add(time.Hour)
	if _, err := Result(d, "run-a", 0, 0); err != nil {
		t.Fatal(err)
	}
	clk.t = oT0.Add(2 * time.Hour)
	if _, err := Result(d, "run-a", 2, 0); err != nil {
		t.Fatal(err)
	}
	if got := oRead(t, d, "run-a").FirstReadAt; got == nil || !got.Equal(oT0.Add(time.Hour)) {
		t.Fatalf("first_read_at %v", got)
	}
	// A second reader that saw first_read_at unset before the first one
	// saved it must not overwrite it.
	markRead(d, "run-a")
	if got := oRead(t, d, "run-a").FirstReadAt; !got.Equal(oT0.Add(time.Hour)) {
		t.Fatalf("first_read_at overwritten: %v", got)
	}
}

func TestResultNotDone(t *testing.T) {
	d, _, _ := oDeps(t)
	f := oRecord("run-f", run.StateFailed, run.ExecExited, "c", oT0)
	f.Error = &worker.Failure{Code: "WORKER_FAILED", Message: "exit 1"}
	oPut(t, d, f)
	os.WriteFile(filepath.Join(filepath.Dir(d.Store.AnswerTempPath("x")), "run-f.txt"), []byte("stray"), 0o600)
	p, err := Result(d, "run-f", 0, 0)
	if err != nil || p.Available || p.Text != "" || p.Error == nil || p.Error.Code != "WORKER_FAILED" || p.State != run.StateFailed {
		t.Fatalf("failed run: %+v %v", p, err)
	}
	oPut(t, d, oRecord("run-r", run.StateRunning, run.ExecLive, "c", oT0)) // dead: no lock holder
	p, err = Result(d, "run-r", 0, 0)
	if err != nil || p.Available || p.State != run.StateInterrupted {
		t.Fatalf("dead run: %+v %v", p, err)
	}
	// Reading an interrupted run marks it read; the record itself keeps its
	// stored state.
	if got := oRead(t, d, "run-r"); got.FirstReadAt == nil || got.State != run.StateRunning {
		t.Fatalf("dead run after result: %s read %v", got.State, got.FirstReadAt)
	}
	oLive(t, d, "run-l")
	if _, err := Result(d, "run-l", 0, 0); err != nil || oRead(t, d, "run-l").FirstReadAt != nil {
		t.Fatalf("a live run was marked read: %v", err)
	}
}

func TestResultCorruptAndRemoved(t *testing.T) {
	d, _, _ := oDeps(t)
	oDoneRun(t, d, "run-a", "hello")
	txt := filepath.Join(filepath.Dir(d.Store.AnswerTempPath("x")), "run-a.txt")
	if err := os.WriteFile(txt, []byte("hello!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Result(d, "run-a", 0, 0); !errors.Is(err, run.CodeCorruptState) {
		t.Fatalf("size mismatch: %v", err)
	}
	if err := d.Store.Remove("run-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Result(d, "run-a", 0, 0); !errors.Is(err, run.CodeNotFound) {
		t.Fatalf("removed: %v", err)
	}
}

// The sweep removes the record, then the answer. A reader that lands
// between the two must answer NOT_FOUND, not a page or CORRUPT_STATE.
func TestResultMidSweepIsNotFound(t *testing.T) {
	d, _, _ := oDeps(t)
	oDoneRun(t, d, "run-a", "hello")
	dir := filepath.Dir(d.Store.AnswerTempPath("x"))
	jsonGone := make(chan struct{})
	swept := make(chan error, 1)
	go func() {
		swept <- d.Store.WithIndexLock(func() error {
			os.Remove(filepath.Join(dir, "run-a.json"))
			close(jsonGone)
			time.Sleep(200 * time.Millisecond)
			return os.Remove(filepath.Join(dir, "run-a.txt"))
		})
	}()
	<-jsonGone
	if _, err := Result(d, "run-a", 0, 0); !errors.Is(err, run.CodeNotFound) {
		t.Fatalf("mid-sweep: %v", err)
	}
	if err := <-swept; err != nil {
		t.Fatal(err)
	}
}

// Result racing a real Sweep yields a correct page or NOT_FOUND, never a
// crash or wrong text.
func TestResultConcurrentWithSweep(t *testing.T) {
	answer := strings.Repeat(oMultibyte, 300)
	for round := range 20 {
		d, _, clk := oDeps(t)
		oDoneRun(t, d, "run-a", answer)
		clk.t = oT0.Add(8 * 24 * time.Hour) // unread past KeepUnread
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var off int64
				for {
					p, err := Result(d, "run-a", off, 64)
					if errors.Is(err, run.CodeNotFound) {
						return
					}
					if err != nil || p.Text != answer[off:p.NextOffset] {
						t.Errorf("round %d offset %d: %q %v", round, off, p.Text, err)
						return
					}
					if p.EOF {
						return
					}
					off = p.NextOffset
				}
			}()
		}
		time.Sleep(time.Duration(round) * time.Millisecond)
		Sweep(d)
		wg.Wait()
		if _, err := d.Store.Read("run-a"); err == nil {
			// A reader got in first and set first_read_at: the 24 h clock now applies.
			if oRead(t, d, "run-a").FirstReadAt == nil {
				t.Fatalf("round %d: unread run survived the sweep", round)
			}
		}
	}
}
