package main

import (
	"os"

	"github.com/alex2481kobe/askother/internal/lifecycle"
	"github.com/alex2481kobe/askother/internal/run"
)

// cmdSupervise is `askother supervise <id>`, started only by the launcher. It
// runs from the record alone and never reads the config.
// Fds 3 and 4 are handed to Supervise untouched: it marks them
// close-on-exec before any child exists. No signal handlers: a supervisor
// that dies releases the run lock, and readers then see interrupted.
func cmdSupervise(args []string) int {
	if len(args) != 1 {
		return 1
	}
	d, err := stateDeps(os.Environ(), userHome())
	if err != nil {
		return 1 // stderr is /dev/null; readers see the run interrupted
	}
	return lifecycle.Supervise(d, args[0], os.NewFile(run.LockFD, "lock"), os.NewFile(run.ReadyFD, "ready"))
}
