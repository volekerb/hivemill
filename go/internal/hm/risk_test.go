package hm

import (
	"os"
	"path/filepath"
	"testing"
)

const testTierRules = `{"docs_globs": ["docs/**", "*.md"], "docs_excluded_prefixes": ["roles/", "ci/", ".github/"],
 "tests_globs": ["tests/test_*.py"], "sensitive_paths": [".github/**", "ci/tier_rules.json", "roles/hooks/**", "roles/*.md",
 "go/internal/guard/**"], "sensitive_defs": {}, "small_lines": 150, "large_lines": 600}`

func riskHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, "ci"), 0o755)
	if err := os.WriteFile(filepath.Join(home, "ci", "tier_rules.json"), []byte(testTierRules), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// The routing reads files, never words: a task about auth that names the guard hook in its prose but declares and
// touches only docs gets the default model (Sonnet).
func TestRiskIgnoresTaskTextAndRoutesDocsToSonnet(t *testing.T) {
	r, _ := testRunner(t, "")
	r.Env.Home = riskHome(t)
	task := &Task{Project: "factory", Title: "Explain the auth check of roles/hooks/worker-guard",
		Spec:       "WHY: owners ask how auth works in roles/hooks/worker-guard and go/internal/guard/rules.go.\nFILES: docs/auth.md.",
		Acceptance: "1. documented - cmd:grep -c auth docs/auth.md => 1"}
	risky, why := Risk(r.Env.Home, task, nil)
	if risky {
		t.Fatalf("auth and sensitive paths in the text must not route to Opus: %s", why)
	}
	if risky, why = Risk(r.Env.Home, task, []string{"docs/auth.md"}); risky {
		t.Fatalf("a docs-only diff: %s", why)
	}
	if m, _, _ := r.ModelFor(risky); m != "claude-sonnet-5-5" {
		t.Errorf("model %s, want Sonnet", m)
	}
	// with no declared scope at all, the prose still decides nothing
	bare := &Task{Project: "factory", Spec: "touch roles/hooks/worker-guard auth"}
	if risky, why := Risk(r.Env.Home, bare, []string{"docs/auth.md"}); risky {
		t.Errorf("undeclared scope, docs diff: %s", why)
	}
}

func TestRiskRoutesSensitiveFilesToOpus(t *testing.T) {
	r, _ := testRunner(t, "")
	r.Env.Home = riskHome(t)
	cases := []struct {
		name    string
		task    *Task
		changed []string
	}{
		{"FILES names a sensitive file", &Task{Spec: "FILES: go/internal/guard/rules.go, docs/guard.md."}, nil},
		{"FILES names a directory holding one", &Task{Spec: "FILES: roles/hooks/ and tests."}, nil},
		{"FILES glob can match one", &Task{Spec: "FILES: go/internal/*/rules.go."}, nil},
		{"scope criterion allows one", &Task{Acceptance: "1. scope - cmd:git diff --name-only origin/main...HEAD | " +
			"grep -vE '^(docs/.*|go/internal/.*)$' => empty"}, nil},
		{"scope criterion names one exactly", &Task{Acceptance: "1. scope - cmd:git diff --name-only origin/main...HEAD | " +
			"grep -vx 'ci/tier_rules.json' => empty"}, nil},
		{"the diff changes one", &Task{Spec: "FILES: docs/ci.md."}, []string{"docs/ci.md", ".github/workflows/ci.yml"}},
		{"explicit tier T3", &Task{Tier: "T3", Spec: "FILES: docs/x.md."}, nil},
	}
	for _, c := range cases {
		risky, why := Risk(r.Env.Home, c.task, c.changed)
		if !risky {
			t.Errorf("%s: not risky", c.name)
			continue
		}
		if m, _, _ := r.ModelFor(risky); m != "claude-opus-5-5" {
			t.Errorf("%s (%s): model %s, want Opus", c.name, why, m)
		}
	}
	safe := &Task{Spec: "FILES: go/internal/hm/risk.go, docs/option-b-runner.md.", Acceptance: "1. scope - cmd:git diff --name-only " +
		"origin/main...HEAD | grep -vE '^(go/internal/hm/.*|docs/.*|tests/.*)$' => empty"}
	if risky, why := Risk(r.Env.Home, safe, []string{"go/internal/hm/risk.go"}); risky {
		t.Errorf("a scope that cannot reach a sensitive path: %s", why)
	}
	// a file is matched as a file: roles/*.md covers one segment, so a nested role prompt is not sensitive
	if risky, why := Risk(r.Env.Home, &Task{Spec: "FILES: roles/hivemill/task-judge.md, go/internal/hm/judge.go."}, nil); risky {
		t.Errorf("a nested .md file under roles/: %s", why)
	}
	// "No roles/ change" lists what stays out of scope; it is not a declaration of roles/
	if risky, why := Risk(r.Env.Home, &Task{Spec: "FILES: go/internal/hm/run.go. No roles/ or .github/ change."}, nil); risky {
		t.Errorf("a negated FILES clause: %s", why)
	}
}

func TestRiskFailsClosedWithoutRules(t *testing.T) {
	if risky, why := Risk(t.TempDir(), &Task{Project: "factory", Spec: "FILES: docs/x.md."}, nil); !risky || why != "tier rules unreadable" {
		t.Errorf("unreadable rules: %v %q", risky, why)
	}
}

func TestRegexPrefixes(t *testing.T) {
	got := regexPrefixes(`^(bin/factory_cli\.py|go/internal/tick/.*|go/internal/cli/.*)$`)
	want := map[string]bool{"bin/factory_cli.py": true, "go/internal/tick/": false, "go/internal/cli/": false}
	if len(got) != len(want) {
		t.Fatalf("prefixes %+v", got)
	}
	for _, p := range got {
		if full, ok := want[p.lit]; !ok || full != p.full {
			t.Errorf("prefix %+v", p)
		}
	}
	if got := regexPrefixes(`docs/`); len(got) != 1 || got[0].lit != "" {
		t.Errorf("unanchored regex overlaps everything: %+v", got)
	}
}
