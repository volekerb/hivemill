package merge

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func roll(t *testing.T, js string) Rollup {
	t.Helper()
	var r Rollup
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// Expected values below are what bin/factory_cli.py's latest_checks / checks_ok / missing_checks return for the same
// rollups (run in Python while writing the port).
func TestChecks(t *testing.T) {
	r := roll(t, `[
	 {"__typename":"CheckRun","name":"tests","workflowName":"CI","conclusion":"FAILURE","startedAt":"2026-01-01T00:00:00Z"},
	 {"__typename":"CheckRun","name":"tests","workflowName":"CI","conclusion":"SUCCESS","startedAt":"2026-01-01T00:05:00Z"},
	 {"__typename":"StatusContext","context":"ci/x","state":"PENDING"},
	 {"__typename":"CheckRun","name":"lint","workflowName":"CI","status":"IN_PROGRESS"},
	 {"__typename":"CheckRun","name":"skip","workflowName":"CI","conclusion":"skipped"}]`)
	bad, err := ChecksOK(r)
	if err != nil || !reflect.DeepEqual(bad, []string{"ci/x=PENDING", "lint=IN_PROGRESS"}) {
		t.Fatalf("ChecksOK = %v, %v", bad, err)
	}
	if !Pending(bad) || Pending([]string{"a=FAILURE", "b=PENDING"}) || Pending(nil) {
		t.Error("Pending")
	}
	latest, _ := LatestChecks(r)
	if len(latest) != 4 || latest[0]["conclusion"] != "SUCCESS" {
		t.Errorf("LatestChecks kept %d entries, first %v: the newer run of the same check must replace the older", len(latest), latest[0])
	}
	// a rerun with no timestamp sorts oldest; list order breaks ties
	tie := roll(t, `[{"name":"a","conclusion":"FAILURE"},{"name":"a","conclusion":"SUCCESS"}]`)
	if bad, _ := ChecksOK(tie); len(bad) != 0 {
		t.Errorf("ties go to the later entry, got %v", bad)
	}
	miss, _ := MissingChecks("tests, nope ,", r)
	if !reflect.DeepEqual(miss, []string{"required check nope missing"}) {
		t.Errorf("MissingChecks = %v", miss)
	}
	if _, err := ChecksOK(roll(t, `[{"name":["x"]}]`)); err != ErrUnsupported {
		t.Errorf("a list-valued name must be ErrUnsupported, got %v", err)
	}
}

func TestVerdictEvaluate(t *testing.T) {
	head := "AbCdEf0123456789abcdef0123456789abcdef01"
	never := func(string) (Carry, error) { t.Fatal("carries called"); return Carry{}, nil }
	cases := []struct {
		name string
		v    Verdict
		want Kind
	}{
		{"no row", Verdict{}, Missing},
		{"a fail", Verdict{Present: true, Verdict: "fail", Head: head}, Missing},
		{"exact", Verdict{Present: true, Verdict: "pass", Head: head}, Holds},
		{"case folded", Verdict{Present: true, Verdict: "pass", Head: strings.ToLower(head)}, Holds},
	}
	for _, c := range cases {
		o, err := Evaluate("qa", c.v, head, map[string]Carry{}, nil, true, never)
		if err != nil || o.Kind != c.want {
			t.Errorf("%s: %+v %v, want %s", c.name, o, err, c.want)
		}
	}
	older := Verdict{Present: true, Verdict: "pass", Head: "1111111111111111111111111111111111111111"}
	memo := map[string]Carry{}
	calls := 0
	carries := func(x string) (Carry, error) { calls++; return Carry{Why: "not a pure main merge: x"}, nil }
	for i := 0; i < 2; i++ {
		o, _ := Evaluate("reviewer", older, head, memo, nil, true, carries)
		if o.Kind != StaleVerdict || o.Why != "not a pure main merge: x" || o.From != older.Head {
			t.Fatalf("older head: %+v", o)
		}
	}
	if calls != 1 {
		t.Errorf("carry check ran %d times for one verdict head, want 1 (memo)", calls)
	}
	un := &Carry{Why: "cannot verify, gh repo view failed: boom"}
	if o, _ := Evaluate("qa", Verdict{Present: true, Verdict: "pass"}, head, map[string]Carry{}, un, false, never); o.Why != un.Why {
		t.Errorf("unavailable carry: %+v", o)
	}
	if o, _ := Evaluate("qa", Verdict{Present: true, Verdict: "pass"}, head, map[string]Carry{}, nil, true, never); o.Why != "verdict has no head" {
		t.Errorf("no head: %+v", o)
	}
	if o, _ := Evaluate("qa", older, head, map[string]Carry{}, nil, false, never); o.Why != "cannot verify, gh repo view failed" {
		t.Errorf("no repo url: %+v", o)
	}
}

func TestProblemsOrder(t *testing.T) {
	pr := PR{State: "CLOSED", Draft: true, Mergeable: "CONFLICTING", HeadSHA: "aaaaaaaaaaaa",
		Rollup: roll(t, `[{"name":"t","conclusion":"FAILURE"}]`)}
	got, err := Problems(GateInput{PR: pr, Waited: 5, Required: "need", NoAuto: true,
		Outcomes: []Outcome{{Role: "reviewer", Kind: Missing}, {Role: "qa", Kind: StaleVerdict, From: "bbbbbbbbbbbb", Why: "w"}},
		Stale:    Stale{Why: "git error checking ancestry: 128"}})
	want := []string{"cannot verify head contains origin/main: git error checking ancestry: 128", "no reviewer PASS",
		"qa PASS is for bbbbbbbb, head is now aaaaaaaa (w)", "PR is CLOSED", "PR is a draft", "PR has conflicts",
		"checks not green: t=FAILURE (waited 5s)", "required check need missing", "project has auto_merge=0 — boss must get the owner's OK"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Problems = %q, %v\nwant %q", got, err, want)
	}
	if _, err := Problems(GateInput{PR: PR{State: "OPEN"}, Stale: Stale{Why: "STALE"}}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a stale head must be ErrUnsupported (python refreshes), got %v", err)
	}
}

func TestRepoSlugAndPolicy(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/Owner/Name.git": "owner/name", "u@host:O/N.git": "o/n", "o/r": "o/r",
		"https://github.com/o/r/pull/7?x=1": "pull/7", "/srv/git/O/N.GIT": "o/n",
	} {
		if got, ok := RepoSlug(in); !ok || got != want {
			t.Errorf("RepoSlug(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "  ", "onlyone", "a/..", "../b", "https://h/"} {
		if got, ok := RepoSlug(in); ok {
			t.Errorf("RepoSlug(%q) = %q, want none", in, got)
		}
	}
	if PushPolicy("", "o/r") != "" || PushPolicy("a/b", "o/r") != "" || PushPolicy("A/B, o/r", "O/R") != "repo o/r is frozen (G-021)" {
		t.Error("PushPolicy")
	}
	if got := PushPolicy("a/b", "nothing"); !strings.HasPrefix(got, "cannot tell which repo 'nothing' is while frozen_repos is set") {
		t.Errorf("PushPolicy unparsable = %q", got)
	}
	if got := JudgeSlugs("O/R", "o/r"); !reflect.DeepEqual(got, []string{"o/r"}) {
		t.Errorf("JudgeSlugs = %v", got)
	}
	if ClassifyGateRefusalCause("PR is BEHIND") != "stale_or_conflicting_branch" || ClassifyGateRefusalCause("checks not green: x") != "missing_test" ||
		ClassifyGateRefusalCause("red main") != "gate_refusal" {
		t.Error("ClassifyGateRefusalCause")
	}
}

func TestRedMain(t *testing.T) {
	st := func(v string) func(string) RedMain { return func(string) RedMain { return ParseRedMain(v) } }
	got, err := RedMainRefusal(true, []string{"o/r"}, "T-1", st(`{"run": 5, "task": "T-9", "url": "u"}`))
	if err != nil || got != "red main: run 5 on o/r failed (u); merges wait for a green push run" {
		t.Fatalf("red main = %q %v", got, err)
	}
	if got, _ := RedMainRefusal(true, []string{"o/r"}, "T-9", st(`{"run": 5, "task": "T-9"}`)); got != "" {
		t.Errorf("the named fix task may merge, got %q", got)
	}
	if got, _ := RedMainRefusal(false, []string{"o/r"}, "T-1", st(`{"run": 5}`)); got != "" {
		t.Errorf("flag off: %q", got)
	}
	// an unreadable record keeps the freeze (fail closed)
	if got, _ := RedMainRefusal(true, []string{"o/r"}, "T-1", st(`not json`)); got != "red main: run ? on o/r failed (no url); merges wait for a green push run" {
		t.Errorf("unreadable record = %q", got)
	}
	if got, _ := RedMainRefusal(true, []string{"o/r"}, "T-1", st(`null`)); got != "" {
		t.Errorf("null record = %q", got)
	}
	run := func(m map[string]any, err error) func(string) (string, map[string]any, error) {
		return func(string) (string, map[string]any, error) { return "0123456789abcdef", m, err }
	}
	none := func(string) RedMain { return RedMain{} }
	for _, tc := range []struct {
		name string
		fix  func(string) (string, map[string]any, error)
		want string
	}{
		{"no run yet", run(nil, nil), "main run has not started for o/r head 01234567 (no url); merges wait for it to finish (judge_wait_main_run=1)"},
		{"running", run(map[string]any{"status": "in_progress", "url": "u"}, nil), "main run is in_progress for o/r head 01234567 (u); merges wait for it to finish (judge_wait_main_run=1)"},
		{"failed", run(map[string]any{"status": "completed", "conclusion": "failure", "databaseId": float64(9), "url": "u"}, nil),
			"main run 9 on o/r head 01234567 concluded failure (u); merges wait for a green run (judge_wait_main_run=1)"},
		{"green", run(map[string]any{"status": "completed", "conclusion": "success"}, nil), ""},
		{"gh error", run(nil, errString("gh api commits/main exit 1: boom")),
			"main run unknown on o/r: gh api commits/main exit 1: boom; merges wait until gh answers (judge_wait_main_run=1)"},
	} {
		if got, err := MainRunRefusal(true, []string{"o/r"}, "T-1", none, tc.fix); err != nil || got != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.name, got, err, tc.want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// TestUnicodeParity: Python's str.strip() counts NBSP, \v and U+001C..U+001F as whitespace; str.lower() of U+0130 is two
// characters. Python: " \v\x1cab٣ ".strip() == "ab٣"; "İ".lower() == "i̇".
func TestUnicodeParity(t *testing.T) {
	if got := pyStrip(" \v\x1cab٣ "); got != "ab٣" {
		t.Errorf("pyStrip = %q", got)
	}
	if got := pyLower("İX"); got != "i̇x" {
		t.Errorf("pyLower = %q", got)
	}
	if got, ok := RepoSlug(" O/R\v"); !ok || got != "o/r" {
		t.Errorf("RepoSlug with NBSP and \\v = %q %v", got, ok)
	}
	if miss, _ := MissingChecks(" t ", nil); !reflect.DeepEqual(miss, []string{"required check t missing"}) {
		t.Errorf("MissingChecks strips Unicode whitespace: %v", miss)
	}
	// the Arabic-Indic digit U+0663 is not stripped and not a check state
	if bad, _ := ChecksOK(roll(t, `[{"name":"n","conclusion":"X٣"}]`)); !reflect.DeepEqual(bad, []string{"n=X٣"}) {
		t.Errorf("ChecksOK = %v", bad)
	}
}

// ---- fuzz: every parser/decoder must not panic ----

func FuzzParsePR(f *testing.F) {
	f.Add(`{"number":7,"state":"OPEN","headRefOid":"abc","headRefName":"b","baseRefName":"main","mergeable":"MERGEABLE","isDraft":false,"statusCheckRollup":[{"name":"t","conclusion":"SUCCESS"}],"url":"u"}`)
	f.Add(`{"number":"7"}`)
	f.Add(`[]`)
	f.Add(`{"headRefOid":"x","number":1,"state":null,"mergeable":null,"isDraft":null,"statusCheckRollup":[null]}`)
	f.Fuzz(func(t *testing.T, s string) {
		if pr, err := ParsePR([]byte(s)); err == nil {
			_, _ = ChecksOK(pr.Rollup)
			_, _ = MissingChecks("a,b", pr.Rollup)
		}
	})
}

func FuzzChecksOK(f *testing.F) {
	f.Add(`[{"name":"t","conclusion":"FAILURE","startedAt":"2026-01-01T00:00:00Z"},{"context":"c","state":"PENDING"}]`)
	f.Add(`[{"__typename":"StatusContext","context":null}]`)
	f.Add(`[{"name":1}]`)
	f.Fuzz(func(t *testing.T, s string) {
		var r Rollup
		if json.Unmarshal([]byte(s), &r) != nil {
			return
		}
		bad, err := ChecksOK(r)
		if err == nil {
			_ = Pending(bad)
		}
		_, _ = LatestChecks(r)
		_, _ = MissingChecks(s, r)
	})
}

func FuzzRepoSlug(f *testing.F) {
	for _, s := range []string{"https://github.com/o/r.git", "u@host:o/r", "o/r", "", "a?b#c/d\n", " x/y\v"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// no panic; the slug of "00/0  #" keeps its inner blanks in Python too, so it is not required to be stable
		_, _ = RepoSlug(s)
		_ = CfgSlugs(s)
		_ = PushPolicy(s, s)
		_ = ParseRedMain(s)
	})
}

// TestLatestChecksIdentityIsATuple: (workflow "x|sy", name "z") and (workflow "x", name "y|sz") are two checks, as in
// Python's (workflowName, name) tuple; the red one must not be replaced by the later green one.
func TestLatestChecksIdentityIsATuple(t *testing.T) {
	r := roll(t, `[{"__typename":"CheckRun","name":"z","workflowName":"x|sy","conclusion":"FAILURE","startedAt":"2026-01-01T00:00:00Z"},
	 {"__typename":"CheckRun","name":"y|sz","workflowName":"x","conclusion":"SUCCESS","startedAt":"2026-01-01T00:05:00Z"}]`)
	bad, err := ChecksOK(r)
	if err != nil || !reflect.DeepEqual(bad, []string{"z=FAILURE"}) {
		t.Fatalf("ChecksOK = %v, %v; want the red check kept apart", bad, err)
	}
}
