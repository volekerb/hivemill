package guard

import "testing"

// T-0990 / T-0826: zsh precommand modifiers and declarer/foreach targets (tests/test_worker_guard_nopush_env_prefix_zsh_modifiers.py).

func TestZshPrecommandNoPushDenies(t *testing.T) {
	cwd := t.TempDir()
	denied := []string{
		"env --uns=GIT_DIR git status", "env --uns GIT_DIR git status", "env --unse=GIT_DIR git status",
		"env --ignore-env git status", "env --ignore-environment git status", "env --xyz git status",
		"noglob git push", "nocorrect git push", "repeat 1 git push", "noglob nocorrect git push",
		"noglob - git push", "nocorrect - git push", "repeat 1 - git push",
		"foreach x (a) git push; end", "foreach path (a) ls; end",
		"vared PATH", "vared -p msg GIT_DIR", "integer path", "integer GIT_DIR=1",
		"repeat PATH=1 ls", "repeat GIT_DIR=1 git status",
	}
	for _, role := range []string{"qa", "reviewer"} {
		for _, cmd := range denied {
			if err := Check(cmd, cwd, role, false); err == nil {
				t.Errorf("%s: %q: want block", role, cmd)
			}
		}
	}
}

func TestZshPrecommandBenignAllowed(t *testing.T) {
	cwd := t.TempDir()
	allowed := []string{
		"env FOO=1 ls", "env --uns FOO ls", "noglob ls *", "nocorrect ls", "repeat 2 echo hi",
		"noglob git status", "integer x", "vared x", "repeat 1 ls", "env -a foo ls",
	}
	for _, role := range []string{"qa", "reviewer"} {
		for _, cmd := range allowed {
			if err := Check(cmd, cwd, role, false); err != nil {
				t.Errorf("%s: %q: want allowed, got %v", role, cmd, err)
			}
		}
	}
}
