package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunExitCodes drives the entry point with a fake node on PATH: PASS exits 0, a missing tool exits 1.
func TestRunExitCodes(t *testing.T) {
	bin, err := filepath.Abs("../../internal/verify/testdata/bin")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := filepath.Abs("../../internal/verify/testdata/tree")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	env := func(path string) []string { return []string{"PATH=" + path, "HOME=" + tmp, "TMPDIR=" + tmp} }
	var out bytes.Buffer
	if code := run([]string{"mod", "--tree", tree}, env(bin+":/usr/bin:/bin"), &out); code != 0 || !strings.Contains(out.String(), "verify: result PASS mode=mod") {
		t.Fatalf("code=%d\n%s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"mod", "--tree", tree}, env(""), &out); code != 1 || !strings.Contains(out.String(), "reason=could not run") {
		t.Fatalf("code=%d\n%s", code, out.String())
	}
	if m, _ := filepath.Glob(filepath.Join(tmp, "verify-home-*")); len(m) != 0 {
		t.Errorf("leftover temp home: %v", m)
	}
}
