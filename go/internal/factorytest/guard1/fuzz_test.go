package guard1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzHookCommand is the fuzz target for the parser behind the hook (G7): whatever command text the Go guard is given
// it must decide, exit 0 (allow) or 2 (block with a reason on stderr), never crash, hang or exit any other way.
func FuzzHookCommand(f *testing.F) {
	for _, s := range []string{
		"git status", "git push origin main", "git push origin main", "git\vpush origin main", "git push origin ٣",
		"tar -xf a.tar -C r/.git", "unzip a.zip -d r/.git", "env -S 'git push' x", "FOO=1 git -c core.x=y status",
		"gh api -X POST repos/o/r/releases", "python3 -c 'import os'", "cat <<EOF | sh\ngit push\nEOF", "'unterminated", "$(", "",
	} {
		f.Add(s)
	}
	bin(f) // build once, before the workers start
	f.Fuzz(func(t *testing.T, cmd string) {
		if strings.ContainsRune(cmd, 0) {
			t.Skip("NUL cannot be part of a shell command")
		}
		w := newWorld(t)
		scratch := filepath.Join(w.dir(0), "scratch")
		home := filepath.Join(w.dir(0), "live")
		for _, d := range []string{scratch, home} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("make %s: %v", d, err)
			}
		}
		env := w.environ(call{Set: map[string]string{"WORKER_GUARD_LIVE_HOME": home, "WORKER_GUARD_SCRATCH": scratch}})
		payload, err := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": cmd}, "cwd": scratch})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		for _, role := range []string{"qa", "coder"} {
			got := run(t, []string{"--role", role}, string(payload), env, scratch)
			if got.rc != 0 && got.rc != 2 {
				t.Fatalf("role %s command %q: exit %d, want 0 or 2; stderr %q", role, cmd, got.rc, got.stderr)
			}
			if got.rc == 2 && strings.TrimSpace(got.stderr) == "" {
				t.Fatalf("role %s command %q: blocked without a reason on stderr", role, cmd)
			}
		}
	})
}
