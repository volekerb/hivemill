package guard

import (
	"strings"
	"testing"
)

// Mirror of tests/test_worker_guard_hq_deny.py: same entries, forms and allow list, same order.
// Counts: block entries 33, forms 12, block cases 396 + 5 extra, allow 35.

var hqHerdrSubs = []string{"server stop", "workspace close", "workspace create", "tab close", "tab create",
	"pane close", "pane split", "agent start"}

type hqEntry struct{ word, rest string }

func hqEntries() []hqEntry {
	var out []hqEntry
	for _, s := range hqHerdrSubs {
		out = append(out, hqEntry{"herdr", s})
	}
	hc := append(append([]string{}, hqHerdrSubs...), "kill x", "rm x", "run x")
	for _, w := range []string{"hc", "herdr-core"} {
		for _, s := range hc {
			out = append(out, hqEntry{w, s})
		}
	}
	return append(out, hqEntry{"claude", "-p hi"}, hqEntry{"gh", "pr merge 1"}, hqEntry{"sqlite3", "x.db .tables"})
}

var hqForms = []struct {
	name string
	fn   func(w, r string) string
}{
	{"plain", func(w, r string) string { return w + " " + r }},
	{"assign", func(w, r string) string { return "FOO=1 " + w + " " + r }},
	{"env", func(w, r string) string { return "env " + w + " " + r }},
	{"command", func(w, r string) string { return "command " + w + " " + r }},
	{"exec", func(w, r string) string { return "exec " + w + " " + r }},
	{"nohup", func(w, r string) string { return "nohup " + w + " " + r }},
	{"timeout", func(w, r string) string { return "timeout 5 " + w + " " + r }},
	{"abspath", func(w, r string) string { return "/usr/local/bin/" + w + " " + r }},
	{"dotpath", func(w, r string) string { return "./" + w + " " + r }},
	{"shell-c", func(w, r string) string { return "bash -c '" + w + " " + r + "'" }},
	{"and-chain", func(w, r string) string { return "true && " + w + " " + r }},
	{"subst", func(w, r string) string { return "echo $(" + w + " " + r + ")" }},
}

var hqExtraBlock = []string{
	"gh pr merge 1 --repo o/n",
	"gh -R o/n pr merge 1",
	"gh --repo o/n pr merge 1",
	"npx @anthropic-ai/claude-code -p hi",
	"node /x/node_modules/@anthropic-ai/claude-code/cli.js",
}

var hqAllow = []string{
	"herdr pane list", "herdr pane read p1", "hc ls", "hc read x", "hc send x hi", "herdr agent list",
	"grep 'herdr server stop' f", "echo claude", "git log --grep sqlite3",
	"factory msg director s 'herdr pane close happened'",
	"cat <<'EOF'\nherdr server stop\nclaude\nEOF",
	"factory spawn coder --task T-1", "factory relaunch director",
	"gh pr view 1", "gh pr diff 1", "gh pr checks 1", "gh pr list",
	"which claude", "which herdr", "command -v claude", "command -v herdr", "command -v hc",
	"command -v herdr-core", "command -v sqlite3", "command -v gh",
	"echo sqlite3 gh pr merge", "grep -r 'gh pr merge' docs",
	"./tests/stubs/herdr server stop", "tests/stubs/hc kill x",
	"$X pane close", "${X} server stop", "echo 'unbalanced herdr server stop",
	"git status", "factory inbox", "echo hc run",
}

func hqBlocked(t *testing.T, cmd string) {
	t.Helper()
	err := Check(cmd, "", "", true)
	if err == nil {
		t.Errorf("hq %q: allowed", cmd)
		return
	}
	if !strings.Contains(Explain(err), "HQ deny rule") {
		t.Errorf("hq %q: wrong block: %s", cmd, Explain(err))
	}
}

func TestHqDeny(t *testing.T) {
	clearTermsEnv(t)
	t.Run("entries-in-every-form", func(t *testing.T) {
		for _, e := range hqEntries() {
			for _, f := range hqForms {
				hqBlocked(t, f.fn(e.word, e.rest))
			}
		}
	})
	t.Run("extra-block", func(t *testing.T) {
		for _, cmd := range hqExtraBlock {
			hqBlocked(t, cmd)
		}
	})
	t.Run("reason-text", func(t *testing.T) {
		err := Check("hc server stop", "", "", true)
		want := "HQ deny rule: hc server stop is denied for boss, director and lead in every spelling " +
			"(see roles/settings; use the factory CLI)"
		if err == nil || !strings.Contains(Explain(err), want) {
			t.Errorf("reason: %v", err)
		}
	})
	t.Run("allow", func(t *testing.T) {
		for _, cmd := range hqAllow {
			if err := Check(cmd, "", "", true); err != nil {
				t.Errorf("hq allow %q: %v", cmd, err)
			}
		}
	})
	t.Run("counts", func(t *testing.T) {
		if len(hqEntries()) != 33 || len(hqForms) != 12 || len(hqExtraBlock) != 5 || len(hqAllow) != 35 {
			t.Errorf("case counts drifted from the Python module: %d %d %d %d",
				len(hqEntries()), len(hqForms), len(hqExtraBlock), len(hqAllow))
		}
	})
}
