package hm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The scope shapes of real pilot tasks: a FILES: line with a negated clause, an allow-list criterion, a deny-list
// criterion and a whole-line (-x) criterion.
var (
	scopeNudge = &Task{Spec: "Make every tick nudge write one events row.\nFILES: bin/factory_cli.py nudge functions, " +
		"tests/test_nudge_events.py (new); go/internal/tick only if ported. Rules: merge origin/main before PR.",
		Acceptance: "1. tests pass - test:tests/test_nudge_events.py\n3. Scope: only the tick code and tests change - " +
			"cmd:git diff --name-only origin/main...HEAD | grep -vE '^(bin/factory_cli\\.py|go/internal/tick/.*|tests/.*|docs/.*)$' => empty"}
	scopeSpend = &Task{Spec: "FILES: go/internal/kit/spend/*.go and tests with testdata transcripts, go/cmd/hm-spend (flags). " +
		"Same files as T-1776. No bin/factory_cli.py or roles/ change.",
		Acceptance: "9. no Python or roles change - cmd:git diff --name-only origin/main...HEAD | grep -E '^(bin/|roles/)' => empty"}
	scopeWWW = &Task{Spec: "BUILD: 1. 404.html spacing.\nFILES: 404.html and/or one CSS file under assets/.",
		Acceptance: "3. only the 404 page or its CSS changes - cmd:git diff --name-only origin/main...HEAD | grep -vx 'index.html' => empty"}
)

func TestDeclaredScopeReadsFilesLineAndScopeCriteria(t *testing.T) {
	s := DeclaredScope(scopeNudge)
	if len(s.Allow) != 1 || len(s.Deny) != 0 || len(s.Files) != 3 || len(s.Bad) != 0 {
		t.Fatalf("nudge scope: allow %d deny %d files %v bad %v", len(s.Allow), len(s.Deny), s.Files, s.Bad)
	}
	// the three files the pilot's #878 really touched are in scope: its unasked change was inside a declared file
	if out := s.OutOfScope([]string{"bin/factory_cli.py", "tests/test_nudge_events.py"}); len(out) != 0 {
		t.Errorf("in-scope files flagged: %v", out)
	}
	out := s.OutOfScope([]string{"docs/x.md", "web/app.py"})
	if len(out) != 2 || !strings.Contains(out[0], "not named on the FILES: line") || !strings.Contains(out[1], "allow-list") {
		t.Errorf("out of scope: %v", out)
	}

	s = DeclaredScope(scopeSpend)
	for _, f := range s.Files {
		if strings.HasPrefix(f, "bin/") || strings.HasPrefix(f, "roles") {
			t.Errorf("a clause starting with No names files out of scope, not in it: %v", s.Files)
		}
	}
	if out := s.OutOfScope([]string{"go/internal/kit/spend/metrics.go", "go/cmd/hm-spend/main.go"}); len(out) != 0 {
		t.Errorf("glob and directory entries: %v", out)
	}
	if out := s.OutOfScope([]string{"roles/hivemill/task.md"}); len(out) != 1 || !strings.Contains(out[0], "forbidden") {
		t.Errorf("deny-list: %v", out)
	}

	s = DeclaredScope(scopeWWW)
	// each declaration is checked on its own: index.html passes the -x allow-list but is not on the FILES: line
	out = s.OutOfScope([]string{"index.html", "404.html"})
	if len(out) != 2 || !strings.Contains(out[0], "FILES:") || !strings.Contains(out[1], "allow-list") {
		t.Errorf("-x pattern is a whole-line match: %v", out)
	}
	if out := s.OutOfScope([]string{"assets/css/factory.css"}); len(out) != 1 {
		t.Errorf("FILES: assets/ covers the CSS but the -x allow-list does not: %v", out)
	}
}

func TestScopeSectionFlagsAndNeverGuesses(t *testing.T) {
	got := ScopeSection(DeclaredScope(scopeNudge), []string{"bin/factory_cli.py", "web/app.py"})
	if !strings.Contains(got, "FLAGGED: 1 of 2") || !strings.Contains(got, "web/app.py") {
		t.Errorf("flagged list: %s", got)
	}
	if got := ScopeSection(DeclaredScope(&Task{Spec: "no list"}), []string{"a.go"}); !strings.Contains(got, "no declared scope") {
		t.Errorf("undeclared: %s", got)
	}
	bad := &Task{Acceptance: "1. scope - cmd:git diff --name-only origin/main...HEAD | grep -vE '^(a|b' => empty"}
	got = ScopeSection(DeclaredScope(bad), []string{"c.go"})
	if !strings.Contains(got, "unreadable declaration") || !strings.Contains(got, "no declared scope") {
		t.Errorf("a regex that does not compile is reported, not guessed: %s", got)
	}
}

func ptr(s string) *string { return &s }

