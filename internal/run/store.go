package run

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Store is the run file tree under one root directory: index.lock, and in
// runs/ each run's <id>.json record, <id>.txt answer and <id>.lock run lock.
// Resolving the root (ASKOTHER_HOME or the default) is the caller's job.
type Store struct {
	root, runs string
	fault      func(op faultOp, path string) error // tests only: fail a publication or removal step
}

// Open returns the store at root, creating root and root/runs with mode 0700.
func Open(root string) (*Store, error) {
	root = filepath.Clean(root)
	runs := filepath.Join(root, "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return nil, Errorf(CodeStoreIO, "create %s: %v", filepath.Base(runs), err)
	}
	return &Store{root: root, runs: runs}, nil
}

func (s *Store) indexPath() string          { return filepath.Join(s.root, "index.lock") }
func (s *Store) file(id, ext string) string { return filepath.Join(s.runs, id+ext) }

// AnswerTempPath is the same-directory temp path a worker writes its answer
// to before PublishAnswer. It is not created in advance: Codex must create
// it itself.
func (s *Store) AnswerTempPath(id string) string { return s.file(id, ".answer.tmp") }

// NewID returns a run id: UTC time to the second, a dash, then 128 random
// bits in hex. CreateRunLock (O_EXCL) is what reserves it.
func NewID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", Errorf(CodeStoreIO, "random id: %v", err)
	}
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b), nil
}

// validID is the only shape a run id may have: 1 to 128 letters, digits and
// '-'. No '/' or '.', so an id can never name a path outside runs/.
var validID = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

// checkID rejects anything that could escape runs/ or match unrelated files.
func checkID(id string) error {
	if !validID.MatchString(id) {
		return Errorf(CodeInvalidInput, "run id %q must be 1 to 128 letters, digits or '-'", id)
	}
	return nil
}

// Read returns the validated record runs/<id>.json. Records are replaced
// atomically, so a read needs no lock.
func (s *Store) Read(id string) (*Run, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.file(id, ".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, Errorf(CodeNotFound, "run %s not found", id)
	}
	if err != nil {
		return nil, Errorf(CodeStoreIO, "read run %s: %v", id, err)
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, Errorf(CodeCorruptState, "run %s: %v", id, err)
	}
	if r.ID != id {
		return nil, Errorf(CodeCorruptState, "run %s: record has id %q", id, r.ID)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// Put validates r and atomically replaces its record. The caller must hold
// the index lock (WithIndexLock); outside the launcher, use Update. A write
// error is a StoreError: check Visible before assuming the old record stands.
func (s *Store) Put(r *Run) error {
	if err := checkID(r.ID); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return Errorf(CodeCorruptState, "encode run %s: %v", r.ID, err)
	}
	return s.writeAtomic(s.file(r.ID, ".json"), append(data, '\n'))
}

// Update is the only read-modify-write path for a record: take the index
// lock, read the record fresh, apply fn, validate, write atomically. If fn
// returns an error nothing is written. It returns the record as written,
// also with a Visible StoreError (readers already see it); otherwise nil.
func (s *Store) Update(id string, fn func(*Run) error) (*Run, error) {
	var out *Run
	err := s.WithIndexLock(func() error {
		r, err := s.Read(id)
		if err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
		if r.ID != id {
			return Errorf(CodeInvalidInput, "update of run %s changed its id", id)
		}
		err = s.Put(r)
		if err == nil || Visible(err) {
			out = r
		}
		return err
	})
	return out, err
}

// ids lists the ids of all records in id (creation time) order.
func (s *Store) ids() ([]string, error) {
	entries, err := os.ReadDir(s.runs)
	if err != nil {
		return nil, Errorf(CodeStoreIO, "list runs: %v", err)
	}
	var ids []string
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if ok && checkID(id) == nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// List reads every record. A record that cannot be read or fails validation
// is reported in bad (by id) and does not stop the listing; err is set only
// when the directory itself cannot be read.
func (s *Store) List() (runs []*Run, bad map[string]error, err error) {
	ids, err := s.ids()
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		r, err := s.Read(id)
		switch {
		case err == nil:
			runs = append(runs, r)
		case errors.Is(err, CodeNotFound):
			// removed since the directory was listed
		default:
			if bad == nil {
				bad = map[string]error{}
			}
			bad[id] = err
		}
	}
	return runs, bad, nil
}

// Remove deletes a run's files for the retention sweep, which decides
// eligibility and holds the index lock. It refuses with BUSY while a
// supervisor holds the run lock. The record goes first, so a removal cut
// short never leaves a done record without its answer; then the answer,
// leftover temps, and the lock last. Missing files are not an error.
func (s *Store) Remove(id string) error {
	live, err := s.ProbeLive(id)
	if err != nil {
		return err
	}
	if live {
		return Errorf(CodeBusy, "run %s is live", id)
	}
	temps, err := filepath.Glob(filepath.Join(s.runs, id+".*tmp*"))
	if err != nil {
		return Errorf(CodeStoreIO, "list temps of %s: %v", id, err)
	}
	paths := append([]string{s.file(id, ".json"), s.file(id, ".txt")}, temps...)
	for _, p := range append(paths, s.file(id, ".lock")) {
		err := s.step(opRemove, p, func() error { return os.Remove(p) })
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Errorf(CodeStoreIO, "remove %s: %v", filepath.Base(p), err)
		}
	}
	return nil
}
