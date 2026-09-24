package lifecycle

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/alex2481kobe/askother/internal/config"
	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/worker"
)

// StopPoll is how often the supervisor re-reads its record for a stop
// request and records a newly reported native session.
const StopPoll = time.Second

// Test seams; production never changes them.
var (
	svPoll       = StopPoll
	svGrace      = run.DefaultStopGrace
	svRetryDelay = 5 * time.Second / 3 // 3 terminal retries over about 5 s
	svPublish    = (*run.Store).PublishAnswer
	svMaxLine    = run.MaxLine
)

// svRun is one supervision. session, dirty and protoErr are shared between
// the stdout reader and the stop watcher; stopActed and signaled are
// written only by the watcher and read after it has been joined.
type svRun struct {
	d     Deps
	id    string
	w     *run.Worker
	dec   worker.Decoder
	start time.Time // monotonic, from the worker start

	mu       sync.Mutex
	session  string // the native session the worker reported
	dirty    bool   // session is not in the record yet
	protoErr error

	stopActed bool // a stop request or timeout was acted on
	signaled  bool // Stop delivered TERM: the only way a run ends stopped
}

// Supervise is the body of `askother supervise <id>`. lock and
// ready are fds 3 and 4 in production (os.NewFile in cmd/askother), passed
// directly in tests. The run lock is released only when Supervise returns,
// after the terminal record is written or its retries are exhausted. It
// returns 0 when supervision was clean, whatever the run's outcome, and 1
// when the record could not be kept truthful by this process (readers then
// see interrupted once the lock is free).
func Supervise(d Deps, id string, lock, ready *os.File) int {
	// Before any child exists: a worker that inherited the run lock would
	// keep a dead supervisor looking alive.
	run.CloseOnExecInherited(int(lock.Fd()), int(ready.Fd()))
	defer lock.Close() // closing, never LOCK_UN, releases the run lock
	defer ready.Close()

	r, err := d.Store.Read(id)
	if err != nil || r.State != run.StateStarting {
		return 1
	}
	s := &svRun{d: d, id: id}
	inv, adapter, fail := s.invocation(r)
	if fail != nil {
		return s.failNotStarted(fail)
	}
	cmd, err := adapter.Build(inv)
	if err != nil {
		var f *worker.Failure
		if !errors.As(err, &f) {
			f = &worker.Failure{Code: string(run.CodeInvalidInput), Message: err.Error()}
		}
		return s.failNotStarted(f)
	}
	s.dec = adapter.NewDecoder(inv)

	// Intent before the spawn, so a crash from here on reads
	// interrupted/unknown rather than not_started.
	pid := os.Getpid()
	if !written(d.Store.Update(id, func(r *run.Run) error {
		r.Execution = run.ExecUnknown
		r.Runtime.SupervisorPID = &pid
		return nil
	})) {
		return 1
	}
	workerEnv := append(config.WorkerEnv(d.Env), "ASKOTHER_RUN_ID="+id)
	w, err := run.StartWorker(cmd, r.Request.CWD, workerEnv)
	if err != nil {
		return s.failNotStarted(&worker.Failure{Code: string(run.CodeWorkerFailed), Message: "start worker: " + err.Error()})
	}
	s.w, s.start = w, time.Now()
	started := d.now()
	if !written(d.Store.Update(id, func(r *run.Run) error {
		r.State, r.Execution, r.StartedAt = run.StateRunning, run.ExecLive, &started
		r.Runtime.WorkerPID, r.Runtime.WorkerPGID = &w.Pid, &w.Pgid
		return nil
	})) {
		// The record cannot say live: kill the group rather than leave an
		// unrecorded worker behind.
		_ = syscall.Kill(-w.Pgid, syscall.SIGKILL)
		w.Wait(func([]byte) error { return nil }, 0)
		return 1
	}
	// Readiness only after running is committed. A launcher that has gone
	// away gives EPIPE here, which is harmless: Go only dies of SIGPIPE on
	// fds 1 and 2.
	_, _ = ready.Write([]byte{1})
	ready.Close()
	return s.supervise()
}

// written reports whether an Update result is visible to readers.
func written(_ *run.Run, err error) bool { return err == nil || run.Visible(err) }

// invocation builds the adapter invocation from the record. A continuation
// resumes the native session copied into its own record at admission.
func (s *svRun) invocation(r *run.Run) (worker.Invocation, worker.Adapter, *worker.Failure) {
	adapter, ok := s.d.Adapters[r.Request.Worker]
	if !ok {
		return worker.Invocation{}, nil, &worker.Failure{Code: string(run.CodeInvalidInput),
			Message: "no adapter for worker " + r.Request.Worker}
	}
	inv := worker.Invocation{
		Binary: r.Runtime.Binary, Prompt: r.Request.Prompt, CWD: r.Request.CWD,
		Model: r.Request.Model, Effort: r.Request.Effort, Mode: r.Request.Mode,
		TempAnswer: s.d.Store.AnswerTempPath(s.id),
	}
	if r.NativeSessionID != nil {
		inv.ResumeID = *r.NativeSessionID
	}
	return inv, adapter, nil
}

// supervise reads the worker to its end while the watcher acts on stops,
// then publishes and writes the terminal record.
func (s *svRun) supervise() int {
	waitDone, watchDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watchDone)
		s.watch(waitDone)
	}()
	out := s.w.Wait(s.onLine, svMaxLine)
	close(waitDone)
	<-watchDone // no stopping write may land after the terminal one
	return s.finish(out)
}

// onLine feeds one stdout line to the decoder. A protocol error is
// remembered and reading goes on: the worker must never block on a full pipe.
func (s *svRun) onLine(line []byte) error {
	u, err := s.dec.Consume(line)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && s.protoErr == nil {
		s.protoErr = err
	}
	if u.SessionID != "" && u.SessionID != s.session {
		s.session, s.dirty = u.SessionID, true
	}
	return nil
}
