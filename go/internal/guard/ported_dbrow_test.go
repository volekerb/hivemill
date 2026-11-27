package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePython3 puts an executable python3 first on PATH that only appends to a marker file, and returns the marker's
// path: a test that ends with the marker absent proves the guard ran no python3 (T-2112).
func fakePython3(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "python3-ran")
	script := "#!/bin/sh\necho \"$@\" >> '" + marker + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "python3"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

// noPythonRan fails the test when the fake python3 of fakePython3 was run.
func noPythonRan(t *testing.T, marker string) {
	t.Helper()
	if b, err := os.ReadFile(marker); err == nil {
		t.Errorf("python3 was run by the guard: %q", b)
	}
}

// The DB values the guard export has no row for (formerly "a DB value the export has no row for" was left to the
// Python hook). With a DB file present the export is read, and a missing row is absent, as the Python hook's query
// returning no row is:
//   - own_goal: no agents row (or an empty goal_id) is None, so `factory goal show <id>` is the broad-read block;
//   - task_worktree: no tasks row is None, so the coder's push has no own worktree and is refused (fail closed);
//   - test_sandbox_on: no config row is False, so the uv/pytest runner is not routed to the sandbox;
//   - PublicText.__init__: no public_text_repos / frozen_repos row leaves both sets empty, so the rule is off.
func TestPortedMissingDBRow(t *testing.T) {
	marker := fakePython3(t)
	const owner = "git commit -m 'a secret'" // dbHome's terms file holds "secret"
	for _, c := range []struct {
		name  string
		exp   exportDoc
		agent string
		cmd   string
		wt    bool   // run from the gitFix worktree (the coder-t-7 push cases)
		block string // "" = allowed, else a substring of the block
	}{
		{"own goal row present", exportDoc{Goals: map[string]string{"coder-t-9": "G-005"}}, "coder-t-9", "factory goal show G-005", false, ""},
		{"own goal no agents row", exportDoc{}, "coder-t-9", "factory goal show G-005", false, "broad factory reads"},
		{"own goal row for another agent", exportDoc{Goals: map[string]string{"coder-t-8": "G-005"}}, "coder-t-9", "factory goal show G-005", false, "broad factory reads"},
		{"own goal empty goal id", exportDoc{Goals: map[string]string{"coder-t-9": ""}}, "coder-t-9", "factory goal show G-005", false, "broad factory reads"},
		{"own goal other goal", exportDoc{Goals: map[string]string{"coder-t-9": "G-006"}}, "coder-t-9", "factory goal show G-005", false, "broad factory reads"},
		{"task worktree row present", exportDoc{Worktrees: map[string]string{"T-7": ""}}, "coder-t-7", "git push origin HEAD:factory/t-7", true, ""},
		{"task worktree no tasks row", exportDoc{}, "coder-t-7", "git push origin HEAD:factory/t-7", true, "cannot resolve the worktree recorded for t-7"},
		{"task worktree row for another task", exportDoc{Worktrees: map[string]string{"T-8": ""}}, "coder-t-7", "git push origin HEAD:factory/t-7", true, "cannot resolve the worktree recorded for t-7"},
		{"test sandbox row 1", exportDoc{Config: map[string]string{"test_sandbox": " 1 "}}, "", "uvx pytest tests/test_x.py", false, "test_sandbox=1"},
		{"test sandbox no config row", exportDoc{}, "", "uvx pytest tests/test_x.py", false, ""},
		{"test sandbox row 0", exportDoc{Config: map[string]string{"test_sandbox": "0"}}, "", "uvx pytest tests/test_x.py", false, ""},
		{"public text row present", exportDoc{Config: map[string]string{"public_text_repos": "o/r"}}, "", owner, false, "owner-term"},
		{"public text no rows", exportDoc{}, "", owner, false, ""},
		{"public text empty rows", exportDoc{Config: map[string]string{"public_text_repos": "", "frozen_repos": " "}}, "", owner, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			cwd := ""
			if c.wt {
				f := newGitFix(t)
				cwd = f.wt
				for k := range c.exp.Worktrees {
					c.exp.Worktrees[k] = f.wt
				}
			} else {
				t.Chdir(t.TempDir())
			}
			home := dbHome(t) // a DB file: the export is read, not the "no DB" branch
			writeExport(t, home, c.exp)
			t.Setenv("FACTORY_HOME", home)
			if c.agent != "" {
				t.Setenv("FACTORY_AGENT", c.agent)
			} else {
				unsetEnv(t, "FACTORY_AGENT")
			}
			err := Check(c.cmd, cwd, "coder", false)
			switch {
			case IsDelegate(err):
				t.Errorf("%q: %v, want the decision made from the export, not a fail-closed refusal", c.cmd, err)
			case c.block == "" && err != nil:
				t.Errorf("%q: %v, want allowed", c.cmd, err)
			case c.block != "" && (err == nil || !strings.Contains(err.Error(), c.block)):
				t.Errorf("%q: %v, want a block containing %q", c.cmd, err, c.block)
			}
		})
	}
	noPythonRan(t, marker)
}
