package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unaskedRun is a judging run whose diff changes the given files; it records the PR comments.
func unaskedRun(t *testing.T, files ...string) (*Runner, *SQLite, *[]string) {
	r, s, _ := judgeRun(t)
	git := r.Tools.Git
	r.Tools.Git = func(repo string, args ...string) (int, string, string) {
		if args[0] == "diff" {
			return 0, strings.Join(files, "\x00"), ""
		}
		return git(repo, args...)
	}
	comments := &[]string{}
	r.Tools.GH = func(in string, args ...string) (int, string, string) {
		if len(args) > 1 && args[1] == "comment" {
			*comments = append(*comments, in)
		}
		return 0, "", ""
	}
	return r, s, comments
}

func mergeWith(unasked string) string {
	return `{"verdict": "MERGE", "reasons": ["fine"], "criteria_assessed": [], "changes": [{"change": "does x", "criterion": "x"}, ` +
		unasked + `]}`
}

func verdictOf(t *testing.T, s *SQLite) (*Run, Verdict) {
	t.Helper()
	run, err := s.Run("T-0001")
	if err != nil || run == nil || len(run.Verdicts) != 1 {
		t.Fatalf("no verdict: %v %+v", err, run)
	}
	return run, run.Verdicts[0]
}

const okChange = `{"change": "+ aria-label on @nav", "criterion": null, "unasked_ok": true, "why": "labels only", "file": "web/a.html"}`

func TestJudgeUnaskedOKMerges(t *testing.T) {
	r, s, comments := unaskedRun(t, "web/a.html")
	terms := filepath.Join(t.TempDir(), "terms")
	_ = os.WriteFile(terms, []byte("zz-never-matches-term\n"), 0o600)
	if err := s.SetFactoryCfg("scrub_terms_file", terms); err != nil {
		t.Fatal(err)
	}
	if out := judgeSays(t, r, mergeWith(okChange)); out != "" {
		t.Fatalf("blocked: %q", out)
	}
	run, v := verdictOf(t, s)
	if v.Verdict != VerdictMerge || v.JudgeVerdict != "" {
		t.Fatalf("verdict %+v", v)
	}
	if lv, _, _ := s.LaneVerdict("T-0001", run.JudgeHead); lv != "pass" {
		t.Errorf("lane verdict %q", lv)
	}
	if len(*comments) != 0 {
		t.Errorf("no comment before the merge: %q", *comments)
	}
	r.finish(run, &PR{State: "MERGED"})
	if len(*comments) != 1 || !strings.HasPrefix((*comments)[0], "Unasked changes merged") ||
		!strings.Contains((*comments)[0], "    - + aria-label on @nav\n      why: labels only") {
		t.Errorf("comments %q", *comments)
	}
	evs, err := r.Log.Read()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Event == "judge.unasked_ok" && e.TaskID == "T-0001" && e.Data["why"] == "labels only" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d judge.unasked_ok events", n)
	}
	full, _, _ := plannerRunner(t, "") // the digest needs the whole schema; it reads the same event log
	full.Log = r.Log
	d, err := full.BuildDigest(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if md := d.Markdown(time.Now()); !strings.Contains(md, "## Unasked changes merged (1)\n- T-0001: + aria-label on @nav (why: labels only)") {
		t.Errorf("digest:\n%s", md)
	}
}

// a private term that the 300-byte cut would split still keeps the list out of the public comment
func TestJudgeUnaskedCommentGuardsUncutText(t *testing.T) {
	r, s, comments := unaskedRun(t, "web/a.html")
	terms := filepath.Join(t.TempDir(), "terms")
	_ = os.WriteFile(terms, []byte("zzprivateterm\n"), 0o600)
	if err := s.SetFactoryCfg("scrub_terms_file", terms); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a ", 147) + "zzprivateterm"
	judgeSays(t, r, mergeWith(`{"change": "`+long+`", "criterion": null, "unasked_ok": true, "why": "w", "file": "web/a.html"}`))
	run, _ := verdictOf(t, s)
	r.finish(run, &PR{State: "MERGED"})
	if len(*comments) != 1 || !strings.Contains((*comments)[0], "did not pass") || strings.Contains((*comments)[0], "zzpr") {
		t.Errorf("comments %q", *comments)
	}
}

