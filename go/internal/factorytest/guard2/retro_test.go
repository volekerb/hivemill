package guard2

import (
	"os"
	"os/user"
	"strings"
	"testing"
)

// Port of tests/test_worker_guard_retro.py (G-036 SC3): retro role guard — no push/merge, no role-file edits, no gate
// or config changes.

func liveHome(t testing.TB) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	return u.HomeDir + "/factory"
}

func retroRC(t *testing.T, expect int, role string, payload map[string]any) res {
	t.Helper()
	r := hookJSON(t, payload, hookOpts{args: roleArgs(role)})
	if r.code != expect {
		t.Errorf("%v: exit %d, want %d (stderr %q)", payload["tool_input"], r.code, expect, r.stderr)
	}
	return r
}

func bashPayload(cmd string) map[string]any {
	return map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestRetroGuardBlockedBash(t *testing.T) {
	live := liveHome(t)
	liveStem := live[:len(live)-7]
	cmds := []string{
		"git push origin HEAD",
		"gh pr merge 1",
		"factory judge T-1 --merge",
		"factory config retro_enabled 0",
		"factory project update factory --gate-cmd true",
		"factory gate run T-1",
		"cp /tmp/zz-retro-src.txt " + live + "/roles/retro.md",
		"cp /tmp/zz-retro-src.txt " + live + "/rol*/coder.md",
		"echo x > " + live + "/rol*/coder.md",
		`echo x > "$(printf %s ` + liveStem + `f; printf %s tory/roles)/coder.md"`,
		`echo x > "$(printf %s ` + live + `)/roles/coder.md"`,
		`echo x > "$(printf %s ` + live + `)/roles/retro.md"`,
		`echo x > "$(printf %s ` + live + `/rol; printf %s es)/coder.md"`,
		`echo x > "$(printf %s ` + live + `/r; printf %s oles)/coder.md"`,
		`echo x > "$(printf %s ` + live + `/; printf %s roles/coder.md)"`,
		`echo x > "$(printf '%s/%s' ` + live + ` roles/retro.md)"`,
		`echo x > "$(printf '%s/roles/coder.md' ` + live + `)"`,
		`echo x > "$(printf %s /tmp/zz-out); touch /tmp/zz-retro-hook-exec-semi"`,
		`echo x > "$(touch /tmp/zz-retro-hook-exec-block; printf '%s/%s' ` + live + ` roles/coder.md)"`,
		`echo x > "$(printf %s /tmp/zz-ok.txt)" > $(printf %s ` + live + `/roles/coder.md)`,
		`echo x > $(printf %s ` + live + `/roles/coder.md) > "$(printf %s /tmp/zz-ok.txt)"`,
		"cd " + live + ` && echo x > "$(printf %s roles/coder.md)"`,
		"cd " + live + ` && echo x > "$(printf %s ./roles/coder.md)"`,
		"ZZ=$(printf '%s/%s' " + live + ` roles/coder.md); echo x > "$ZZ"`,
		"cd $HOME/factory && echo x > roles/coder.md",
		"cd ${HOME}/factory && echo x > roles/retro.md",
		"cd $HOME/factory && cp /tmp/zz-retro-src.txt roles/coder.md",
		`cd $HOME/factory && echo x > "$(printf %s roles/coder.md)"`,
		"cd $HOME/factory/ && echo x > roles/coder.md",
		"cd ${HOME}/factory/ && echo x > roles/retro.md",
		"cd $HOME/factory/ && cp /tmp/zz-retro-src.txt roles/coder.md",
		"cd $HOME/factory/. && echo x > roles/coder.md",
		"cd $HOME//factory && echo x > roles/coder.md",
		"cd $HOME/./factory && echo x > roles/coder.md",
		`echo x > "$(printf '%s %s %s' a b c)"`,
		"X=$(printf %s hi > " + live + "/run/guard-export.json)",
		"X=$(mktemp > " + live + "/run/guard-export.json)",
		"git push origin main  # please merge",
	}
	markers := []string{"/tmp/zz-retro-hook-exec-semi", "/tmp/zz-retro-hook-exec-block"}
	for _, cmd := range cmds {
		for _, m := range markers {
			if strings.Contains(cmd, m) {
				if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
		}
		retroRC(t, 2, "retro", bashPayload(cmd))
		for _, m := range markers {
			if strings.Contains(cmd, m) && exists(m) {
				t.Errorf("%s: the guard executed the command: marker %s exists", cmd, m)
			}
		}
	}
}

func TestRetroGuardBlockedEditTools(t *testing.T) {
	for _, tool := range []string{"Write", "Edit", "MultiEdit", "NotebookEdit"} {
		retroRC(t, 2, "retro", map[string]any{"tool_name": tool,
			"tool_input": map[string]any{"file_path": liveHome(t) + "/roles/coder.md", "content": "x"}})
	}
}

func TestRetroGuardAllowedBash(t *testing.T) {
	const marker = "/tmp/zz-only-printf"
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	retroRC(t, 0, "retro", bashPayload(`echo x > "$(printf %s /tmp/zz-only-printf)"`))
	if exists(marker) {
		t.Errorf("%s was created by the guard", marker)
	}
	for _, cmd := range []string{
		`echo x > "$(printf %s /tmp/; printf %s roles/coder.md)"`,
		"factory retro packet",
		"factory whoami",
		"factory inbox",
		"factory done x",
		"git log -1",
		"git diff",
		"gh api repos/volekerb/hivemill/pulls/1/comments",
	} {
		retroRC(t, 0, "retro", bashPayload(cmd))
	}
}

func TestRetroGuardAllowedWriteScratch(t *testing.T) {
	retroRC(t, 0, "retro", map[string]any{"tool_name": "Write",
		"tool_input": map[string]any{"file_path": "/tmp/zz-retro-note.txt", "content": "x"}})
}

func TestRetroGuardOtherRolesUnchanged(t *testing.T) {
	live := liveHome(t)
	retroRC(t, 0, "coder", map[string]any{"tool_name": "Write",
		"tool_input": map[string]any{"file_path": live + "/roles/coder.md", "content": "x"}})
	retroRC(t, 0, "coder", bashPayload("factory config retro_enabled 0"))
	retroRC(t, 0, "coder", bashPayload("git log -1"))
	retroRC(t, 2, "coder", bashPayload("X=$(printf %s hi > "+live+"/run/guard-export.json)"))
	retroRC(t, 2, "reviewer", bashPayload("X=$(mktemp > "+live+"/run/guard-export.json)"))
}
