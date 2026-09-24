package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// mcpClient drives one `orca mcp` process over real pipes, one request at
// a time.
type mcpClient struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan []byte
	stderr *lockedBuffer
	nextID int
	exited chan struct{}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startMCP starts `orca mcp` with extra env entries and initializes it as
// the named client.
func (e *env) startMCP(clientName string, extraEnv ...string) *mcpClient {
	e.t.Helper()
	cmd := exec.Command(orcaBin, "mcp")
	cmd.Env = append(append([]string{}, e.base...), extraEnv...)
	c := &mcpClient{t: e.t, cmd: cmd, lines: make(chan []byte, 16), stderr: &lockedBuffer{}, exited: make(chan struct{})}
	cmd.Stderr = c.stderr
	var err error
	c.in, err = cmd.StdinPipe()
	must(e.t, err)
	out, err := cmd.StdoutPipe()
	must(e.t, err)
	must(e.t, cmd.Start())
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(nil, 16<<20)
		for sc.Scan() {
			c.lines <- bytes.Clone(sc.Bytes())
		}
		close(c.lines)
	}()
	go func() { _ = cmd.Wait(); close(c.exited) }()
	e.t.Cleanup(func() { c.kill() })

	res := c.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"clientInfo":      map[string]string{"name": clientName, "version": "0.0.0-e2e"},
		"capabilities":    map[string]any{},
	})
	var init struct {
		ServerInfo struct{ Name string } `json:"serverInfo"`
	}
	if json.Unmarshal(res, &init) != nil || init.ServerInfo.Name != "orca" {
		e.t.Fatalf("initialize: %s", res)
	}
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return c
}

func (c *mcpClient) send(msg any) {
	c.t.Helper()
	b, err := json.Marshal(msg)
	must(c.t, err)
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Fatalf("write to orca mcp: %v (stderr: %s)", err, c.stderr)
	}
}

// request sends one JSON-RPC request and returns its result; a JSON-RPC
// error fails the test.
func (c *mcpClient) request(method string, params any) json.RawMessage {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	timeout := time.After(90 * time.Second)
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				c.t.Fatalf("%s: orca mcp closed stdout (stderr: %s)", method, c.stderr)
			}
			var r struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(line, &r); err != nil {
				c.t.Fatalf("non-JSON on the protocol stream: %q", line)
			}
			if r.ID != id {
				c.t.Fatalf("%s: reply for id %d, want %d: %s", method, r.ID, id, line)
			}
			if r.Error != nil {
				c.t.Fatalf("%s: JSON-RPC error %s", method, r.Error)
			}
			return r.Result
		case <-timeout:
			c.t.Fatalf("%s: no reply (stderr: %s)", method, c.stderr)
		}
	}
}

// tool calls a tool and decodes its structured result into out. It
// returns whether the result is a tool error.
func (c *mcpClient) tool(name string, args, meta any, out any) (isError bool) {
	c.t.Helper()
	params := map[string]any{"name": name, "arguments": args}
	if meta != nil {
		params["_meta"] = meta
	}
	var r struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	raw := c.request("tools/call", params)
	must(c.t, json.Unmarshal(raw, &r))
	if out != nil {
		if err := json.Unmarshal(r.StructuredContent, out); err != nil {
			c.t.Fatalf("%s: decode %s: %v", name, raw, err)
		}
	}
	return r.IsError
}

// mustTool is tool for calls that must succeed.
func (c *mcpClient) mustTool(name string, args, meta any, out any) {
	c.t.Helper()
	var raw json.RawMessage
	if c.tool(name, args, meta, &raw) {
		c.t.Fatalf("%s: tool error %s", name, raw)
	}
	if out != nil {
		must(c.t, json.Unmarshal(raw, out))
	}
}

// close ends stdin and requires a clean exit 0: EOF is a shutdown.
func (c *mcpClient) close() {
	c.t.Helper()
	c.in.Close()
	select {
	case <-c.exited:
	case <-time.After(5 * time.Second):
		c.t.Fatalf("orca mcp did not exit on stdin EOF")
	}
	if code := c.cmd.ProcessState.ExitCode(); code != 0 {
		c.t.Fatalf("orca mcp exit %d after EOF (stderr: %s)", code, c.stderr)
	}
}

func (c *mcpClient) kill() {
	_ = c.cmd.Process.Signal(syscall.SIGKILL)
	<-c.exited
}
