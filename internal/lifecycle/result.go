package lifecycle

import (
	"errors"
	"io"
	"unicode/utf8"

	"github.com/alex2481kobe/orca/internal/run"
	"github.com/alex2481kobe/orca/internal/worker"
)

// Result page sizes in bytes.
const (
	DefaultResultLimit = 16 << 10
	MaxResultLimit     = 64 << 10
)

// ResultPage is one page of a run's answer.
type ResultPage struct {
	ID                             string
	State                          run.State
	Available                      bool
	Text                           string
	Offset, NextOffset, TotalBytes int64
	EOF                            bool
	Error                          *worker.Failure
}

// Result serves the answer of a done run from byte offset, at most limit
// bytes (0 means DefaultResultLimit; more than MaxResultLimit is capped).
// offset must lie on a code-point boundary, and the page end is moved back
// so no code point is split. A run that is not done has Available false and
// carries its error. The state comes from Observe, so a dead run reads
// interrupted, never running. Serving a run that has ended records
// first_read_at once, best effort: it only starts the shorter retention
// clock, so a failed write never fails the page.
func Result(d Deps, id string, offset, limit int64) (ResultPage, error) {
	switch {
	case offset < 0:
		return ResultPage{}, run.Errorf(run.CodeInvalidInput, "offset must be 0 or more")
	case limit == 0:
		limit = DefaultResultLimit
	case limit < utf8.UTFMax:
		// A smaller page could not always hold one whole code point.
		return ResultPage{}, run.Errorf(run.CodeInvalidInput, "limit must be at least %d bytes", utf8.UTFMax)
	case limit > MaxResultLimit:
		limit = MaxResultLimit
	}
	r, err := Observe(d, id)
	if err != nil {
		return ResultPage{}, err
	}
	p := ResultPage{ID: r.ID, State: r.State, Offset: offset, NextOffset: offset, Error: r.Error}
	if r.State == run.StateDone {
		if err := readPage(d, r, &p, limit); err != nil {
			return ResultPage{}, err
		}
	}
	if run.IsTerminal(r.State) && r.FirstReadAt == nil {
		markRead(d, r.ID)
	}
	return p, nil
}

// readPage fills p from runs/<id>.txt. OpenAnswer checks the size against
// the record. The sweep removes the record before the answer, so an answer
// that vanished after the record was read reads NOT_FOUND.
func readPage(d Deps, r *run.Run, p *ResultPage, limit int64) error {
	f, err := d.Store.OpenAnswer(r.ID, r.Result.Bytes)
	if err != nil {
		return err
	}
	defer f.Close()
	total := r.Result.Bytes
	if p.Offset > total {
		return run.Errorf(run.CodeInvalidInput, "offset %d is past the end of the %d-byte answer", p.Offset, total)
	}
	// One byte past the page shows whether the end splits a code point.
	buf := make([]byte, min(limit+1, total-p.Offset))
	if _, err := f.ReadAt(buf, p.Offset); err != nil && !errors.Is(err, io.EOF) {
		return run.Errorf(run.CodeStoreIO, "read answer %s: %v", r.ID, err)
	}
	if len(buf) > 0 && !utf8.RuneStart(buf[0]) {
		return run.Errorf(run.CodeInvalidInput, "offset %d is inside a UTF-8 code point; use a next_offset", p.Offset)
	}
	end := int64(len(buf))
	if end > limit {
		end = limit
		for back := 0; back < utf8.UTFMax-1 && end > 0 && !utf8.RuneStart(buf[end]); back++ {
			end--
		}
	}
	p.Available = true
	p.TotalBytes = total
	p.Text = string(buf[:end])
	p.NextOffset = p.Offset + end
	p.EOF = p.NextOffset == total
	return nil
}

// markRead sets first_read_at if it is still unset. Two readers can both
// see it unset; the one that writes second keeps the first timestamp.
func markRead(d Deps, id string) {
	now := d.now()
	_, _ = d.Store.Update(id, func(r *run.Run) error {
		if r.FirstReadAt != nil {
			return errAlreadyRead
		}
		r.FirstReadAt = &now
		return nil
	})
}

var errAlreadyRead = errors.New("already read")
