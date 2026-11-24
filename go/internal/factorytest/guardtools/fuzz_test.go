package guardtools

import (
	"testing"

	"factory/go/internal/guard"
)

// G8 Unicode parity. Expected values are what the Python hook (roles/hooks/worker-guard.py) answers for the same
// input, measured with both implementations side by side (exit 0 for each): NBSP, vertical tab and U+0663 are not
// shell whitespace and not ASCII word characters, so a "sudo" glued to them is a different word.
func TestPrivGuardUnicodeParity(t *testing.T) {
	for _, cmd := range []string{"sudo id", "echo hi; sudo id", "echo hi;\vsudo id", "sudo\vid", "٣sudo id",
		"echo ٣ sudo", "echo sudo", "su -", "docker ps", "ls | xargs sudo"} {
		eachMode(func(mode string) { allowed(t, cmd, mode, "") })
	}
}

// G7: the command parser must never panic and must never let a plain leading "sudo " through, whatever follows.
func FuzzPrivGuardCommand(f *testing.F) {
	for _, s := range []string{"id", "-n true", "'x", " ", "$(", "`", "\\\n", "a | b", "٣"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tail string) {
		if err := guard.Check("sudo "+tail, "/tmp", "coder", false); err == nil {
			t.Errorf("sudo %q was allowed", tail)
		}
		_ = guard.Check(tail, "/tmp", "coder", true) // must not panic in HQ mode either
	})
}