func TestChangeProblems(t *testing.T) {
	task := &Task{Spec: "BUILD: 1. Window flags: --since and --until, mutually usable with --hours; default unchanged.",
		Acceptance: "6. T-1776 fields kept - cmd:hm-spend report --root testdata --hours 1000000 --json => exit 0"}
	v := &Verdict{Changes: []Change{
		{Change: "+ --since flag", Criterion: ptr("Window flags: --since and --until")},
		{Change: "+ fields kept", Criterion: ptr("6. T-1776 fields kept")},
		{Change: "maxHours 2160 -> 1000000", Criterion: ptr("--hours 1000000")},
		{Change: "cooldown on failure", Criterion: nil},
		{Change: "x", Criterion: ptr("something nobody wrote")},
	}}
	unmapped, misquoted := ChangeProblems(v, task)
	if len(unmapped) != 1 || unmapped[0] != "cooldown on failure" {
		t.Errorf("unmapped %v", unmapped)
	}
	if len(misquoted) != 2 || !strings.Contains(misquoted[0], "quotes a proof") || !strings.Contains(misquoted[1], "not in the task") {
		t.Errorf("misquoted %v", misquoted)
	}
}

func judgeRun(t *testing.T) (*Runner, *SQLite, string) {
	r, s := testRunner(t, "claude-task-judge-t-0001-r1")
	head := strings.Repeat("b", 40)
	_ = s.PutRun(&Run{Task: "T-0001", Goal: "G-001", Agent: "claude-task-t-0001", Judges: []string{"claude-task-judge-t-0001-r1"},
		State: Judging, Round: 1, JudgeHead: head, PRURL: "https://github.com/zz-test/www/pull/1"})
	return r, s, head
}

func judgeSays(t *testing.T, r *Runner, verdict string) string {
	t.Helper()
	var out bytes.Buffer
	in, _ := json.Marshal(map[string]string{"last_assistant_message": "```json\n" + verdict + "\n```", "session_id": "j-1"})
	if err := r.JudgeHook(bytes.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestJudgeMergeWithAnUnmappedChangeIsRejected(t *testing.T) {
	r, s, head := judgeRun(t)
	out := judgeSays(t, r, `{"verdict": "MERGE", "reasons": ["fine"], "criteria_assessed": [{"id": 1, "proven": true}],
		"changes": [{"change": "does x", "criterion": "x"}, {"change": "a failed nudge now starts the cooldown", "criterion": null}]}`)
	if out != "" {
		t.Fatalf("an unmapped change rejects at once, no block: %q", out)
	}
	got, _ := s.Run("T-0001")
	v := got.Verdicts[0]
	if got.State != Judged || v.Verdict != VerdictReject || v.JudgeVerdict != VerdictMerge || !strings.Contains(v.Reasons[0], "cooldown") {
		t.Errorf("run %s verdict %+v", got.State, v)
	}
	if lv, _, _ := s.LaneVerdict("T-0001", head); lv != "fail" {
		t.Errorf("lane verdict %q", lv)
	}
}

func TestJudgeMergeQuotingOnlyAProofIsRefusedOnce(t *testing.T) {
	r, s, _ := judgeRun(t)
	merge := `{"verdict": "MERGE", "reasons": ["fine"], "criteria_assessed": [], "changes": [{"change": "c", "criterion": "cmd:true"}]}`
	if out := judgeSays(t, r, merge); !strings.Contains(out, `"block"`) || !strings.Contains(out, "proof") {
		t.Fatalf("a proof quote is refused once: %q", out)
	}
	if out := judgeSays(t, r, `{"verdict": "MERGE", "reasons": ["fine"], "criteria_assessed": [], "changes": []}`); out != "" {
		t.Fatalf("the second malformed MERGE is recorded, not blocked: %q", out)
	}
	got, _ := s.Run("T-0001")
	if v := got.Verdicts[0]; v.Verdict != VerdictReject || !v.Invalid {
		t.Errorf("second miss counts as REJECT: %+v", v)
	}
}

func TestJudgeHoldParksTheRunForTheOwner(t *testing.T) {
	r, s, head := judgeRun(t)
	judgeSays(t, r, `{"verdict": "HOLD", "reasons": ["the CSS rule also restyles film/index.html"], "criteria_assessed": [],
		"changes": [{"change": ".section > h1 + .lede", "criterion": null}]}`)
	if lv, _, _ := s.LaneVerdict("T-0001", head); lv != "fail" {
		t.Fatalf("a HOLD is never a pass verdict: %q", lv)
	}
	run, _ := s.Run("T-0001")
	r.judged(run)
	if run.State != Held || !strings.Contains(run.MergeHold, "judge HOLD") || Active(run.State) {
		t.Errorf("held run: %s %q", run.State, run.MergeHold)
	}
}
