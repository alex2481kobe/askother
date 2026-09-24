package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// putRun stores a fresh starting record with id and returns it.
func putRun(t *testing.T, s *Store, id string) *Run {
	t.Helper()
	r := minimalRun()
	r.ID = id
	if err := s.WithIndexLock(func() error { return s.Put(r) }); err != nil {
		t.Fatal(err)
	}
	return r
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestOpenAndFileModesArePrivate(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	putRun(t, s, id)
	for path, want := range map[string]os.FileMode{
		s.root: 0o700, s.runs: 0o700, s.indexPath(): 0o600, s.file(id, ".json"): 0o600,
	} {
		if got := mode(t, path); got != want {
			t.Errorf("%s: mode %v, want %v", filepath.Base(path), got, want)
		}
	}
}

func TestNewIDIsUTCTimePlus128RandomBits(t *testing.T) {
	re := regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{32}$`)
	a, b := mustID(t), mustID(t)
	if !re.MatchString(a) || a == b {
		t.Fatalf("ids %q %q: want distinct matches of %s", a, b, re)
	}
}

// Without index.lock, concurrent read-modify-writes lose updates. Here 64
// goroutines each append 3 distinct notes; every one must survive.
func TestConcurrentUpdatesLoseNothing(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	putRun(t, s, id)
	const workers, each = 64, 3
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				_, err := s.Update(id, func(r *Run) error {
					r.Notes = append(r.Notes, fmt.Sprintf("%d-%d", w, i))
					return nil
				})
				if err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	r, err := s.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, n := range r.Notes {
		seen[n] = true
	}
	if len(seen) != workers*each || len(r.Notes) != workers*each {
		t.Fatalf("%d distinct of %d notes survived, want %d", len(seen), len(r.Notes), workers*each)
	}
}

func TestUpdateWritesNothingOnErrorOrInvalidRecord(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	putRun(t, s, id)
	boom := errors.New("boom")
	if _, err := s.Update(id, func(r *Run) error { r.Key = "changed"; return boom }); err != boom {
		t.Fatalf("fn error: %v", err)
	}
	// done without a published answer is invalid and must never be written.
	_, err := s.Update(id, func(r *Run) error { r.State, r.EndedAt = StateDone, ptr(t0); return nil })
	if !errors.Is(err, CodeCorruptState) {
		t.Fatalf("invalid record: %v, want CORRUPT_STATE", err)
	}
	if r, err := s.Read(id); err != nil || r.Key != "k1" || r.State != StateStarting {
		t.Fatalf("record changed: %+v, %v", r, err)
	}
	if _, err := s.Update(mustID(t), func(*Run) error { return nil }); !errors.Is(err, CodeNotFound) {
		t.Fatalf("missing record: %v, want NOT_FOUND", err)
	}
}

func TestListReportsUnreadableRecordsWithoutFailing(t *testing.T) {
	s := newStore(t)
	good, bad := mustID(t), mustID(t)
	putRun(t, s, good)
	if err := os.WriteFile(s.file(bad, ".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, badRecs, err := s.List()
	if err != nil || len(runs) != 1 || runs[0].ID != good {
		t.Fatalf("List: %d runs, %v", len(runs), err)
	}
	if len(badRecs) != 1 || !errors.Is(badRecs[bad], CodeCorruptState) {
		t.Fatalf("bad records: %v", badRecs)
	}
}

func TestRemoveRefusesLiveAndDeletesEverythingElse(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	lock, err := s.CreateRunLock(id)
	if err != nil {
		t.Fatal(err)
	}
	putRun(t, s, id)
	for _, name := range []string{id + ".txt", id + ".answer.tmp", id + ".json.tmp-1"} {
		if err := os.WriteFile(filepath.Join(s.runs, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Remove(id); !errors.Is(err, CodeBusy) {
		t.Fatalf("Remove while live: %v, want BUSY", err)
	}
	if _, err := s.Read(id); err != nil {
		t.Fatalf("live run was touched: %v", err)
	}
	lock.Close()
	if err := s.Remove(id); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(s.runs, id+"*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	if err := s.Remove(id); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

// A removal cut short after its first unlink must never leave a done
// record whose answer is gone: the record goes first, so readers see the
// run as not found rather than done with a missing answer.
func TestRemoveCutShortNeverLeavesDoneWithoutAnswer(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	r := minimalRun()
	r.ID, r.State, r.Execution, r.EndedAt = id, StateDone, ExecExited, ptr(t0)
	r.Exit, r.Result = &Exit{Code: ptr(0)}, Result{Available: true, Bytes: 5}
	if err := s.WithIndexLock(func() error { return s.Put(r) }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.file(id, ".txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed := 0
	s.fault = func(op faultOp, path string) error {
		if op != opRemove {
			return nil
		}
		if removed++; removed > 1 {
			return syscall.EIO // the process dies after its first unlink
		}
		return nil
	}
	if err := s.Remove(id); !errors.Is(err, CodeStoreIO) {
		t.Fatalf("Remove cut short: %v, want STORE_IO", err)
	}
	if got, err := s.Read(id); err == nil {
		t.Fatalf("record %s survived as %s while its answer may be gone", id, got.State)
	}
	s.fault = nil
	if err := s.Remove(id); err != nil {
		t.Fatalf("finishing the removal: %v", err)
	}
	if _, err := os.Stat(s.file(id, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock left after removal: %v", err)
	}
}

func TestRunIDsCannotEscapeRunsDir(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad := []string{"", ".", "..", "../x", "a/b", "/etc/passwd", "x.json", "x\x00y", "runé", strings.Repeat("a", 129)}
	for _, id := range bad {
		if err := checkID(id); !errors.Is(err, CodeInvalidInput) {
			t.Errorf("checkID(%q) = %v, want INVALID_INPUT", id, err)
		}
		if _, err := s.Read(id); !errors.Is(err, CodeInvalidInput) {
			t.Errorf("Read(%q) = %v, want INVALID_INPUT", id, err)
		}
	}
	good, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkID(good); err != nil {
		t.Errorf("checkID(NewID()) = %v", err)
	}
}
