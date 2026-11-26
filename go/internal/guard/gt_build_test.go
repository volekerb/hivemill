// Ported from go/internal/factorytest/guardtools/corpus_test.go (T-1922). The one guardtools case that runs a tool:
// it checks the build graph of cmd/worker-guard with go list, not a guard rule, so it stays apart from gt_test.go.

package guard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// gtGoTool finds go on PATH or in GOROOT; a guard test never skips for a missing toolchain.
func gtGoTool() (string, error) {
	if p, err := exec.LookPath("go"); err == nil {
		return p, nil
	}
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("go not found on PATH or in GOROOT; install Go (a guard test never skips)")
}

func TestGTGuardBuildWorkerGuardHasNoDependencyOutsideThisModule(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..")) // the module root, go/
	if err != nil {
		t.Fatal(err)
	}
	gobin, err := gtGoTool()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(gobin, "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./cmd/worker-guard")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v; run it by hand in go/", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if !strings.HasPrefix(p, "factory/go") {
			t.Errorf("worker-guard depends on %s: the guard stays dependency-free (ADR 0001)", p)
		}
	}
}
