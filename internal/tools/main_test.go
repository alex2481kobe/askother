package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/alex2481kobe/askother/internal/config"
	"github.com/alex2481kobe/askother/internal/lifecycle"
	"github.com/alex2481kobe/askother/internal/run"
	"github.com/alex2481kobe/askother/internal/testutil"
	"github.com/alex2481kobe/askother/internal/worker"
)

// tSupervisor is the helper mode: the test binary, symlinked under this name
// as Deps.Self, acts as `askother supervise <id>`. The name also makes leftover
// helpers findable with `pgrep -f t-supervisor`.
const tSupervisor = "t-supervisor"

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == tSupervisor {
		os.Exit(tSupervise(os.Args[1:]))
	}
	code := m.Run()
	if err := testutil.RemoveFakeWorker(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		code = 1
	}
	os.Exit(code)
}

// tSupervise runs lifecycle.Supervise on inherited fds 3 and 4, as cmd/askother
// will. The store is <dir>/state, where Self is <dir>/bin/t-supervisor.
func tSupervise(args []string) int {
	if len(args) != 2 || args[0] != "supervise" {
		fmt.Fprintln(os.Stderr, "usage: supervise <id>")
		return 2
	}
	home := filepath.Join(filepath.Dir(filepath.Dir(os.Args[0])), "state")
	st, err := run.Open(home)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	d := lifecycle.Deps{Store: st, Self: os.Args[0], StateHome: home,
		Adapters: adapters(), Env: config.EnvMap(os.Environ())}
	return lifecycle.Supervise(d, args[1], os.NewFile(run.LockFD, "lock"), os.NewFile(run.ReadyFD, "ready"))
}

func adapters() map[string]worker.Adapter {
	return map[string]worker.Adapter{"codex": worker.Codex{}, "claude": worker.Claude{}}
}
