package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"

	"github.com/alex2481kobe/orca/internal/ui"
)

func cmdUI(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "orca: ui takes no arguments")
		return exitRefused
	}
	d, err := stateDeps(os.Environ(), userHome())
	if err != nil {
		return setupFailed(stderr, err)
	}
	ln, server, url, err := ui.Listen(d)
	if err != nil {
		fmt.Fprintf(stderr, "orca ui: %v\n", err)
		return exitInternal
	}
	defer ln.Close()
	fmt.Fprintln(stdout, url)
	openUI(url, stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "orca ui: %v\n", err)
		return exitInternal
	}
	return 0
}

func openUI(url string, stderr io.Writer) {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	cmd := exec.Command(command, url)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "orca ui: open the URL above in your browser (%v)\n", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}
