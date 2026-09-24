package worker

import (
	"fmt"
	"os"
	"testing"

	"github.com/alex2481kobe/orca/internal/testutil"
)

// TestMain removes the fake worker binary shared by this package's tests.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := testutil.RemoveFakeWorker(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		code = 1
	}
	os.Exit(code)
}
