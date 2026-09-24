package run

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Descriptors the launcher hands the supervisor.
const (
	LockFD  = 3 // runs/<id>.lock, held LOCK_EX
	ReadyFD = 4 // write end of the readiness pipe
)

// StartDetached starts self with args in a new session, with stdin, stdout
// and stderr on /dev/null and extra as fds 3 and up. The child inherits the
// environment.
//
// After it returns, the launcher must Close() its copy of the run lock and
// never LOCK_UN it: flock locks belong to the open file description, which the
// child shares, so LOCK_UN would release the child's lock too. It then closes
// its readiness writer, so the reader sees EOF if the child dies before
// signalling. A long-lived launcher must Reap the returned command.
func StartDetached(self string, args []string, extra []*os.File) (*exec.Cmd, error) {
	cmd := exec.Command(self, args...)
	cmd.ExtraFiles = extra
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// Reap waits for cmd in the background, so a long-lived `orca mcp` leaves no
// zombie supervisors behind.
func Reap(cmd *exec.Cmd) {
	go func() { _ = cmd.Wait() }()
}

// CloseOnExecInherited marks inherited descriptors close-on-exec. The
// supervisor calls it first, with LockFD and ReadyFD: Go does not set it on
// ExtraFiles in the child, and a worker that inherits the run lock keeps a
// dead supervisor looking alive.
func CloseOnExecInherited(fds ...int) {
	for _, fd := range fds {
		syscall.CloseOnExec(fd)
	}
}

// Readiness is what the launcher saw on the readiness pipe.
type Readiness int

const (
	Ready        Readiness = iota // the supervisor wrote the byte
	ReadyEOF                      // the pipe closed without the byte: report the record as it is
	ReadyTimeout                  // nothing within the timeout
)

func (r Readiness) String() string {
	switch r {
	case Ready:
		return "ready"
	case ReadyEOF:
		return "eof"
	case ReadyTimeout:
		return "timeout"
	}
	return "unknown"
}

// NewReadiness returns the readiness pipe. The writer goes to the supervisor
// as ReadyFD; the launcher closes its own copy right after StartDetached.
func NewReadiness() (r, w *os.File, err error) {
	return os.Pipe()
}

// WaitReady waits up to timeout for the readiness byte.
// It does not close r. An error is returned only for an unexpected read
// failure.
func WaitReady(r *os.File, timeout time.Duration) (Readiness, error) {
	if err := r.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return ReadyTimeout, err
	}
	var b [1]byte
	n, err := r.Read(b[:])
	switch {
	case n == 1:
		return Ready, nil
	case errors.Is(err, io.EOF):
		return ReadyEOF, nil
	case errors.Is(err, os.ErrDeadlineExceeded):
		return ReadyTimeout, nil
	}
	return ReadyTimeout, err
}
