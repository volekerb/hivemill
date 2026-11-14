//go:build !race

package cli

import (
	"path/filepath"
	"testing"
)

// TestGoldenRegenClean is the "parity regen" proof: update mode run on a temp copy of testdata/golden must
// reproduce the checked-in goldens byte for byte. The worktree is never written.
//
// Excluded from -race runs: it only shells out to python3 and compares bytes (no concurrency to prove),
// and replaying every case under the race detector blows the CI go-job package budget (go test default -timeout 10m).
// Run locally: cd go && go test -count=1 -run TestGoldenRegenClean ./internal/cli/
func TestGoldenRegenClean(t *testing.T) {
	copyRoot := filepath.Join(t.TempDir(), "golden")
	if err := copyTree(goldenDir, copyRoot); err != nil {
		t.Fatal(err)
	}
	regenGolden(t, copyRoot) // skips with a reason when python3 is not on PATH
	want, err := regenFiles(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := regenFiles(copyRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("no golden files under " + goldenDir)
	}
	for _, d := range regenDiff(want, got) {
		t.Error(d + "; regenerate with: cd go && go test -count=1 -run TestGolden ./internal/cli/ -args -update-golden")
	}
}
