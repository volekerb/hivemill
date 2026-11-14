package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// repoRoot is the hivemill checkout that contains bin/flags.json. The merged-tree go proof
// (git archive of go/ only) has no such tree; tests that need it call requireFullRepo.
func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	root := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "bin", "flags.json")); err != nil {
		return ""
	}
	return root
}

func requireFullRepo(t *testing.T) {
	t.Helper()
	if repoRoot() == "" {
		t.Skip("merged-tree go proof: full repo checkout not available")
	}
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	if root := repoRoot(); root != "" {
		return filepath.Join(root, filepath.FromSlash(rel))
	}
	p := filepath.Join("testdata", "repo", filepath.FromSlash(rel))
	if _, err := os.Stat(p); err != nil {
		t.Skipf("merged-tree go proof: missing fixture %s", rel)
	}
	return p
}
