package skilleval1

import (
	"os"
	"testing"

	"factory/go/internal/sandbox"
)

// TestMain makes the test binary its own stage-1 helper: sandbox.Cfg.Self is this executable, so the contained
// runs (`self --setup ...`) go through the real entry point sandbox.Main, as in the skilleval and sandbox packages.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if code, handled := sandbox.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}
