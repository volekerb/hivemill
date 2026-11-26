package hm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"factory/go/internal/gitx"
)

// t3Fixture is a T3 run in state merging on real git: base holds the files of t3Base, the task branch applies a change
// (an empty content deletes the file). gh is fake.
type t3Fixture struct {
	r      *Runner
	s      *SQLite
	run    *Run
	ci     string     // conclusion of the PR's one check
	merges [][]string // argv of every `gh pr merge`
	alerts int        // alert_cmd calls

	check      string // name of the PR's one check
	mergeFails bool
}

var t3Base = map[string]string{
	"roles/settings/zz.json.in":     `{"permissions":{"allow":["Read"],"deny":["Bash(rm:*)","Bash(sudo:*)"]}}`,
	"go/internal/guard/zz_test.go":  "package guard\n\nfunc TestZZ(t *testing.T) {\n\tcheck(\"zz one\")\n}\n",
	"go/internal/guard/zz_guard.go": "package guard\n",
	".github/workflows/tests.yml":   "on: push\njobs:\n  unit:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make test\n",
}

// a T3 change no limit covers
var t3Clean = map[string]string{"go/internal/guard/zz_guard.go": "package guard\n\n// zz\n"}

func newT3(t *testing.T, change map[string]string) *t3Fixture {
	t.Helper()
	r, s := testRunner(t, "")
	if _, err := s.db.Exec(`ALTER TABLE goals ADD COLUMN intent TEXT`); err != nil { // the digest reads it
		t.Fatal(err)
	}
	tmp := t.TempDir()
	bare, wt := filepath.Join(tmp, "origin.git"), filepath.Join(tmp, "wt")
	sh(t, tmp, "init", "-q", "--bare", "-b", "main", bare)
	sh(t, tmp, "clone", "-q", bare, wt)
	commit := func(msg string, files map[string]string) {
		for p, content := range files {
			full := filepath.Join(wt, p)
			if content == "" {
				if err := os.Remove(full); err != nil {
					t.Fatal(err)
				}
				continue
			}
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		sh(t, wt, "add", "-A")
		sh(t, wt, "commit", "-q", "-m", msg)
	}
	commit("base", t3Base)
	sh(t, wt, "push", "-q", "origin", "HEAD:main")
	sh(t, wt, "checkout", "-q", "-b", "factory/t-0001")
	commit("T-0001: zz change", change)
	sh(t, wt, "push", "-q", "origin", "HEAD:factory/t-0001")
	head := sh(t, wt, "rev-parse", "HEAD")

	// a dummy term list: the public-text check never passes without one
	terms := filepath.Join(tmp, "terms.txt")
	_ = os.WriteFile(terms, []byte("zz-private-term\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "ci"), 0o755)
	_ = os.WriteFile(filepath.Join(r.Env.Home, "ci", "scrub_allowlist.txt"), nil, 0o644)
	for k, v := range map[string]string{"noreply_email": "1+t@users.noreply.github.com", "scrub_terms_file": terms} {
		if _, err := s.db.Exec(`INSERT INTO config(key,value) VALUES(?,?)`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetCfg("alert_cmd", "true"); err != nil {
		t.Fatal(err)
	}

	f := &t3Fixture{r: r, s: s, ci: "SUCCESS", check: "unit"}
	o := gitx.Options{Home: r.Env.Home, Timeout: time.Minute}
	r.Tools.Git = func(repo string, args ...string) (int, string, string) { return gitx.RC(repo, o, args...) }
	r.Tools.Sh = func(string, time.Duration, []string, ...string) (int, string) { f.alerts++; return 0, "" }
	state := "OPEN"
	r.Tools.GH = func(_ string, args ...string) (int, string, string) {
		switch {
		case len(args) > 1 && args[0] == "pr" && args[1] == "view":
			b, _ := json.Marshal(map[string]any{"number": 1, "state": state, "headRefOid": head, "mergeable": "MERGEABLE",
				"title": "T-0001: zz change", "body": "zz", "url": "https://github.com/zz-test/www/pull/1",
				"statusCheckRollup": []map[string]any{{"__typename": "CheckRun", "name": f.check, "status": "COMPLETED", "conclusion": f.ci}}})
			return 0, string(b), ""
		case len(args) > 1 && args[0] == "pr" && args[1] == "merge":
			f.merges = append(f.merges, slices.Clone(args))
			if f.mergeFails {
				return 1, "", "zz refused"
			}
			state = "MERGED"
			return 0, "", ""
		case args[0] == "run":
			return 0, "[]", ""
		case args[0] == "api":
			return 0, "1+t@users.noreply.github.com noreply@github.com", ""
		}
		return 1, "", "no"
	}
	f.run = &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Branch: "factory/t-0001",
		Worktree: wt, Head: head, State: Merging, Round: 1, PRURL: "https://github.com/zz-test/www/pull/1"}
	if err := s.PutRun(f.run); err != nil {
		t.Fatal(err)
	}
	if err := s.Verdict("T-0001", f.run.PRURL, head, "pass", "MERGE", "zz-judge"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *t3Fixture) events(t *testing.T, kind string) []Event {
	t.Helper()
	all, err := f.r.Log.Read()
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range all {
		if e.Event == kind {
			out = append(out, e)
		}
	}
	return out
}

// held checks the outcome of a hard limit: no merge, one owner ask that lists it, one alert, one merge.guarded event.
func (f *t3Fixture) held(t *testing.T, want ...string) {
	t.Helper()
	f.r.Merge(f.run)
	if f.run.State != Held || len(f.merges) != 0 {
		t.Fatalf("state %s (%s), merges %v", f.run.State, f.run.MergeHold, f.merges)
	}
	if err := f.s.PutRun(f.run); err != nil {
		t.Fatal(err)
	}
	f.r.syncHeldAsks() // the later pass must not file a second question
	asks, _ := f.s.Asks("open")
	if len(asks) != 1 {
		t.Fatalf("asks %+v", asks)
	}
	ev := f.events(t, "merge.guarded")
	if len(ev) != 1 {
		t.Fatalf("merge.guarded events %+v", ev)
	}
	limits, _ := json.Marshal(ev[0].Data["limits"])
	for _, w := range want {
		if !strings.Contains(asks[0].Question, w) || !strings.Contains(string(limits), w) {
			t.Errorf("want %q in the ask %q and the event %s", w, asks[0].Question, limits)
		}
	}
	if f.alerts != 1 {
		t.Errorf("alert_cmd ran %d times", f.alerts)
	}
}

func TestT3AutoMerge(t *testing.T) {
	f := newT3(t, t3Clean)
	f.r.Merge(f.run)
	if f.run.State != Merged || len(f.merges) != 1 {
		t.Fatalf("state %s (%s), merges %v", f.run.State, f.run.MergeHold, f.merges)
	}
	argv := f.merges[0]
	if !slices.Contains(argv, "--squash") || !slices.Contains(argv, "--delete-branch") || slices.Contains(argv, "--admin") || slices.Contains(argv, "--auto") {
		t.Errorf("gh argv %v", argv)
	}
	ev := f.events(t, "merge.guarded")
	if len(ev) != 1 {
		t.Fatalf("merge.guarded events %+v", ev)
	}
	if limits, ok := ev[0].Data["limits"].([]any); !ok || len(limits) != 0 {
		t.Errorf("limits must be an empty list: %#v", ev[0].Data["limits"])
	}
	d, err := f.r.BuildDigest(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if md := d.Markdown(time.Now()); !strings.Contains(md, "T3 merged without you (1)\n- T-0001 ") {
		t.Errorf("digest:\n%s", md)
	}
	if asks, _ := f.s.Asks("open"); len(asks) != 0 || f.alerts != 0 {
		t.Errorf("asks %+v, alerts %d", asks, f.alerts)
	}
}

func TestT3DenyRemovalHeld(t *testing.T) {
	newT3(t, map[string]string{"roles/settings/zz.json.in": `{"permissions":{"allow":["Read","Bash(gh pr:*)"],"deny":["Bash(rm:*)"]}}`}).
		held(t, "a) roles/settings/zz.json.in: removes or edits deny entry", "d) roles/settings/zz.json.in: adds an allow")
}

func TestT3GuardTestDeletionHeld(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		newT3(t, map[string]string{"go/internal/guard/zz_test.go": ""}).held(t, "b) go/internal/guard/zz_test.go: deletes a guard test file")
	})
	t.Run("case", func(t *testing.T) {
		newT3(t, map[string]string{"go/internal/guard/zz_test.go": "package guard\n\nfunc TestZZ(t *testing.T) {\n}\n"}).
			held(t, "b) go/internal/guard/zz_test.go: removes or edits 1 test case line(s)")
	})
}

func TestT3WorkflowSkipHeld(t *testing.T) {
	skip := "on: push\njobs:\n  unit:\n    if: false\n    runs-on: ubuntu-latest\n    steps:\n      - run: make test\n"
	newT3(t, map[string]string{".github/workflows/tests.yml": skip}).held(t, "c) .github/workflows/tests.yml: adds if: false")

	jobs := "on: push\njobs:\n  unit:\n    name: unit\n    steps:\n      - name: other\n  lint:\n    steps: []\n"
	diff := Diff{{Path: ".github/workflows/t.yml", Old: jobs, New: "on: push\njobs:\n  lint:\n    continue-on-error: true\n    steps: []\n"}}
	got := strings.Join(limitList(Check(diff, PRStatus{CI: "green", Mergeable: "MERGEABLE", Required: []string{"unit"}})), "; ")
	for _, want := range []string{"removes job unit", "removes the required check name unit", "adds continue-on-error: true"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in %q", want, got)
		}
	}
}

func TestT3RedCIHeld(t *testing.T) {
	f := newT3(t, t3Clean)
	f.ci = "FAILURE"
	f.held(t, "e) CI is not all green (red)")
}

func TestT3ConfigOff(t *testing.T) {
	f := newT3(t, t3Clean)
	if err := f.s.SetCfg("t3_auto_merge", "off"); err != nil {
		t.Fatal(err)
	}
	f.r.Merge(f.run)
	if f.run.State != Held || len(f.merges) != 0 || len(f.events(t, "merge.guarded")) != 0 {
		t.Fatalf("state %s (%s), merges %v", f.run.State, f.run.MergeHold, f.merges)
	}
}

// a PR that renames away a required job never gets that check: the owner is asked instead of waiting for ever
func TestMergeGuardMissingRequiredCheck(t *testing.T) {
	require := func(f *t3Fixture) {
		if _, err := f.s.db.Exec(`INSERT OR REPLACE INTO config(key,value) VALUES('required_checks','unit')`); err != nil {
			t.Fatal(err)
		}
		f.check = "other"
	}
	f := newT3(t, map[string]string{".github/workflows/tests.yml": "on: push\njobs:\n  unit:\n    name: Unit tests\n    steps: []\n"})
	require(f)
	f.held(t, "c) .github/workflows: removes the required check name unit")

	f = newT3(t, t3Clean) // the check is only late: wait
	require(f)
	f.r.Merge(f.run)
	if f.run.State != Merging || len(f.events(t, "merge.guarded")) != 0 || f.alerts != 0 {
		t.Fatalf("state %s (%s), alerts %d", f.run.State, f.run.MergeHold, f.alerts)
	}
}

func TestMergeGuardFailedMergeIsNotUnattended(t *testing.T) {
	f := newT3(t, t3Clean)
	f.mergeFails = true
	f.r.Merge(f.run)
	if len(f.merges) != 1 || f.run.State != Merging || f.run.Unattended {
		t.Fatalf("state %s (%s), merges %v, unattended %v", f.run.State, f.run.MergeHold, f.merges, f.run.Unattended)
	}
}

func TestMergeGuardAllowEntries(t *testing.T) {
	for entry, want := range map[string]bool{
		"Bash": true, "Bash(*)": true, "Bash(gh*)": true, "Bash(gh pr:*)": true, "Bash(gh pr merge*)": true,
		"Bash(gh pr merge --squash:*)": true, "Bash(gh pr merge 1 --admin)": true,
		"Read": false, "Bash(gh pr view:*)": false, "Bash(git:*)": false, "Bash(gh pr merge 1 --squash)": false,
	} {
		b, _ := json.Marshal(entry)
		if got := allowsForcedMerge(string(b)); got != want {
			t.Errorf("allowsForcedMerge(%s) = %v", entry, got)
		}
	}
}

func TestMergeGuardOtherLimits(t *testing.T) {
	ok := PRStatus{CI: "green", Mergeable: "MERGEABLE"}
	for _, c := range []struct {
		f    DiffFile
		want string // "" = no limit
	}{
		{DiffFile{Path: "roles/hooks/worker-guard.py", Old: "# note\ndeny(a)\ndeny(b)\n", New: "deny(a)\n"}, "d)"},
		{DiffFile{Path: "roles/hooks/worker-guard.py", Old: "# note\ndeny(a)\n", New: "deny(a)\ndeny(b)\n"}, ""},
		{DiffFile{Path: "go/cmd/worker-guard/zz_test.go", Old: "x", Deleted: true}, "b)"},
		{DiffFile{Path: "go/internal/factorytest/guard1/testdata/zz.json", Old: "[\n1,\n2\n]", New: "[\n1\n]"}, "b)"},
		{DiffFile{Path: "ci/guard_corpus.json", Old: `{"must_block":["a","b"]}`, New: `{"must_block":["a"]}`}, "b)"},
		{DiffFile{Path: "ci/guard_corpus.json", Old: `{"must_block":["a"]}`, New: `{"must_block":["a","b"]}`}, ""},
		{DiffFile{Path: "plugins/zz/settings.json", Old: `{"ask":["x"]}`, Deleted: true}, "a)"},
		{DiffFile{Path: "roles/settings/zz.json.in", Old: `{"deny":["x"]}`, New: `{"deny":[`}, "a)"},
		{DiffFile{Path: "roles/settings/zz.json.in", Old: `{"deny":["x"]}`, New: `{"deny":["y","x"]}`}, ""},
	} {
		got := strings.Join(limitList(Check(Diff{c.f}, ok)), "; ")
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s %q -> %q: limits %q, want %q", c.f.Path, c.f.Old, c.f.New, got, c.want)
		}
	}
	if got := Check(nil, PRStatus{CI: "green", Mergeable: "CONFLICTING"}); len(got) != 1 || got[0].Rule != "e" {
		t.Errorf("conflict: %v", got)
	}
}
