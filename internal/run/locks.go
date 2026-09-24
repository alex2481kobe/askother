package run

import (
	"errors"
	"os"
	"syscall"
)

// WithIndexLock runs fn while holding an exclusive flock on index.lock.
// Every JSON read-modify-write, admission and cleanup decision runs under it.
//
// fn must be short: file reads, liveness probes and synced writes only. It
// must never wait on a model run, stream output, sleep, wait for a process or
// write to an MCP client. fn must not call
// WithIndexLock or Update itself: flock is per open file, so a nested call
// deadlocks.
func (s *Store) WithIndexLock(fn func() error) error {
	f, err := os.OpenFile(s.indexPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Errorf(CodeStoreIO, "open index.lock: %v", err)
	}
	defer f.Close() // closing the file releases the lock
	if err := flock(f, syscall.LOCK_EX); err != nil {
		return Errorf(CodeStoreIO, "lock index.lock: %v", err)
	}
	return fn()
}

// CreateRunLock creates runs/<id>.lock with O_EXCL and takes LOCK_EX|LOCK_NB
// on it, before the record exists. O_EXCL also reserves
// the id: a collision fails here. The launcher hands the returned file to
// the supervisor, then closes its copy (never LOCK_UN).
func (s *Store) CreateRunLock(id string) (*os.File, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	path := s.file(id, ".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, Errorf(CodeStoreIO, "create run lock %s: %v", id, err)
	}
	if err := flock(f, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		os.Remove(path) // we created it exclusively and nobody holds it
		return nil, Errorf(CodeStoreIO, "lock run %s: %v", id, err)
	}
	return f, nil
}

// ProbeLive reports whether a supervisor holds runs/<id>.lock. It tries a
// non-blocking shared lock on the existing file: two concurrent probes never
// block each other, while exclusive probes were seen to make each other read
// a finished run as running. It never creates the file; a missing lock
// means not live and the caller decides what that implies.
func (s *Store) ProbeLive(id string) (bool, error) {
	if err := checkID(id); err != nil {
		return false, err
	}
	f, err := os.Open(s.file(id, ".lock"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, Errorf(CodeStoreIO, "open run lock %s: %v", id, err)
	}
	defer f.Close() // drops our probe lock
	switch err := flock(f, syscall.LOCK_SH|syscall.LOCK_NB); {
	case err == nil:
		return false, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, nil
	default:
		return false, Errorf(CodeStoreIO, "probe run lock %s: %v", id, err)
	}
}

func flock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if err != syscall.EINTR {
			return err
		}
	}
}
