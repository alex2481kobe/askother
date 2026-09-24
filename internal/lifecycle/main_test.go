package lifecycle

import (
	"fmt"
	"os"
	"testing"

	"github.com/alex2481kobe/orca/internal/testutil"
)

// helperEnv selects a helper mode when the test binary re-executes itself as
// a subprocess. Each unit registers its modes from its own test file's init.
const helperEnv = "ORCA_LIFECYCLE_TEST_HELPER"

var helpers = map[string]func(args []string) error{}

func registerHelper(mode string, fn func(args []string) error) {
	if _, dup := helpers[mode]; dup {
		panic("duplicate helper mode " + mode)
	}
	helpers[mode] = fn
}

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		fn, ok := helpers[mode]
		if !ok {
			fmt.Fprintln(os.Stderr, "unknown helper mode", mode)
			os.Exit(2)
		}
		if err := fn(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "helper:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	code := m.Run()
	if err := testutil.RemoveFakeWorker(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		code = 1
	}
	os.Exit(code)
}
