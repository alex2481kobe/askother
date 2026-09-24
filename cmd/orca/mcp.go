package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/alex2481kobe/orca/internal/mcp"
	"github.com/alex2481kobe/orca/internal/tools"
)

// cmdMCP is `orca mcp`: the stdio MCP server a harness starts. It exits 0
// on stdin EOF, SIGINT or SIGTERM; supervisors and runs are unaffected.
func cmdMCP(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "orca: mcp takes no arguments\n\n%s", usage)
		return exitRefused
	}
	d, err := mcpDeps(os.Environ(), userHome())
	if err != nil {
		return setupFailed(stderr, err)
	}
	ctx, stop := mcp.SignalContext(context.Background())
	defer stop()

	// stdout carries protocol only. Anything else that writes to os.Stdout
	// lands on stderr instead.
	protocol := os.Stdout
	os.Stdout = os.Stderr

	h := tools.New(d, instanceID())
	srv := mcp.NewServer(mcp.Config{
		In: os.Stdin, Out: protocol, Log: stderr,
		Version: version(), Tools: h.Tools, Handler: h,
	})
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintf(stderr, "orca mcp: %v\n", err)
		return exitInternal
	}
	return 0
}

// instanceID is the fallback caller id for clients that name no session:
// opaque and unique per process.
func instanceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return "orca-mcp-" + hex.EncodeToString(b)
}
