package worker

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// sharedRuns runs two invocations through ONE adapter value concurrently.
// Both Builds finish before either decoder exists, the order a stateful
// adapter gets wrong. check runs one run's decoder and reports its problems.
func sharedRuns(t *testing.T, a Adapter, ins [2]Invocation, check func(i int, d Decoder)) {
	t.Helper()
	var built, done sync.WaitGroup
	built.Add(2)
	for i, in := range ins {
		done.Add(1)
		go func() {
			defer done.Done()
			_, err := a.Build(in)
			built.Done()
			if err != nil {
				t.Errorf("run %d: Build: %v", i, err)
				return
			}
			built.Wait()
			check(i, a.NewDecoder(in))
		}()
	}
	done.Wait()
}

func TestCodexSharedWorker(t *testing.T) {
	dir := t.TempDir()
	threads := [2]string{"01900000-0000-7000-8000-0000000000a1", "01900000-0000-7000-8000-0000000000b2"}
	var ins [2]Invocation
	for i, id := range threads {
		ins[i] = Invocation{Binary: "/bin/codex", Mode: "read-only", ResumeID: id, TempAnswer: filepath.Join(dir, id+".tmp")}
		if err := os.WriteFile(ins[i].TempAnswer, []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var a Adapter = Codex{}
	sharedRuns(t, a, ins, func(i int, d Decoder) {
		other := threads[1-i]
		// Each decoder validates its own resume thread, not the other run's.
		if _, err := d.Consume([]byte(`{"type":"thread.started","thread_id":"` + other + `"}`)); err == nil {
			t.Errorf("run %d accepted the other run's thread", i)
		}
		d = a.NewDecoder(ins[i])
		if _, err := d.Consume([]byte(`{"type":"thread.started","thread_id":"` + threads[i] + `"}`)); err != nil {
			t.Errorf("run %d: %v", i, err)
		}
		if _, err := d.Consume([]byte(cxCompleted)); err != nil {
			t.Errorf("run %d: %v", i, err)
		}
		f, err := d.Finish(cxCode(0))
		if err != nil || !f.Success || f.AnswerPath != ins[i].TempAnswer || f.SessionID != threads[i] {
			t.Errorf("run %d: final %+v failure %+v, %v; want answer %s", i, f, f.Failure, err, ins[i].TempAnswer)
		}
	})
}

func TestClaudeSharedWorker(t *testing.T) {
	dir := t.TempDir()
	sessions := [2]string{"00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000b2"}
	var ins [2]Invocation
	for i, id := range sessions {
		ins[i] = Invocation{Binary: "c", Mode: "dontAsk", TempAnswer: filepath.Join(dir, id+".tmp")}
	}
	ins[1].ResumeID = sessions[1] // one new session, one resume
	var a Adapter = Claude{}
	init := func(id string) []byte {
		return []byte(`{"type":"system","subtype":"init","session_id":"` + id + `"}`)
	}
	sharedRuns(t, a, ins, func(i int, d Decoder) {
		if ins[i].ResumeID != "" {
			if _, err := d.Consume(init(sessions[1-i])); err == nil {
				t.Errorf("run %d accepted the other run's session", i)
			}
			d = a.NewDecoder(ins[i])
		}
		if _, err := d.Consume(init(sessions[i])); err != nil {
			t.Errorf("run %d: %v", i, err)
		}
		if _, err := d.Consume([]byte(`{"type":"result","is_error":false,"result":"` + sessions[i] + `"}`)); err != nil {
			t.Errorf("run %d: %v", i, err)
		}
		f, err := d.Finish(cxCode(0))
		b, _ := os.ReadFile(ins[i].TempAnswer)
		if err != nil || !f.Success || f.AnswerPath != ins[i].TempAnswer || f.SessionID != sessions[i] || string(b) != sessions[i] {
			t.Errorf("run %d: final %+v failure %+v, %v; answer %q", i, f, f.Failure, err, b)
		}
	})
}
