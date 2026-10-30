package guard

import (
	"strings"
	"testing"
)

// T-0775 (#212): a coder's git subcommand must be a literal word. The refused shapes are the Python test's list.
func TestCoderDynamicGitSubcommand(t *testing.T) {
	t.Setenv("FACTORY_AGENT", "coder-t-0001")
	for _, cmd := range []string{
		"p=push; git $p origin main", `p=push; git "$p" origin HEAD:main`, "git ${p:-push} origin main",
		"git $(echo push) origin main", "git `echo push` origin main", `x=(push origin main); git "${x[@]}"`,
		"git -C . $p origin main", "p=push; git -C . $p origin main", `f(){ git "$@"; }; f push origin main`,
		"alias gp='git $p'; gp origin main", `git "$1" origin main`, "git $* origin main",
		"git {push,} origin main", "git pus? origin main", "git p*h origin main", "git pu[s]h origin main",
	} {
		err := Check(cmd, t.TempDir(), "coder", false)
		if err == nil {
			t.Errorf("%q: allowed, want a block", cmd)
		}
	}
	err := Check("p=push; git $p origin main", t.TempDir(), "coder", false)
	if err == nil || !strings.Contains(Explain(err), "hide the git subcommand") {
		t.Errorf("want the dynamic-subcommand message, got %v", err)
	}
	for _, cmd := range []string{"git status", "git log -1", "git merge-tree --write-tree origin/main HEAD"} {
		if err := Check(cmd, t.TempDir(), "coder", false); err != nil {
			t.Errorf("%q: %v", cmd, err)
		}
	}
	if err := Check("p=push; git $p origin main", t.TempDir(), "judge", false); err != nil && strings.Contains(Explain(err), "hide the git subcommand") {
		t.Errorf("other roles are unchanged, got %v", err)
	}
}

func TestLiteralSubcommand(t *testing.T) {
	for w, want := range map[string]bool{
		"push": true, "status": true, "merge-tree": true, "a.b_c": true, "9x": true, "": false, "-x": false, ".x": false,
		"$p": false, "p*h": false, "{push,}": false, "é": false, "push\n": false,
	} {
		if got := literalSubcommand(w); got != want {
			t.Errorf("literalSubcommand(%q) = %v, want %v", w, got, want)
		}
	}
}
