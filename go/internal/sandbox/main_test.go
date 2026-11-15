package sandbox

import (
	"os"
	"testing"
)

// TestMain makes the test binary its own stage-1 / helper binary: Cfg.Self is os.Args[0] in the real-namespace and
// credential-helper tests, so `self --setup ...`, `self git-credential ...` and `self gh-shim ...` run the real
// entry points (Main) exactly as the shipped binary would.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if code, handled := Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}
