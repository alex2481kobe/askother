package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// WaitBudget is the server-side cap on one wait call: below the tool-call
// timeouts of the MCP clients AskOther serves, since a client may not cancel.
const WaitBudget = 50 * time.Second

// shutdownGrace is how long shutdown waits for in-flight responses after
// cancelling their contexts.
const shutdownGrace = 2 * time.Second

// ClientInfo is initialize's clientInfo. Name selects the caller identity
// source ("claude-code", "codex-mcp-client").
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Handler runs one tools/call. A *run.Error result becomes a tool error
// (isError); any other error becomes a JSON-RPC internal error.
type Handler interface {
	Call(ctx context.Context, name string, args, meta json.RawMessage, client ClientInfo) (any, error)
}

// Config wires a Server. Out carries protocol only; Log gets diagnostics.
type Config struct {
	In      io.Reader
	Out     io.Writer
	Log     io.Writer
	Version string
	Tools   func() []Tool
	Handler Handler
}

// Server is a newline-delimited JSON-RPC 2.0 MCP server over one stream pair.
type Server struct {
	in      *bufio.Reader
	out     io.Writer
	log     *log.Logger
	version string
	tools   []Tool
	known   map[string]bool
	handler Handler

	writeMu sync.Mutex
	closed  atomic.Bool // set once Serve returns; later writes are dropped

	mu       sync.Mutex
	stopping bool
	client   ClientInfo
	inflight map[string]*pending
	wg       sync.WaitGroup
}

type pending struct {
	cancel   context.CancelFunc
	byClient atomic.Bool
}

// NewServer builds a Server. Tools is called once.
func NewServer(c Config) *Server {
	logw := c.Log
	if logw == nil {
		logw = io.Discard
	}
	tools := c.Tools()
	known := make(map[string]bool, len(tools))
	for _, t := range tools {
		known[t.Name] = true
	}
	return &Server{
		in: bufio.NewReader(c.In), out: c.Out, log: log.New(logw, "askother mcp: ", log.LstdFlags),
		version: c.Version, tools: tools, known: known, handler: c.Handler,
		inflight: map[string]*pending{},
	}
}

// Serve reads requests until stdin EOF or ctx is done, then
// stops reading, cancels in-flight requests, waits up to shutdownGrace for
// their responses and returns. It returns nil on a clean shutdown and the
// read error otherwise.
func (s *Server) Serve(ctx context.Context) error {
	base, cancel := context.WithCancel(ctx)
	defer cancel()
	readDone := make(chan error, 1)
	go func() { readDone <- s.readLoop(base) }()

	var err error
	select {
	case err = <-readDone:
	case <-ctx.Done():
	}
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	cancel()

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		s.log.Printf("shutdown: abandoning in-flight requests after %s", shutdownGrace)
	}
	s.closed.Store(true)
	return err
}

func (s *Server) readLoop(ctx context.Context) error {
	for {
		line, err := s.in.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			s.dispatch(ctx, line)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			s.log.Printf("read: %v", err)
			return err
		}
	}
}

// start registers a request goroutine unless shutdown has begun.
func (s *Server) start() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	s.wg.Add(1)
	return true
}

// write sends one message as a single line. The mutex keeps concurrent
// responses from interleaving: stdout carries protocol only.
func (s *Server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		s.log.Printf("marshal response: %v", err)
		return
	}
	b = append(b, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return
	}
	if _, err := s.out.Write(b); err != nil {
		s.log.Printf("write: %v", err)
	}
}

// SignalContext returns a context cancelled by SIGINT (Claude) or SIGTERM
// (Codex), which is how each client stops its server. SIGPIPE is ignored
// so writing to a closed stdout fails with EPIPE instead of killing the
// process.
func SignalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	signal.Ignore(syscall.SIGPIPE)
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
}
