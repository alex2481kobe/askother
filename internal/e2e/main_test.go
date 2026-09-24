// Package e2e tests the real askother binary end to end: `askother mcp` over pipes,
// detached supervisors, fake codex/claude workers and the `askother wait` CLI.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alex2481kobe/askother/internal/testutil"
)

// buildDir holds the askother binary and every test's state; it outlives the
// tests so TestMain can check them for survivors.
var (
	buildDir    string
	askotherBin string
)

func TestMain(m *testing.M) {
	code := 1
	if err := buildAskOther(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL: build askother:", err)
	} else {
		code = m.Run()
	}
	if leaked := reapAllRuns(); len(leaked) > 0 {
		fmt.Fprintln(os.Stderr, "FAIL: live supervisors outlived their tests (killed now):", strings.Join(leaked, ", "))
		code = 1
	}
	if err := testutil.RemoveFakeWorker(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		code = 1
	}
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

func buildAskOther() error {
	var err error
	if buildDir, err = os.MkdirTemp("", "askother-e2e-"); err != nil {
		return err
	}
	// Resolve /var -> /private/var so paths compare equal to what the
	// binary reports about itself.
	if buildDir, err = filepath.EvalSymlinks(buildDir); err != nil {
		return err
	}
	askotherBin = filepath.Join(buildDir, "askother")
	_, file, _, _ := runtime.Caller(0)
	cmd := exec.Command("go", "build", "-o", askotherBin, "./cmd/askother")
	cmd.Dir = filepath.Join(filepath.Dir(file), "..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// reapAllRuns kills the supervisor and worker group of every run in every
// test's state home whose lock is still held, and names them.
func reapAllRuns() []string {
	homes, _ := filepath.Glob(filepath.Join(buildDir, "t-*", "state"))
	var leaked []string
	for _, h := range homes {
		leaked = append(leaked, killLiveRuns(h)...)
	}
	return leaked
}
