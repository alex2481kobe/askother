package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess is the stdio server run by TestSubprocessSignalShutdown.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ASKOTHER_MCP_TEST_HELPER") != "1" {
		return
	}
	ctx, stop := SignalContext(context.Background())
	defer stop()
	srv := NewServer(Config{In: os.Stdin, Out: os.Stdout, Log: os.Stderr, Version: "0.0.0-test", Tools: testTools, Handler: newFakeHandler()})
	if err := srv.Serve(ctx); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestSubprocessSignalShutdown(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
			cmd.Env = append(os.Environ(), "ASKOTHER_MCP_TEST_HELPER=1")
			stdin, _ := cmd.StdinPipe()
			stdout, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
			sc := bufio.NewScanner(stdout)
			next := func() wireResp {
				t.Helper()
				if !sc.Scan() {
					t.Fatalf("stdout ended: %v", sc.Err())
				}
				var r wireResp
				if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
					t.Fatalf("non-JSON stdout line %q", sc.Bytes())
				}
				return r
			}
			fmt.Fprintln(stdin, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"codex-mcp-client"}}}`)
			if r := next(); !strings.Contains(string(r.Result), `"2025-06-18"`) {
				t.Fatalf("initialize: %s", r.Result)
			}
			fmt.Fprintln(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
			fmt.Fprintln(stdin, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}`)
			fmt.Fprintln(stdin, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
			if r := next(); string(r.ID) != "2" {
				t.Fatalf("ping blocked behind slow call: got id %s", r.ID)
			}
			start := time.Now()
			cmd.Process.Signal(sig)
			if r := next(); string(r.ID) != "1" || !strings.Contains(string(r.Result), "cancelled") {
				t.Fatalf("in-flight reply: %s %s", r.ID, r.Result)
			}
			if sc.Scan() {
				t.Fatalf("output after shutdown reply: %q", sc.Bytes())
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("exit after %s: %v", sig, err)
			}
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("exit took %s", d)
			}
		})
	}
}

// A client that closes our stdout must not kill the server with SIGPIPE.
func TestSubprocessSurvivesClosedStdout(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "ASKOTHER_MCP_TEST_HELPER=1")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	stdout.Close()
	fmt.Fprintln(stdin, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	time.Sleep(200 * time.Millisecond)
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit after closed stdout: %v", err)
	}
}
