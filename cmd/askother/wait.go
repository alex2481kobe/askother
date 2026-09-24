package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alex2481kobe/askother/internal/lifecycle"
	"github.com/alex2481kobe/askother/internal/run"
)

// Exit codes that belong to the waiter, not to run outcomes.
const (
	exitTimeout     = 124
	exitInterrupted = 130
)

// waitSlice bounds one lifecycle.Wait call; the loop repeats it until the
// runs end or --timeout expires. A variable so tests can shorten it.
var waitSlice = time.Minute

type waitArgs struct {
	ids     []string
	any     bool
	timeout time.Duration // 0: wait forever
}

// parseWaitArgs accepts flags anywhere: --any, --timeout D, --timeout=D, and
// -- to end flags.
func parseWaitArgs(args []string) (waitArgs, error) {
	var w waitArgs
	flags := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !flags || !strings.HasPrefix(a, "-"):
			w.ids = append(w.ids, a)
		case a == "--":
			flags = false
		case a == "--any":
			w.any = true
		case a == "--timeout" || strings.HasPrefix(a, "--timeout="):
			v, ok := strings.CutPrefix(a, "--timeout=")
			if !ok {
				if i++; i == len(args) {
					return w, errors.New("--timeout needs a duration, e.g. 10m")
				}
				v = args[i]
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return w, fmt.Errorf("--timeout: %q is not a positive duration such as 90s or 10m", v)
			}
			w.timeout = d
		default:
			return w, fmt.Errorf("wait: unknown flag %s", a)
		}
	}
	if len(w.ids) == 0 {
		return w, errors.New("wait needs at least one run id")
	}
	return w, nil
}

// cmdWait is `askother wait <id>... [--any] [--timeout D]`. It only observes:
// no write and no config is needed, so it works from a read-only sandbox.
func cmdWait(args []string, stdout, stderr io.Writer) int {
	w, err := parseWaitArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "askother: %v\n\n%s", err, usage)
		return exitRefused
	}
	d, err := stateDeps(os.Environ(), userHome())
	if err != nil {
		return setupFailed(stderr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return waitRuns(ctx, d, w, stdout, stderr)
}

func waitRuns(ctx context.Context, d lifecycle.Deps, w waitArgs, stdout, stderr io.Writer) int {
	var deadline time.Time
	if w.timeout > 0 {
		deadline = time.Now().Add(w.timeout)
	}
	for {
		budget := waitSlice
		if !deadline.IsZero() {
			budget = min(budget, time.Until(deadline))
		}
		res, err := lifecycle.Wait(ctx, d, w.ids, w.any, budget)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "askother: %v\n", err)
			if errors.Is(err, run.CodeNotFound) || errors.Is(err, run.CodeInvalidInput) {
				return exitRefused
			}
			return exitInternal
		case res.Satisfied:
			printStatus(stdout, res.Runs)
			return outcomeCode(res.Runs)
		case ctx.Err() != nil:
			fmt.Fprintln(stderr, "askother: wait interrupted; the runs continue")
			return exitInterrupted
		case !deadline.IsZero() && !time.Now().Before(deadline):
			printStatus(stdout, res.Runs)
			return exitTimeout
		}
	}
}

// outcomeCode applies the exit code precedence to the runs that ended
// (with --any the others are still running and do not count).
func outcomeCode(runs []*run.Run) int {
	var states []run.State
	var execs []run.Execution
	for _, r := range runs {
		if run.IsTerminal(r.State) {
			states = append(states, r.State)
			execs = append(execs, r.Execution)
		}
	}
	return run.ExitCode(states, execs)
}

func printStatus(w io.Writer, runs []*run.Run) {
	now := time.Now()
	for _, r := range runs {
		fmt.Fprintln(w, statusLine(r, now))
	}
}
