package skilleval2

import (
	"os"
	"testing"

	"factory/go/internal/sandbox"
)

// TestMain makes the test binary its own stage-1 helper: the contained checker (skilleval.RunChecker) re-executes this
// binary as `self --setup ...`, which sandbox.Main answers. Any other package test file must not add a second TestMain.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if code, handled := sandbox.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}
