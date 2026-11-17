package scout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ch "factory/go/internal/cursorharness"
	ft "factory/go/internal/factorytest"
)

// Port of tests/test_cursor_harness.py (5 cases; see ported_scout.txt).
// adapted: Python's `PILOT_ROLES == CURSOR_ROLES` has one Go name (ch.CursorRoles), so the equality cannot be asserted;
// the case checks the allow-list itself: the evaluator is not in it and every factory worker role but the evaluator is.

func TestCursorHarnessParsePilotRolesCoderAndQAAllowed(t *testing.T) {
	for _, r := range []string{"coder", "qa"} {
		got, err := ch.ParsePilotRoles(r)
		if err != nil {
			t.Fatal(err)
		}
		eqStrings(t, r, got, []string{r})
	}
}

func TestCursorHarnessParsePilotRolesEvaluatorRejected(t *testing.T) {
	if _, err := ch.ParsePilotRoles("evaluator"); err == nil {
		t.Fatal("evaluator must be rejected")
	}
}

func TestCursorHarnessParsePilotRolesUnknownRoleRejected(t *testing.T) {
	_, err := ch.ParsePilotRoles("not-a-role")
	if err == nil {
		t.Fatal("unknown role must be rejected")
	}
	contains(t, "error", err.Error(), "not-a-role")
}

func TestCursorHarnessPilotRolesExcludeEvaluator(t *testing.T) {
	if ch.CursorRoles["evaluator"] {
		t.Fatal("evaluator must not be in the cursor allow-list")
	}
	if len(ch.CursorRoles) == 0 || !ch.CursorRoles["coder"] {
		t.Fatalf("allow-list = %v", ch.CursorRoles)
	}
	for _, r := range ch.SortedRoles() {
		if got, err := ch.ParsePilotRoles(r); err != nil || len(got) != 1 {
			t.Fatalf("role %s: %v %v", r, got, err)
		}
	}
}

func TestCursorHarnessHooksFailClosedAndPreToolUseMatcher(t *testing.T) {
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	hooksDir := ch.HooksDirForRepo(root)
	if _, err := os.Stat(hooksDir); err != nil {
		t.Fatalf("hooks dir %s: %v", hooksDir, err) // the gate tree carries roles/: no fallback
	}
	tmp := t.TempDir()
	_, err = ch.MaterializeCursorPolicy(tmp, "coder", ch.MaterializeOpts{
		HooksDir:     hooksDir,
		Deny:         []string{"Bash(sudo:*)"},
		Allow:        []string{},
		SystemPrompt: "# coder\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(tmp, ".cursor", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("hooks.json: %v", err)
	}
	if len(doc.Hooks) == 0 {
		t.Fatal("hooks.json has no hooks")
	}
	for event, entries := range doc.Hooks {
		for _, e := range entries {
			if v, _ := e["failClosed"].(bool); !v {
				t.Errorf("%s hook entry must set failClosed true: %v", event, e)
			}
		}
	}
	pre := doc.Hooks["preToolUse"]
	if len(pre) == 0 {
		t.Fatal("no preToolUse hook")
	}
	if m, _ := pre[0]["matcher"].(string); m != "Shell|Write|Delete|Read|Grep|StrReplace|EditNotebook" {
		t.Fatalf("matcher = %q", m)
	}
}

// FuzzParsePilotRoles (G7): the roles value is user config text; it must never panic.
func FuzzParsePilotRoles(f *testing.F) {
	for _, s := range []string{"coder", "qa", "evaluator", "not-a-role", "", " coder , qa ,", "coder ", "\v", "٣"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ch.ParsePilotRoles(s)
		if err == nil {
			for _, r := range got {
				if !ch.CursorRoles[r] || strings.TrimSpace(r) != r {
					t.Fatalf("accepted %q from %q", r, s)
				}
			}
		}
	})
}