func TestJudgeUnaskedWithoutFlagRejects(t *testing.T) {
	for _, bad := range []string{
		`{"change": "a new cooldown", "criterion": null}`,
		`{"change": "a new cooldown", "criterion": null, "unasked_ok": false, "why": "w"}`,
		`{"change": "a new cooldown", "criterion": null, "unasked_ok": true, "why": " "}`,
		okChange + `, {"change": "a new cooldown", "criterion": null}`,
	} {
		r, s, _ := unaskedRun(t, "web/a.html")
		judgeSays(t, r, mergeWith(bad))
		_, v := verdictOf(t, s)
		if v.Verdict != VerdictReject || v.JudgeVerdict != VerdictMerge || !strings.Contains(v.Reasons[0], "cooldown") {
			t.Errorf("%s: verdict %+v", bad, v)
		}
	}
}

func TestJudgeUnaskedRiskPathHolds(t *testing.T) {
	change := func(file string) string {
		return `{"change": "wider env stripping", "criterion": null, "unasked_ok": true, "why": "safer", "file": "` + file + `"}`
	}
	for _, risky := range []string{"go/internal/guard/git.go", "roles/hooks/worker-guard", "roles/settings/coder.json.in",
		"go/internal/hm/merge.go", "go/internal/hm/merguard.go", "go/internal/hm/judge.go", ".github/workflows/tests.yml"} {
		for _, file := range []string{risky, "", "not/in/the/diff.go", "web/a.html"} {
			r, s, _ := unaskedRun(t, "web/a.html", risky)
			judgeSays(t, r, mergeWith(change(file)))
			run, v := verdictOf(t, s)
			if v.Verdict != VerdictHold || v.JudgeVerdict != VerdictMerge || !strings.Contains(v.Reasons[0], "env stripping") {
				t.Errorf("%s, file %q: verdict %+v", risky, file, v)
			}
			if lv, _, _ := s.LaneVerdict("T-0001", run.JudgeHead); lv != "fail" {
				t.Errorf("%s: lane verdict %q", risky, lv)
			}
		}
	}
	r, s, _ := unaskedRun(t) // the changed files cannot be listed
	judgeSays(t, r, mergeWith(change("web/a.html")))
	if _, v := verdictOf(t, s); v.Verdict != VerdictHold {
		t.Errorf("unknown files: %+v", v)
	}

	// a HOLD is no way around the quote check
	r, _, _ = unaskedRun(t, "go/internal/hm/session.go")
	misquoted := `{"change": "y", "criterion": "words the task never had"}, ` + change("go/internal/hm/session.go")
	if out := judgeSays(t, r, mergeWith(misquoted)); !strings.Contains(out, "must quote") {
		t.Errorf("misquoted criterion beside a HOLD: %q", out)
	}

	// the owner merges the held PR, or merges by hand: its unasked change is listed, and a head the judge never saw is
	// named. A merge while a new judge runs lists nothing
	for _, tc := range []struct {
		state string
		moved bool
		want  int
	}{{Held, false, 1}, {Working, false, 1}, {Held, true, 1}, {Judging, false, 0}} {
		r, s, comments := unaskedRun(t, "roles/x.md")
		judgeSays(t, r, mergeWith(change("roles/x.md")))
		run, _ := verdictOf(t, s)
		run.State = tc.state
		head := run.JudgeHead
		if tc.moved {
			head = "another-head"
		}
		r.finish(run, &PR{State: "MERGED", HeadRefOid: head})
		if len(*comments) != tc.want || (tc.want == 1 && strings.Contains((*comments)[0], "was not judged") != tc.moved) {
			t.Errorf("%+v: comments %q", tc, *comments)
		}
	}
}

// naming a harmless changed file does not clear a change while the diff also changes a risk-tier file
func TestJudgeUnaskedRiskDiffHoldsAll(t *testing.T) {
	r, s, _ := unaskedRun(t, "go/internal/hm/merge.go", "web/a.html")
	judgeSays(t, r, mergeWith(okChange))
	_, v := verdictOf(t, s)
	if v.Verdict != VerdictHold || v.JudgeVerdict != VerdictMerge ||
		!strings.Contains(v.Reasons[0], "risk-tier path go/internal/hm/merge.go; the judge names the file web/a.html") {
		t.Errorf("verdict %+v", v)
	}
}

func TestJudgePromptUnaskedOK(t *testing.T) {
	b, err := Asset("task-judge.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"criterion": null, "unasked_ok": true, "why": "one line"`, "Plausibly fine",
		"Cannot be judged safe or unsafe", "a guard, a permission, a merge rule or a public API contract",
		"Wrong or risky", "is never `unasked_ok`"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the judge prompt lacks %q", want)
		}
	}
}
