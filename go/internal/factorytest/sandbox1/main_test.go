package sandbox1

import (
	"os"
	"testing"

	"factory/go/internal/sandbox"
)

// TestMain makes the test binary its own stage-1 helper: Cfg.Self is os.Args[0] in the real-namespace tests, so
// `self --setup ...`, `self --launch ...` and `self gh-shim ...` run the real entry point sandbox.Main exactly as the
// shipped binary would.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if code, handled := sandbox.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}
