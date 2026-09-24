package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// faultOp names one step of a publication or removal; the unexported
// Store.fault hook can fail any of them in tests.
type faultOp string

const (
	opWrite   faultOp = "write" // an injected write failure leaves a half-written temp
	opSync    faultOp = "sync"
	opClose   faultOp = "close"
	opRename  faultOp = "rename"
	opDirSync faultOp = "dirsync"
	opRemove  faultOp = "remove"
)

// StoreError is a failed publication of Path. Visible is false when the step failed before or at the rename, so readers
// still see the old content (or nothing). Visible is true when only the
// directory sync failed: readers already see the new content, but it may not
// survive a crash. A visible write is never rolled back.
type StoreError struct {
	Path    string
	Op      string
	Visible bool
	Err     error
}

func (e *StoreError) Error() string {
	state := "not visible"
	if e.Visible {
		state = "visible, not durable"
	}
	return fmt.Sprintf("STORE_IO: %s %s (%s): %v", e.Op, filepath.Base(e.Path), state, e.Err)
}

func (e *StoreError) Unwrap() error { return e.Err }

// Is makes every StoreError match CodeStoreIO.
func (e *StoreError) Is(target error) bool { return target == CodeStoreIO }

// Visible reports whether err is a StoreError whose new content is already
// visible to readers (only durability is in doubt).
func Visible(err error) bool {
	var se *StoreError
	return errors.As(err, &se) && se.Visible
}

// step runs fn unless the test hook fails op for path first.
func (s *Store) step(op faultOp, path string, fn func() error) error {
	if s.fault != nil {
		if err := s.fault(op, path); err != nil {
			return err
		}
	}
	return fn()
}

// writeAtomic replaces path with data: temp in the same directory, write,
// File.Sync (F_FULLFSYNC on darwin), close, rename, directory sync.
func (s *Store) writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return &StoreError{Path: path, Op: "create", Err: err}
	}
	tmp := f.Name()
	fail := func(op string, err error) error {
		f.Close()
		os.Remove(tmp) // before the rename: nothing visible to roll back
		return &StoreError{Path: path, Op: op, Err: err}
	}
	if s.fault != nil {
		if err := s.fault(opWrite, path); err != nil {
			f.Write(data[:len(data)/2])
			return fail("write", err)
		}
	}
	if _, err := f.Write(data); err != nil {
		return fail("write", err)
	}
	if err := s.step(opSync, path, f.Sync); err != nil {
		return fail("sync", err)
	}
	if err := s.step(opClose, path, f.Close); err != nil {
		return fail("close", err)
	}
	return s.commit(tmp, path)
}

// commit renames tmp to path and syncs the directory. It never removes
// anything once the rename was attempted, because a rename that reported an
// error may still have happened: a failed rename leaves tmp for the sweep,
// and a failed directory sync leaves path visible.
func (s *Store) commit(tmp, path string) error {
	if err := s.step(opRename, path, func() error { return os.Rename(tmp, path) }); err != nil {
		return &StoreError{Path: path, Op: "rename", Err: err}
	}
	if err := s.step(opDirSync, path, func() error { return syncDir(filepath.Dir(path)) }); err != nil {
		return &StoreError{Path: path, Op: "dirsync", Visible: true, Err: err}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}

// PublishAnswer publishes the finished answer in tempPath (a file in the runs
// directory, normally AnswerTempPath(id)) as runs/<id>.txt: chmod 0600, sync,
// close, rename, directory sync. It returns the answer's size, which readers
// check the file against. Errors are StoreErrors with the same
// visible/durable split as writeAtomic; the size is returned with a Visible
// error too. The caller must not write a done record unless err is nil.
func (s *Store) PublishAnswer(id, tempPath string) (int64, error) {
	if err := checkID(id); err != nil {
		return 0, err
	}
	path := s.file(id, ".txt")
	if filepath.Dir(filepath.Clean(tempPath)) != s.runs {
		return 0, Errorf(CodeInvalidInput, "answer temp %s is not in the runs directory", filepath.Base(tempPath))
	}
	f, err := os.OpenFile(tempPath, os.O_RDWR, 0)
	if err != nil {
		return 0, &StoreError{Path: path, Op: "open", Err: err}
	}
	fail := func(op string, err error) (int64, error) {
		f.Close()
		return 0, &StoreError{Path: path, Op: op, Err: err}
	}
	if err := f.Chmod(0o600); err != nil {
		return fail("chmod", err)
	}
	fi, err := f.Stat()
	if err != nil {
		return fail("stat", err)
	}
	if err := s.step(opSync, path, f.Sync); err != nil {
		return fail("sync", err)
	}
	if err := s.step(opClose, path, f.Close); err != nil {
		return fail("close", err)
	}
	if err := s.commit(tempPath, path); err != nil {
		if Visible(err) {
			return fi.Size(), err
		}
		return 0, err
	}
	return fi.Size(), nil
}

// OpenAnswer opens runs/<id>.txt for reading and checks its size against the
// record's result.bytes, so a partial answer is never served. The caller
// serves it only when the record says result.available; a stray .txt beside
// a non-done record is never read. A missing .txt is NOT_FOUND when the
// record is gone too (the sweep removes the record first) and CORRUPT_STATE
// otherwise.
func (s *Store) OpenAnswer(id string, expectBytes int64) (*os.File, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	f, err := os.Open(s.file(id, ".txt"))
	if errors.Is(err, os.ErrNotExist) {
		if _, jerr := os.Stat(s.file(id, ".json")); errors.Is(jerr, os.ErrNotExist) {
			return nil, Errorf(CodeNotFound, "run %s not found", id)
		}
		return nil, Errorf(CodeCorruptState, "run %s: answer missing", id)
	}
	if err != nil {
		return nil, Errorf(CodeStoreIO, "open answer %s: %v", id, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, Errorf(CodeStoreIO, "stat answer %s: %v", id, err)
	}
	if fi.Size() != expectBytes {
		f.Close()
		return nil, Errorf(CodeCorruptState, "run %s: answer is %d bytes, record says %d", id, fi.Size(), expectBytes)
	}
	return f, nil
}
