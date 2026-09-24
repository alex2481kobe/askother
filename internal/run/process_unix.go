package run

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/alex2481kobe/askother/internal/worker"
)

// DrainBound is how long stdout and stderr are drained after the worker
// exits and its group is killed. A descendant outside the group can hold a
// pipe open forever, so the drain must be bounded.
const DrainBound = 2 * time.Second

// NoteDrainBoundExpired is the record note for a drain that hit DrainBound.
const NoteDrainBoundExpired = "drain_bound_expired"

// Worker is a started worker process in its own process group. Its stdout
// and stderr are os.Pipe read ends it owns, so waiting for the leader never
// waits on a descendant that still holds a pipe.
type Worker struct {
	Pid, Pgid int

	stdout, stderr *os.File
	stdin          *os.File // write end; closed once the stdin bytes are written
	cmd            *exec.Cmd
	exited         chan struct{}
}

// Outcome is what Wait observed.
type Outcome struct {
	Exit   worker.Exit
	Stderr StderrTail
	// DrainExpired is true when a pipe was still open at DrainBound: a
	// descendant outside the group held it (NoteDrainBoundExpired).
	DrainExpired bool
	// ReadErr is why stdout stopped being framed early: an oversized line
	// (PROTOCOL), the error onLine returned, or a read failure. Nil at a clean EOF or
	// when only the drain bound cut it short.
	ReadErr error
}

// StartWorker starts cmd in its own process group in dir with exactly env
// (never the caller's environment when env is empty).
// cmd.Stdin is written in full, then closed.
func StartWorker(cmd worker.Command, dir string, env []string) (*Worker, error) {
	var files []*os.File
	pipe := func() (r, w *os.File, err error) {
		r, w, err = os.Pipe()
		files = append(files, r, w)
		return r, w, err
	}
	closeAll := func() {
		for _, f := range files {
			if f != nil {
				f.Close()
			}
		}
	}
	inR, inW, err1 := pipe()
	outR, outW, err2 := pipe()
	errR, errW, err3 := pipe()
	if err := errors.Join(err1, err2, err3); err != nil {
		closeAll()
		return nil, err
	}
	c := exec.Command(cmd.Path, cmd.Args...)
	c.Dir = dir
	c.Env = append([]string{}, env...)
	c.Stdin, c.Stdout, c.Stderr = inR, outW, errW
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		closeAll()
		return nil, err
	}
	// Our copies of the child's ends must go, or stdout never reaches EOF.
	inR.Close()
	outW.Close()
	errW.Close()
	w := &Worker{Pid: c.Process.Pid, Pgid: c.Process.Pid, stdout: outR, stderr: errR,
		stdin: inW, cmd: c, exited: make(chan struct{})}
	go func() {
		_, _ = inW.Write(cmd.Stdin) // EPIPE if the worker exits unread; not our concern
		inW.Close()
	}()
	go func() {
		_ = c.Wait() // only Process.Wait: every stdio is an *os.File
		close(w.exited)
	}()
	return w, nil
}

// Exited is closed once the leader has exited and been reaped.
func (w *Worker) Exited() <-chan struct{} { return w.exited }

// Wait frames stdout into onLine (see Lines; max <= 0 means MaxLine) and
// keeps a bounded stderr tail until the leader exits. Then it SIGKILLs what
// is left of the process group and drains both pipes for at most
// DrainBound. It never blocks unbounded after the leader exits, which
// exec.Cmd's own pipes would (WaitDelay 0 was seen to block 30 s). Call it
// once.
func (w *Worker) Wait(onLine func([]byte) error, max int) Outcome {
	var out Outcome
	var wg sync.WaitGroup
	var outExpired, errExpired bool
	var t tail
	wg.Add(2)
	go func() {
		defer wg.Done()
		err := Lines(w.stdout, max, onLine)
		if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			out.ReadErr = err
			_, err = io.Copy(io.Discard, w.stdout) // keep the worker from blocking on a full pipe
		}
		outExpired = errors.Is(err, os.ErrDeadlineExceeded)
	}()
	go func() {
		defer wg.Done()
		_, err := io.Copy(&t, w.stderr)
		errExpired = errors.Is(err, os.ErrDeadlineExceeded)
	}()
	<-w.exited
	// Leftover group members (grandchildren) would hold the pipes open.
	_ = syscall.Kill(-w.Pgid, syscall.SIGKILL)
	deadline := time.Now().Add(DrainBound)
	_ = w.stdout.SetReadDeadline(deadline)
	_ = w.stderr.SetReadDeadline(deadline)
	wg.Wait()
	w.stdout.Close()
	w.stderr.Close()
	w.stdin.Close() // unblocks the stdin writer if a descendant held stdin unread
	out.Exit = exitOf(w.cmd.ProcessState)
	out.Stderr = t.result()
	out.DrainExpired = outExpired || errExpired
	return out
}

// exitOf maps a process state to worker.Exit: Code nil plus the signal name
// when the process was killed by a signal.
func exitOf(ps *os.ProcessState) worker.Exit {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return worker.Exit{Signal: signalName(ws.Signal())}
	}
	code := ps.ExitCode()
	return worker.Exit{Code: &code}
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGILL: "SIGILL", syscall.SIGTRAP: "SIGTRAP", syscall.SIGABRT: "SIGABRT",
	syscall.SIGBUS: "SIGBUS", syscall.SIGFPE: "SIGFPE", syscall.SIGKILL: "SIGKILL",
	syscall.SIGUSR1: "SIGUSR1", syscall.SIGSEGV: "SIGSEGV", syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM",
	syscall.SIGXCPU: "SIGXCPU", syscall.SIGXFSZ: "SIGXFSZ", syscall.SIGSYS: "SIGSYS",
}

func signalName(s syscall.Signal) string {
	if n, ok := signalNames[s]; ok {
		return n
	}
	return fmt.Sprintf("SIG%d", int(s))
}
