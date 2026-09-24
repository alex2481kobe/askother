package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// emitter writes JSONL events to stdout. Non-ASCII stays raw UTF-8, as both
// CLIs emit it.
type emitter struct {
	w                 io.Writer
	split             bool // cut writes right after every multibyte lead byte
	tolerant          bool // ignore write errors (a broken pipe does not kill us)
	noTrailingNewline bool // the final event is written without '\n'
	cuts              int  // writes that ended mid-codepoint
	writeErrors       int
}

func encode(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// event writes one complete line.
func (e *emitter) event(v any) { e.write(append(encode(v), '\n')) }

// last writes the final event of a run; it honors noTrailingNewline.
func (e *emitter) last(v any) {
	b := encode(v)
	if !e.noTrailingNewline {
		b = append(b, '\n')
	}
	e.write(b)
}

func (e *emitter) write(b []byte) {
	if !e.split {
		e.put(b)
		return
	}
	start := 0
	for i := 1; i < len(b); i++ {
		if b[i-1] < 0xC0 {
			continue
		}
		e.put(b[start:i])
		start = i
		e.cuts++
		if e.cuts <= 64 { // give the reader time to see the split write
			time.Sleep(time.Millisecond)
		}
	}
	e.put(b[start:])
}

func (e *emitter) put(b []byte) {
	if _, err := e.w.Write(b); err != nil {
		e.writeErrors++
		if !e.tolerant {
			fmt.Fprintf(os.Stderr, "fakeworker: write stdout: %v\n", err)
			os.Exit(1)
		}
	}
}

// newUUID returns a random version 4 UUID, the shape of both CLIs' session ids.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// parseDur reads a Go duration ("300ms") or plain seconds ("0.3").
func parseDur(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(f * float64(time.Second))
	}
	return def
}

func parseSize(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

func sleepForever() {
	for {
		time.Sleep(time.Hour)
	}
}
