package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"

	"github.com/alex2481kobe/orca/internal/run"
)

// protocolVersions are the revisions tested with both Claude Code and
// Codex, latest first.
var protocolVersions = []string{"2025-11-25", "2025-06-18"}

// JSON-RPC 2.0 error codes.
const (
	errParse          = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603
)

var nullID = json.RawMessage("null")

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// callResult is a tools/call result. The payload is JSON text content and,
// when it is an object, also structuredContent (2025-06-18 and 2025-11-25:
// structured results SHOULD be mirrored as text for older clients).
type callResult struct {
	Content           []textContent   `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

func (s *Server) dispatch(ctx context.Context, line []byte) {
	if !json.Valid(line) {
		s.write(response{JSONRPC: "2.0", ID: nullID, Error: &rpcError{errParse, "parse error"}})
		return
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		s.write(response{JSONRPC: "2.0", ID: nullID, Error: &rpcError{errInvalidRequest, "request must be a JSON object"}})
		return
	}
	if m.Method == "" {
		s.log.Printf("ignoring message without method (id %s)", m.ID)
		return
	}
	if m.ID == nil {
		s.notification(m)
		return
	}
	if !validID(m.ID) {
		s.write(response{JSONRPC: "2.0", ID: nullID, Error: &rpcError{errInvalidRequest, "id must be a string or number"}})
		return
	}
	if m.JSONRPC != "2.0" {
		s.reply(m.ID, nil, &rpcError{errInvalidRequest, `jsonrpc must be "2.0"`})
		return
	}
	if !s.start() {
		return
	}
	if m.Method == "initialize" {
		// Inline so clientInfo is recorded before any later request runs.
		s.request(ctx, m, nil)
		return
	}
	reqCtx, cancel := context.WithCancel(ctx)
	p := &pending{cancel: cancel}
	key := idKey(m.ID)
	s.mu.Lock()
	s.inflight[key] = p
	s.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			if s.inflight[key] == p {
				delete(s.inflight, key)
			}
			s.mu.Unlock()
		}()
		s.request(reqCtx, m, p)
	}()
}

// request runs one request and replies, recovering from any panic so a
// handler bug costs one reply, never the process.
func (s *Server) request(ctx context.Context, m message, p *pending) {
	defer s.wg.Done()
	var result any
	var rerr *rpcError
	func() {
		defer func() {
			if v := recover(); v != nil {
				s.log.Printf("panic in %s (id %s): %v\n%s", m.Method, m.ID, v, debug.Stack())
				result, rerr = nil, &rpcError{errInternal, fmt.Sprintf("internal error: %v", v)}
			}
		}()
		result, rerr = s.method(ctx, m)
	}()
	if p != nil && p.byClient.Load() {
		return // cancelled by the client: the spec says send no response
	}
	s.reply(m.ID, result, rerr)
}

func (s *Server) reply(id json.RawMessage, result any, rerr *rpcError) {
	s.write(response{JSONRPC: "2.0", ID: id, Result: result, Error: rerr})
}

func (s *Server) method(ctx context.Context, m message) (any, *rpcError) {
	switch m.Method {
	case "initialize":
		return s.initialize(m.Params)
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		return s.toolsCall(ctx, m.Params)
	}
	return nil, &rpcError{errMethodNotFound, "method not found: " + m.Method}
}

func (s *Server) initialize(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ProtocolVersion string     `json:"protocolVersion"`
		ClientInfo      ClientInfo `json:"clientInfo"`
	}
	if err := unmarshalParams(params, &p); err != nil {
		return nil, &rpcError{errInvalidParams, "invalid initialize params: " + err.Error()}
	}
	version := protocolVersions[0]
	if slices.Contains(protocolVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	s.mu.Lock()
	s.client = p.ClientInfo
	s.mu.Unlock()
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "orca", "version": s.version},
	}, nil
}

func (s *Server) toolsCall(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      json.RawMessage `json:"_meta"`
	}
	if err := unmarshalParams(params, &p); err != nil || p.Name == "" {
		return nil, &rpcError{errInvalidParams, "tools/call needs params {name, arguments?}"}
	}
	if !s.known[p.Name] {
		return nil, &rpcError{errInvalidParams, "unknown tool: " + p.Name}
	}
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	v, err := s.handler.Call(ctx, p.Name, p.Arguments, p.Meta, client)
	var toolErr *run.Error
	switch {
	case errors.As(err, &toolErr):
		v = toolErr // tool errors are results the model sees, not JSON-RPC errors
	case err != nil:
		return nil, &rpcError{errInternal, err.Error()}
	}
	payload, merr := json.Marshal(v)
	if merr != nil {
		return nil, &rpcError{errInternal, "encode result: " + merr.Error()}
	}
	r := callResult{Content: []textContent{{Type: "text", Text: string(payload)}}, IsError: toolErr != nil}
	if len(payload) > 0 && payload[0] == '{' {
		r.StructuredContent = payload
	}
	return r, nil
}

func (s *Server) notification(m message) {
	if m.Method != "notifications/cancelled" {
		return // notifications/initialized and unknown ones need nothing
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if unmarshalParams(m.Params, &p) != nil || !validID(p.RequestID) {
		return
	}
	s.mu.Lock()
	pend := s.inflight[idKey(p.RequestID)]
	s.mu.Unlock()
	if pend != nil {
		pend.byClient.Store(true)
		pend.cancel()
	}
}

// unmarshalParams treats absent params as {} and requires an object.
func unmarshalParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] != '{' {
		return errors.New("params must be an object")
	}
	return json.Unmarshal(raw, v)
}

func validID(id json.RawMessage) bool {
	var v any
	if len(id) == 0 || json.Unmarshal(id, &v) != nil {
		return false
	}
	switch v.(type) {
	case string, float64:
		return true
	}
	return false
}

// idKey canonicalizes an id so a cancellation matches however the client
// spelled it; the reply always echoes the original bytes.
func idKey(id json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(id))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return string(id)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
