package run

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/alex2481kobe/askother/internal/worker"
)

var allOps = []faultOp{opWrite, opSync, opClose, opRename, opDirSync}

// failOnce makes the first op on a path ending in suffix fail with EIO.
func failOnce(s *Store, op faultOp, suffix string) {
	fired := false
	s.fault = func(o faultOp, path string) error {
		if !fired && o == op && strings.HasSuffix(path, suffix) {
			fired = true
			return syscall.EIO
		}
		return nil
	}
}

func temps(t *testing.T, s *Store) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(s.runs, "*tmp*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Only a directory-sync failure is "visible, not durable", and nothing is
// rolled back once the rename was attempted.
func TestWriteAtomicSeparatesNotVisibleFromNotDurable(t *testing.T) {
	for _, op := range allOps {
		t.Run(string(op), func(t *testing.T) {
			s := newStore(t)
			path := filepath.Join(s.runs, "x.json")
			if err := s.writeAtomic(path, []byte("old")); err != nil {
				t.Fatal(err)
			}
			failOnce(s, op, ".json")
			err := s.writeAtomic(path, []byte("new content"))
			if !errors.Is(err, CodeStoreIO) || !errors.Is(err, syscall.EIO) {
				t.Fatalf("err %v, want STORE_IO wrapping EIO", err)
			}
			got, _ := os.ReadFile(path)
			visible := op == opDirSync
			if Visible(err) != visible {
				t.Fatalf("Visible(%v) = %v, want %v", err, Visible(err), visible)
			}
			if want := map[bool]string{true: "new content", false: "old"}[visible]; string(got) != want {
				t.Fatalf("content %q, want %q", got, want)
			}
			// before the rename the temp is cleaned; after the attempt it stays
			if left := temps(t, s); (len(left) == 1) != (op == opRename) || len(left) > 1 {
				t.Fatalf("temps after %s fault: %v", op, left)
			}
		})
	}
}

// publish plays the supervisor's publication for a successful worker:
// publish the answer, then commit done, or failed if the answer did not
// publish cleanly.
func publish(s *Store, id string, answer []byte) error {
	tmp := s.AnswerTempPath(id)
	if err := os.WriteFile(tmp, answer, 0o644); err != nil {
		return err
	}
	n, perr := s.PublishAnswer(id, tmp)
	_, err := s.Update(id, func(r *Run) error {
		r.Execution, r.Exit, r.EndedAt = ExecExited, &Exit{Code: ptr(0)}, ptr(t0)
		if perr != nil {
			r.State, r.Error = StateFailed, &worker.Failure{Code: string(CodeStoreIO), Message: perr.Error()}
			return nil
		}
		r.State, r.Result = StateDone, Result{Available: true, Bytes: n}
		return nil
	})
	return err
}

// No fault at any publication step may yield a readable done whose answer
// is missing or partial.
func TestPublishFaultsNeverYieldDoneWithoutFullAnswer(t *testing.T) {
	answer := bytes.Repeat([]byte("answer line\n"), 6000)
	for _, target := range []string{".txt", ".json"} {
		for _, op := range allOps {
			if target == ".txt" && op == opWrite {
				continue // the worker wrote the temp; publication starts at sync
			}
			t.Run(target+"/"+string(op), func(t *testing.T) {
				s := newStore(t)
				id := mustID(t)
				putRun(t, s, id)
				failOnce(s, op, target)
				err := publish(s, id, answer)
				r, rerr := s.Read(id) // Read applies Validate
				if rerr != nil {
					t.Fatalf("record unreadable: %v", rerr)
				}
				wantDone := target == ".json" && op == opDirSync
				if (r.State == StateDone) != wantDone {
					t.Fatalf("state %s (publish err %v), want done=%v", r.State, err, wantDone)
				}
				if target == ".txt" && r.State != StateFailed {
					t.Fatalf("answer fault gave state %s, want failed", r.State)
				}
				if r.State != StateDone {
					return
				}
				f, err := s.OpenAnswer(id, r.Result.Bytes)
				if err != nil {
					t.Fatalf("done but OpenAnswer: %v", err)
				}
				defer f.Close()
				got, _ := io.ReadAll(f)
				if !bytes.Equal(got, answer) {
					t.Fatalf("done with a %d-byte answer, want the %d-byte original", len(got), len(answer))
				}
			})
		}
	}
}

func TestPublishAnswerSizesAndMakesPrivate(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	if err := os.WriteFile(s.AnswerTempPath(id), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := s.PublishAnswer(id, s.AnswerTempPath(id))
	if err != nil || n != 5 {
		t.Fatalf("PublishAnswer: %d, %v", n, err)
	}
	if got := mode(t, s.file(id, ".txt")); got != 0o600 {
		t.Errorf("answer mode %v, want 0600", got)
	}
	if left := temps(t, s); len(left) != 0 {
		t.Errorf("temps left: %v", left)
	}
	outside := filepath.Join(t.TempDir(), "a")
	os.WriteFile(outside, nil, 0o600)
	if _, err := s.PublishAnswer(id, outside); !errors.Is(err, CodeInvalidInput) {
		t.Errorf("temp outside runs/: %v, want INVALID_INPUT", err)
	}
}

func TestOpenAnswerChecksSizeAndPresence(t *testing.T) {
	s := newStore(t)
	id := mustID(t)
	putRun(t, s, id)
	if err := os.WriteFile(s.file(id, ".txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := s.OpenAnswer(id, 5)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Truncate(s.file(id, ".txt"), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenAnswer(id, 5); !errors.Is(err, CodeCorruptState) {
		t.Fatalf("truncated answer: %v, want CORRUPT_STATE", err)
	}
	os.Remove(s.file(id, ".txt"))
	if _, err := s.OpenAnswer(id, 5); !errors.Is(err, CodeCorruptState) {
		t.Fatalf("missing answer beside its record: %v, want CORRUPT_STATE", err)
	}
	os.Remove(s.file(id, ".json"))
	if _, err := s.OpenAnswer(id, 5); !errors.Is(err, CodeNotFound) {
		t.Fatalf("run removed by retention: %v, want NOT_FOUND", err)
	}
}
