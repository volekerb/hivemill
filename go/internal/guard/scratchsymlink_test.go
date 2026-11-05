package guard

import (
	"os"
	"path/filepath"
	"testing"
)

// T-0331: a symlink inside the scratchpad that points outside it must not make HOME / FACTORY_HOME count as scratch.
func TestScratchInitSymlinkEscapeBlocked(t *testing.T) {
	f := newGitFix(t)
	outside := filepath.Join(f.tmp, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	esc := filepath.Join(f.scratch, "esc")
	if err := os.Symlink(outside, esc); err != nil {
		t.Fatal(err)
	}
	h, fh := filepath.Join(f.scratch, "h"), filepath.Join(f.scratch, "f")
	for _, d := range []string{h, fh} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{"coder", "qa"} {
		f.allowed(t, "HOME="+h+" FACTORY_HOME="+fh+" factory init", f.wt, role)
		f.blocked(t, "HOME="+h+" FACTORY_HOME="+esc+" factory init", f.wt, role)
		f.blocked(t, "HOME="+esc+" FACTORY_HOME="+fh+" factory init", f.wt, role)
	}
}

// T-0331 attempt 3: only coder and qa get the exemption; every other no-push role stays blocked, and the match is strict.
func TestScratchInitRoleGate(t *testing.T) {
	f := newGitFix(t)
	h, fh := filepath.Join(f.scratch, "h"), filepath.Join(f.scratch, "f")
	for _, d := range []string{h, fh} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := "HOME=" + h + " FACTORY_HOME=" + fh + " factory init"
	f.allowed(t, cmd, f.wt, "coder")
	for _, role := range []string{"reviewer", "pm", "staff-engineer", "scout", "researcher", "evaluator"} {
		f.blocked(t, cmd, f.wt, role)
	}
	f.blocked(t, "HOME="+h+" FACTORY_HOME="+fh+" factory --x init", f.wt, "qa")
}
