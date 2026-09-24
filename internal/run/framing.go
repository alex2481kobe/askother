package run

import (
	"bufio"
	"bytes"
	"io"
	"unicode/utf8"
)

// MaxLine is the default per-line cap on worker stdout. Real answers have
// reached 83 KB in one line, so the cap is far above that.
const MaxLine = 64 << 20

// stderrTailMax is the size of the kept stderr tail.
const stderrTailMax = 8 << 10

// Lines calls fn with each '\n'-terminated line of r, without the newline.
// A final line with no newline is delivered too, as is a partial line left
// when a read fails (the drain deadline), before that error is returned.
// Empty lines are skipped. fn must not keep line after it returns.
//
// A line longer than max bytes (MaxLine when max <= 0) stops reading with a
// PROTOCOL error; the caller must keep draining r so the worker never
// blocks on a full pipe. Lines stops at fn's first error and returns it, and
// returns nil at EOF.
//
// It reads with bufio.Reader.ReadSlice and grows its own buffer, so memory
// stays bounded by the cap; bufio.Scanner's 64 KiB default would lose a long
// terminal event.
func Lines(r io.Reader, max int, fn func(line []byte) error) error {
	if max <= 0 {
		max = MaxLine
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	for {
		frag, err := br.ReadSlice('\n')
		line = append(line, frag...)
		content := bytes.TrimSuffix(line, []byte{'\n'})
		if len(content) > max {
			return Errorf(CodeProtocol, "a worker stdout line exceeds %d bytes", max)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if len(content) > 0 {
			if ferr := fn(content); ferr != nil {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		line = line[:0]
	}
}

// tail keeps the last stderrTailMax bytes written to it.
type tail struct {
	buf   []byte
	total int64
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	t.total += int64(n)
	if len(p) > stderrTailMax {
		t.buf = t.buf[:0]
		p = p[len(p)-stderrTailMax:]
	}
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*stderrTailMax {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-stderrTailMax:]...)
	}
	return n, nil
}

// result is the tail cut on a UTF-8 boundary. Truncated
// means bytes were dropped from the front.
func (t *tail) result() StderrTail {
	b := t.buf
	if len(b) > stderrTailMax {
		b = b[len(b)-stderrTailMax:]
	}
	if int64(len(b)) < t.total {
		for i := 0; i < utf8.UTFMax-1 && len(b) > 0 && !utf8.RuneStart(b[0]); i++ {
			b = b[1:]
		}
	}
	return StderrTail{Text: string(b), Truncated: int64(len(b)) < t.total}
}
