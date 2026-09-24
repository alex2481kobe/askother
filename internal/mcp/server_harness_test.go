package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/alex2481kobe/orca/internal/run"
)

// fakeHandler: "slow" waits up to 5 s (or release/ctx), "stubborn" ignores
// ctx, "panic" panics, "fail" returns a tool error, anything else echoes.
type fakeHandler struct {
	release   chan struct{}
	cancelled chan struct{}
}

func newFakeHandler() *fakeHandler {
	return &fakeHandler{release: make(chan struct{}), cancelled: make(chan struct{}, 8)}
}

func (h *fakeHandler) Call(ctx context.Context, name string, args, meta json.RawMessage, c ClientInfo) (any, error) {
	switch name {
	case "slow":
		select {
		case <-h.release:
			return map[string]any{"slept": true}, nil
		case <-time.After(5 * time.Second):
			return map[string]any{"slept": true}, nil
		case <-ctx.Done():
			h.cancelled <- struct{}{}
			return map[string]any{"cancelled": true}, nil
		}
	case "stubborn":
		<-h.release
		return map[string]any{"late": true}, nil
	case "panic":
		panic("boom")
	case "fail":
		return nil, run.Errorf(run.CodeNotFound, "no run %q", "r1")
	}
	return map[string]any{"client": c.Name, "args": args, "meta": meta}, nil
}

func testTools() []Tool {
	var ts []Tool
	for _, n := range []string{"echo", "slow", "stubborn", "panic", "fail"} {
		ts = append(ts, Tool{Name: n, InputSchema: object(nil, map[string]any{})})
	}
	return ts
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// choppyWriter splits each Write into small pieces, like a pipe write larger
// than PIPE_BUF, so unserialized concurrent writes would interleave.
type choppyWriter struct{ w io.Writer }

func (c choppyWriter) Write(p []byte) (int, error) {
	for i := 0; i < len(p); i += 64 {
		if _, err := c.w.Write(p[i:min(i+64, len(p))]); err != nil {
			return i, err
		}
		runtime.Gosched()
	}
	return len(p), nil
}

type harness struct {
	t      *testing.T
	h      *fakeHandler
	in     *io.PipeWriter
	lines  chan []byte
	done   chan error
	cancel context.CancelFunc
	log    *lockedBuf
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	x := &harness{t: t, h: newFakeHandler(), in: inW, lines: make(chan []byte, 1024), done: make(chan error, 1), log: &lockedBuf{}}
	srv := NewServer(Config{In: inR, Out: choppyWriter{outW}, Log: x.log, Version: "0.0.0-test", Tools: testTools, Handler: x.h})
	ctx, cancel := context.WithCancel(context.Background())
	x.cancel = cancel
	go func() { x.done <- srv.Serve(ctx) }()
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			x.lines <- bytes.Clone(sc.Bytes())
		}
		close(x.lines)
	}()
	t.Cleanup(func() {
		cancel()
		inW.Close()
		select {
		case <-x.done:
		case <-time.After(shutdownGrace + 2*time.Second):
			t.Error("Serve did not return")
		}
		outW.Close()
	})
	return x
}

func (x *harness) send(format string, args ...any) {
	x.t.Helper()
	if _, err := fmt.Fprintf(x.in, format+"\n", args...); err != nil {
		x.t.Fatalf("send: %v", err)
	}
}

type wireResp struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (x *harness) recv() wireResp {
	x.t.Helper()
	select {
	case line, ok := <-x.lines:
		if !ok {
			x.t.Fatal("stdout closed")
		}
		var r wireResp
		if err := json.Unmarshal(line, &r); err != nil {
			x.t.Fatalf("bad line %q: %v", line, err)
		}
		return r
	case <-time.After(2 * time.Second):
		x.t.Fatal("no response within 2s")
	}
	return wireResp{}
}

func (x *harness) expectSilence(d time.Duration) {
	x.t.Helper()
	select {
	case line := <-x.lines:
		x.t.Fatalf("unexpected output %s", line)
	case <-time.After(d):
	}
}

func (x *harness) initialize(version string) map[string]any {
	x.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":%q,"clientInfo":{"name":"codex-mcp-client","version":"1.0"}}}`, version)
	r := x.recv()
	var m map[string]any
	json.Unmarshal(r.Result, &m)
	return m
}
