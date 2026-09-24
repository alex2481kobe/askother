package run

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

func collect(t *testing.T, r io.Reader, max int) ([]string, error) {
	t.Helper()
	var got []string
	err := Lines(r, max, func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	return got, err
}

func TestLinesFinalLineWithoutNewline(t *testing.T) {
	got, err := collect(t, strings.NewReader("a\n\nb\nlast"), 0)
	if err != nil || strings.Join(got, "|") != "a|b|last" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Split UTF-8 across reads is reassembled byte-exact.
func TestLinesOneByteReads(t *testing.T) {
	want := []string{"\u4e2d\u6587\U0001F600 one", strings.Repeat("\u732b\U0001F41F", 5000)}
	r := iotest.OneByteReader(strings.NewReader(strings.Join(want, "\n") + "\n"))
	got, err := collect(t, r, 0)
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("lines differ (%d lines, %v)", len(got), err)
	}
}

// Lines longer than bufio's 64 KiB buffer still arrive whole; the cap is on
// the content, so a line of exactly max bytes passes and max+1 fails.
func TestLinesCap(t *testing.T) {
	const max = 200 << 10
	ok := strings.Repeat("x", max)
	got, err := collect(t, strings.NewReader(ok+"\nnext\n"), max)
	if err != nil || len(got) != 2 || got[0] != ok {
		t.Fatalf("exact-cap line: %d lines, %v", len(got), err)
	}
	got, err = collect(t, strings.NewReader("first\n"+ok+"y\n"), max)
	if !errors.Is(err, CodeProtocol) || len(got) != 1 {
		t.Fatalf("over-cap line: %d lines, err %v", len(got), err)
	}
	if _, err = collect(t, strings.NewReader(ok+"y"), max); !errors.Is(err, CodeProtocol) {
		t.Fatalf("over-cap final line: %v", err)
	}
}

// A partial line is delivered before a read error such as the drain
// deadline, so a final event cut off by the bound is not lost.
func TestLinesPartialBeforeError(t *testing.T) {
	boom := errors.New("deadline")
	r := io.MultiReader(strings.NewReader("one\ntwo"), iotest.ErrReader(boom))
	got, err := collect(t, r, 0)
	if !errors.Is(err, boom) || strings.Join(got, "|") != "one|two" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestLinesStopsAtCallbackError(t *testing.T) {
	stop := errors.New("protocol")
	n := 0
	err := Lines(strings.NewReader("a\nb\nc\n"), 0, func([]byte) error { n++; return stop })
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestTailUTF8Boundary(t *testing.T) {
	var all bytes.Buffer
	var tl tail
	w := io.MultiWriter(&all, &tl)
	io.WriteString(w, "a") // shifts the 8 KiB cut into the middle of a rune
	for range 3000 {
		io.WriteString(w, "\u00e9\u732b\U0001F41F")
	}
	got := tl.result()
	if !got.Truncated || len(got.Text) > stderrTailMax || len(got.Text) < stderrTailMax-3 {
		t.Fatalf("len %d truncated %v", len(got.Text), got.Truncated)
	}
	if !utf8.ValidString(got.Text) || !strings.HasSuffix(all.String(), got.Text) {
		t.Fatal("tail is not a valid UTF-8 suffix of the input")
	}
}

func TestTailSmallAndHugeWrites(t *testing.T) {
	var tl tail
	tl.Write([]byte("short"))
	if got := tl.result(); got.Text != "short" || got.Truncated {
		t.Fatalf("small: %+v", got)
	}
	big := strings.Repeat("0123456789", 5000) + "end"
	if n, _ := tl.Write([]byte(big)); n != len(big) {
		t.Fatalf("Write returned %d, want %d", n, len(big))
	}
	got := tl.result()
	if !got.Truncated || len(got.Text) != stderrTailMax || !strings.HasSuffix(big, got.Text) {
		t.Fatalf("huge: len %d truncated %v", len(got.Text), got.Truncated)
	}
}
