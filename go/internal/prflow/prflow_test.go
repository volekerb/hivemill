package prflow

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

const fixtureRepo = "o/r"

var fixtureNow = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// fakeGH replaces runGH for one test and returns the call log.
func fakeGH(t *testing.T) *[][]string {
	t.Helper()
	created, before := readFixture(t, "prs_created.json"), readFixture(t, "prs_before.json")
	var calls [][]string
	old := runGH
	runGH = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		for i, a := range args {
			if a == "--search" && i+1 < len(args) && strings.HasPrefix(args[i+1], "created:>=") {
				return created, nil
			}
		}
		return before, nil
	}
	t.Cleanup(func() { runGH = old })
	return &calls
}

func fixtureReport(t *testing.T) *Report {
	t.Helper()
	fakeGH(t)
	f, err := os.Open("testdata/events.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	rep, err := Run(Options{Repo: fixtureRepo, Since: 24 * time.Hour, Now: fixtureNow, Events: f})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

// fixtureWalk walks one fixture PR the way Run does.
func fixtureWalk(t *testing.T, number int) (Walk, string, string) {
	t.Helper()
	fakeGH(t)
	since := fixtureNow.Add(-24 * time.Hour)
	prs, _, err := FetchPRs(fixtureRepo, since)
	if err != nil {
		t.Fatalf("FetchPRs: %v", err)
	}
	ix, err := ReadEvents(strings.NewReader(string(readFixture(t, "events.jsonl"))), fixtureRepo, since.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	for _, p := range prs {
		if p.Number != number {
			continue
		}
		end := fixtureNow
		if p.MergedAt != nil {
			end = *p.MergedAt
		}
		task := ix.taskOf(p)
		return Stages(p.CreatedAt, end, ix.Events[task], ix.Red), task, ix.orgOf(task)
	}
	t.Fatalf("PR %d is not in the fixture", number)
	return Walk{}, "", ""
}

func wantHours(t *testing.T, got map[string]float64, want map[string]float64) {
	t.Helper()
	for _, s := range StageOrder {
		if math.Abs(got[s]-want[s]) > 0.011 {
			t.Errorf("stage %s: got %.3f h, want %.3f h (all: %v)", s, got[s], want[s], got)
		}
	}
}

func TestStages(t *testing.T) {
	cases := []struct {
		name    string
		pr      int
		task    string
		org     string
		hours   map[string]float64
		current string
	}{
		{"ci_wait then review_qa then judge then merged", 1, "T-0001", "claude",
			map[string]float64{StageCIWait: 1, StageReviewQA: 1, StageJudge: 1}, StageJudge},
		{"reviewer FAIL -> coder_rework, second round, refresh", 2, "T-0002", "cursor",
			map[string]float64{StageCIWait: 1.5, StageReviewQA: 2, StageCoderRework: 1, StageJudge: 0.5, StageRefresh: 1},
			StageRefresh},
		{"judge-hold-set .. judge-hold-clear -> hold; red-main window overlapping hold counts as red_main", 3, "T-0003",
			"claude", map[string]float64{StageCIWait: 0.5, StageHold: 7, StageRedMain: 2, StageJudge: 0.5}, StageJudge},
		{"status=blocked -> blocked", 4, "T-0004", "cursor",
			map[string]float64{StageCIWait: 1, StageBlocked: 13}, StageBlocked},
		{"PR with no task -> org other, all time ci_wait", 5, "", "other",
			map[string]float64{StageCIWait: 2}, StageCIWait},
		{"branch factory/t-NNNN with no events -> task from the branch, org other", 6, "T-0006", "other",
			map[string]float64{StageCIWait: 3}, StageCIWait},
		{"capacity gate, coder wait, red PR, ci resume", 8, "T-0008", "claude",
			map[string]float64{StageCIWait: 1, StageCapacityGate: 2, StageCoderRework: 2.5, StageRedPRUnowned: 0.5},
			StageCoderRework},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, task, org := fixtureWalk(t, c.pr)
			if task != c.task || org != c.org {
				t.Errorf("task/org: got %q/%q, want %q/%q", task, org, c.task, c.org)
			}
			wantHours(t, w.Hours(), c.hours)
			if w.Current() != c.current {
				t.Errorf("current stage: got %s, want %s", w.Current(), c.current)
			}
		})
	}
	t.Run("review counters", func(t *testing.T) {
		w, _, _ := fixtureWalk(t, 2)
		if w.ReviewerFail != 1 || w.QAFail != 0 || w.Rounds != 2 || w.Refreshes != 1 {
			t.Errorf("got reviewerFail=%d qaFail=%d rounds=%d refreshes=%d, want 1 0 2 1",
				w.ReviewerFail, w.QAFail, w.Rounds, w.Refreshes)
		}
	})
}

func TestStagesEdges(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }
	t.Run("open red-main window lasts until now", func(t *testing.T) {
		w := Stages(t0, at(4), []TaskEvent{{At: at(1), Action: "judge-autospawn"}}, []Interval{{From: at(2)}})
		wantHours(t, w.Hours(), map[string]float64{StageCIWait: 1, StageJudge: 1, StageRedMain: 2})
		if w.Current() != StageRedMain {
			t.Errorf("current: got %s, want red_main", w.Current())
		}
	})
	t.Run("red main does not touch ci_wait or review_qa", func(t *testing.T) {
		w := Stages(t0, at(4), []TaskEvent{{At: at(1), Action: "review-autospawn", Arg: "s"}}, []Interval{{From: t0, To: at(4)}})
		wantHours(t, w.Hours(), map[string]float64{StageCIWait: 1, StageReviewQA: 3})
	})
	t.Run("events before open and after merge are ignored; file order does not matter", func(t *testing.T) {
		w := Stages(at(1), at(3), []TaskEvent{
			{At: at(5), Action: "judge-hold-set"},
			{At: at(2), Action: "judge-autospawn"},
			{At: at(0.5), Action: "judge-hold-set"},
		}, nil)
		wantHours(t, w.Hours(), map[string]float64{StageCIWait: 1, StageJudge: 1})
	})
	t.Run("QA FAIL with reviewer PASS -> coder_rework; unknown wait kind ignored", func(t *testing.T) {
		w := Stages(t0, at(4), []TaskEvent{
			{At: at(1), Action: "review-autospawn", Arg: "s"},
			{At: at(2), Action: "reviewer-pass"},
			{At: at(2.5), Action: "review-autospawn-wait", Arg: "verdict"},
			{At: at(3), Action: "qa-fail"},
		}, nil)
		wantHours(t, w.Hours(), map[string]float64{StageCIWait: 1, StageReviewQA: 2, StageCoderRework: 1})
		if w.QAFail != 1 || w.Rounds != 1 {
			t.Errorf("got qaFail=%d rounds=%d, want 1 1", w.QAFail, w.Rounds)
		}
	})
}

func TestGhCalls(t *testing.T) {
	calls := fakeGH(t)
	since := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	prs, warnings, err := FetchPRs(fixtureRepo, since)
	if err != nil {
		t.Fatalf("FetchPRs: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("gh calls: got %d, want 2", len(*calls))
	}
	wantSearch := []string{"created:>=2026-11-10T00:00:00Z", "created:<2026-11-10T00:00:00Z -closed:<2026-11-10T00:00:00Z"}
	for i, args := range *calls {
		got := strings.Join(args, " ")
		want := "pr list -R o/r --state all --limit 500 --search " + wantSearch[i] +
			" --json number,state,createdAt,mergedAt,headRefName,mergeStateStatus"
		if got != want {
			t.Errorf("gh call %d:\n got %s\nwant %s", i+1, got, want)
		}
	}
	var nums []int
	for _, p := range prs {
		nums = append(nums, p.Number)
	}
	// 7 is closed without a merge; 1 is in both lists and is kept once.
	if got, want := strings.Trim(strings.Join(strings.Fields(strings.Trim(jsonOf(t, nums), "[]")), ""), ","), "1,2,3,4,5,6,8"; got != want {
		t.Errorf("PR numbers: got %s, want %s", got, want)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: %v", warnings)
	}

	fixtureReport(t)
	t.Run("a whole run calls gh twice", func(t *testing.T) {
		c := fakeGH(t)
		rep, err := Run(Options{Repo: fixtureRepo, Since: 24 * time.Hour, Now: fixtureNow,
			Events: strings.NewReader(string(readFixture(t, "events.jsonl")))})
		if err != nil || rep == nil {
			t.Fatalf("Run: %v", err)
		}
		if len(*c) != 2 {
			t.Errorf("gh calls in Run: got %d, want 2", len(*c))
		}
	})
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGhErrors(t *testing.T) {
	old := runGH
	t.Cleanup(func() { runGH = old })
	runGH = func(...string) ([]byte, error) { return nil, errors.New("gh pr list exited 1: rate limit") }
	if _, err := Run(Options{Repo: fixtureRepo, Since: time.Hour, Now: fixtureNow, Events: strings.NewReader("")}); err == nil ||
		!strings.Contains(err.Error(), "rate limit") {
		t.Errorf("gh failure: got %v, want the gh error", err)
	}
	runGH = func(...string) ([]byte, error) { return []byte("not json"), nil }
	if _, err := Run(Options{Repo: fixtureRepo, Since: time.Hour, Now: fixtureNow, Events: strings.NewReader("")}); err == nil {
		t.Error("non-JSON gh output: got nil error")
	}
	rows := make([]PR, ghLimit)
	for i := range rows {
		rows[i] = PR{Number: i + 1, State: "OPEN", CreatedAt: fixtureNow.Add(-time.Minute)}
	}
	full := []byte(jsonOf(t, rows))
	runGH = func(...string) ([]byte, error) { return full, nil }
	rep, err := Run(Options{Repo: fixtureRepo, Since: time.Hour, Now: fixtureNow, Events: strings.NewReader("")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Warnings) != 2 || !strings.Contains(rep.Text(), "warning: gh pr list") {
		t.Errorf("limit reached: got warnings %v, want one per call and a warning line", rep.Warnings)
	}
	for _, bad := range []Options{
		{Repo: "norepo", Since: time.Hour, Now: fixtureNow, Events: strings.NewReader("")},
		{Repo: fixtureRepo, Since: 0, Now: fixtureNow, Events: strings.NewReader("")},
	} {
		if _, err := Run(bad); err == nil {
			t.Errorf("Run(%+v): got nil error", bad)
		}
	}
}

func TestEventsStream(t *testing.T) {
	fixture := string(readFixture(t, "events.jsonl"))
	// One valid event of about 1.5 MB (over 1 MB, under MaxLineBytes): parsed, its pr= hint is kept.
	big := `{"id": 900, "ts": "2026-11-10T22:00:00Z", "actor": "x", "entity": "task", "entity_id": "T-0099", "action": "update", ` +
		`"detail": "status=in_review pr=https://github.com/o/r/pull/99 ` + strings.Repeat("x", 3<<19) + `"}` + "\n"
	// One line over MaxLineBytes: dropped without being held in memory, and the reader carries on.
	huge := strings.Repeat("y", MaxLineBytes+10) + "\n"
	after := `{"id": 901, "ts": "2026-11-10T23:00:00Z", "actor": "x", "entity": "task", "entity_id": "T-0100", "action": "update", ` +
		`"detail": "pr=https://github.com/o/r/pull/100"}` // no trailing newline
	if len(big) <= 1<<20 {
		t.Fatalf("test line is %d bytes, want over 1 MB", len(big))
	}
	ix, err := ReadEvents(strings.NewReader(fixture+big+huge+`{"ts": "not a time", "entity": "task"}`+"\n"+after),
		fixtureRepo, time.Time{})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	fixtureLines := strings.Count(fixture, "\n")
	if want := fixtureLines + 4; ix.Lines != want {
		t.Errorf("lines: got %d, want %d", ix.Lines, want)
	}
	// skipped: the non-JSON fixture line, the over-long line, the line with a bad ts.
	if ix.Skipped != 3 {
		t.Errorf("skipped: got %d, want 3", ix.Skipped)
	}
	if ix.PRTask[99] != "T-0099" {
		t.Errorf("1.5 MB line: PR 99 -> %q, want T-0099", ix.PRTask[99])
	}
	if ix.PRTask[100] != "T-0100" {
		t.Errorf("line after the over-long line: PR 100 -> %q, want T-0100", ix.PRTask[100])
	}
	if ix.PRTask[1] != "T-0001" || ix.PRTask[3] != "T-0003" {
		t.Errorf("fixture hints lost around the bad line: %v", ix.PRTask)
	}
	if got := ix.BranchTask["feature/x"]; got != "T-0002" {
		t.Errorf("pr-create branch hint: got %q, want T-0002", got)
	}
	if _, ok := ix.BranchTask["owner-patch"]; ok {
		t.Error("a pr-create of another repo must not map a branch")
	}
	if len(ix.Red) != 1 || !ix.Red[0].From.Equal(time.Date(2026, 11, 10, 17, 0, 0, 0, time.UTC)) ||
		!ix.Red[0].To.Equal(time.Date(2026, 11, 10, 19, 0, 0, 0, time.UTC)) {
		t.Errorf("red-main windows of o/r: got %v, want one 17:00..19:00", ix.Red)
	}

	t.Run("stage events before notBefore are not kept", func(t *testing.T) {
		ix, err := ReadEvents(strings.NewReader(fixture), fixtureRepo, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if n := len(ix.Events["T-0008"]); n != 2 {
			t.Errorf("T-0008 events kept: got %d, want 2", n)
		}
		if ix.TaskOrg["T-0008"] != "claude" {
			t.Errorf("org hint before notBefore: got %q, want claude", ix.TaskOrg["T-0008"])
		}
	})
	t.Run("scanLines memory is bounded by the line cap", func(t *testing.T) {
		var seen []int
		lines, tooLong, err := scanLines(strings.NewReader("ab\n"+strings.Repeat("z", 200000)+"\ncd\r\n\nef"), 100, func(b []byte) {
			if cap(b) > 70000 {
				t.Errorf("line buffer grew to %d bytes", cap(b))
			}
			seen = append(seen, len(strings.TrimSpace(string(b))))
		})
		if err != nil || lines != 5 || tooLong != 1 || jsonOf(t, seen) != "[2,2,0,2]" {
			t.Errorf("got lines=%d tooLong=%d seen=%v err=%v, want 5 1 [2,2,0,2] nil", lines, tooLong, seen, err)
		}
	})
}

func TestReport(t *testing.T) {
	rep := fixtureReport(t)
	if rep.PRs != 7 || rep.EventsSkipped != 1 {
		t.Errorf("prs=%d skipped=%d, want 7 1", rep.PRs, rep.EventsSkipped)
	}
	wantDays := []DayRow{{"2026-11-10", 5, 3, 1, 1}, {"2026-11-11", 0, 0, 0, 0}}
	if jsonOf(t, rep.MergedPerDay) != jsonOf(t, wantDays) {
		t.Errorf("merged per day: got %s, want %s", jsonOf(t, rep.MergedPerDay), jsonOf(t, wantDays))
	}
	wantStats := []MergeStats{
		{Org: "all", N: 5, Median: 6, P90: 8.4, Max: 10, Over6h: 1},
		{Org: "claude", N: 3, Median: 6, P90: 9.2, Max: 10, Over6h: 1},
		{Org: "cursor", N: 1, Median: 6, P90: 6, Max: 6, Over6h: 0},
		{Org: "other", N: 1, Median: 2, P90: 2, Max: 2, Over6h: 0},
	}
	if jsonOf(t, rep.OpenToMerge) != jsonOf(t, wantStats) {
		t.Errorf("open-to-merge:\n got %s\nwant %s", jsonOf(t, rep.OpenToMerge), jsonOf(t, wantStats))
	}
	wantOpen := []OpenPR{{Number: 4, AgeHours: 14, MergeStateStatus: "DIRTY", Stage: StageBlocked, Org: "cursor", Task: "T-0004"}}
	if jsonOf(t, rep.OpenOver4h) != jsonOf(t, wantOpen) || jsonOf(t, rep.OpenOver6h) != jsonOf(t, wantOpen) {
		t.Errorf("open lists: got 4h %s, 6h %s, want %s", jsonOf(t, rep.OpenOver4h), jsonOf(t, rep.OpenOver6h), jsonOf(t, wantOpen))
	}
	wantStages := []StageRow{
		{StageCIWait, 10, 2.5, 2.5, 5}, {StageReviewQA, 3, 1, 2, 0}, {StageCoderRework, 3.5, 2.5, 1, 0},
		{StageCapacityGate, 2, 2, 0, 0}, {StageJudge, 2, 1.5, 0.5, 0}, {StageHold, 7, 7, 0, 0},
		{StageRefresh, 1, 0, 1, 0}, {StageRedMain, 2, 2, 0, 0}, {StageRedPRUnowned, 0.5, 0.5, 0, 0},
		{StageBlocked, 13, 0, 13, 0},
	}
	if jsonOf(t, rep.StageHours) != jsonOf(t, wantStages) {
		t.Errorf("stage hours:\n got %s\nwant %s", jsonOf(t, rep.StageHours), jsonOf(t, wantStages))
	}
	if rep.ReviewerFail != 1 || rep.QAFail != 0 || rep.MultiRoundPRs != 1 || rep.Refreshes != 1 {
		t.Errorf("review counters: got %d %d %d %d, want 1 0 1 1", rep.ReviewerFail, rep.QAFail, rep.MultiRoundPRs, rep.Refreshes)
	}
	var slow []int
	for _, s := range rep.Slowest {
		slow = append(slow, s.Number)
	}
	if jsonOf(t, slow) != "[4,3,2,8,1,6,5]" {
		t.Errorf("slowest order: got %v", slow)
	}
	if s := rep.Slowest[0]; s.LargestStage != StageBlocked || s.LargestHours != 13 || s.State != "OPEN" {
		t.Errorf("slowest[0]: %+v", s)
	}

	text := rep.Text()
	for _, prefix := range []string{"merged per day", "open-to-merge", "open over 4 h: 1", "open over 6 h: 1", "stage hours",
		"review: reviewer FAIL 1, QA FAIL 0, PRs with 2+ review rounds 1, refreshes 1", "slowest PRs (top 7)"} {
		if !strings.Contains("\n"+text, "\n"+prefix) {
			t.Errorf("text output has no line starting %q:\n%s", prefix, text)
		}
	}
	for _, line := range []string{
		"  2026-11-10                  5       3       1       1",
		"  all                         5     6.0     8.4    10.0       1",
		"  #4     age   14.0 h  DIRTY      stage blocked        cursor T-0004",
		"  hold                      7.0     7.0     0.0     0.0",
		"  #5        2.0 h  MERGED other  -       largest stage ci_wait 2.0 h",
	} {
		if !strings.Contains(text, line+"\n") {
			t.Errorf("text output misses line %q:\n%s", line, text)
		}
	}
	if again := fixtureReport(t).Text(); again != text {
		t.Error("text output is not stable between two runs")
	}

	js, err := rep.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var back Report
	if err := json.Unmarshal([]byte(js), &back); err != nil {
		t.Fatalf("JSON output does not parse: %v", err)
	}
	if back.Text() != text {
		t.Errorf("--json does not carry the same numbers as the text output:\n%s\nvs\n%s", back.Text(), text)
	}
}

func TestParseSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"72h": 72 * time.Hour, "3d": 72 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := ParseSince(in); err != nil || got != want {
			t.Errorf("ParseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0h", "-2h", "d", "0d", "-1d", "1.5d", "abc", "+3d"} {
		if got, err := ParseSince(in); err == nil {
			t.Errorf("ParseSince(%q) = %v, want an error", in, got)
		}
	}
}

func TestOpenEvents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FACTORY_HOME", "")
	if _, err := OpenEvents(""); err == nil || !strings.Contains(err.Error(), "--events") {
		t.Errorf("no FACTORY_HOME: got %v, want an error naming --events", err)
	}
	t.Setenv("FACTORY_HOME", dir)
	if _, err := OpenEvents(""); err == nil || !strings.Contains(err.Error(), dir+"/state/events.jsonl") {
		t.Errorf("missing file: got %v, want an error naming the path", err)
	}
	if err := os.MkdirAll(dir+"/state", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/state/events.jsonl", []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := OpenEvents("")
	if err != nil {
		t.Fatalf("OpenEvents: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDetailParsers(t *testing.T) {
	prCases := []struct {
		detail string
		n      int
		ok     bool
	}{
		{"status=in_review pr=https://github.com/o/r/pull/12", 12, true},
		{"pr=https://github.com/o/r/pull/12/ — note", 12, true},
		{"pr=https://github.com/x/y/pull/12", 0, false},
		{"pr=https://github.com/o/r/pull/12abc", 0, false},
		{"pr=https://github.com/o/r/pull/-3", 0, false},
		{"see https://github.com/o/r/pull/12", 0, false},
		{"", 0, false},
	}
	for _, c := range prCases {
		if n, ok := prNumber(c.detail, "o/r"); n != c.n || ok != c.ok {
			t.Errorf("prNumber(%q) = %d, %v; want %d, %v", c.detail, n, ok, c.n, c.ok)
		}
	}
	agentCases := []struct{ name, org, task string }{
		{"claude-coder-t-1591", "claude", "T-1591"},
		{"cursor-coder-t-0330-fix2", "cursor", "T-0330"},
		{"coder-t-0012", "", "T-0012"},
		{"acme-coder-t-0012", "", "T-0012"},
		{"claude-reviewer-t-1591", "", ""},
		{"claude-coder-g-058", "", ""},
	}
	for _, c := range agentCases {
		org, task, ok := coderAgent(c.name)
		if org != c.org || task != c.task || ok != (c.task != "") {
			t.Errorf("coderAgent(%q) = %q, %q, %v; want %q, %q", c.name, org, task, ok, c.org, c.task)
		}
	}
	if got := jsonString(`{"kind": "ci", "n": 3}`, "kind"); got != "ci" {
		t.Errorf("jsonString kind: got %q", got)
	}
	for _, d := range []string{"", "plain", `{"kind": 3}`, `{"other": "x"}`, `{broken`, `["kind"]`} {
		if got := jsonString(d, "kind"); got != "" {
			t.Errorf("jsonString(%q) = %q, want empty", d, got)
		}
	}
}

func FuzzReadEvents(f *testing.F) {
	f.Add(`{"ts": "2026-11-10T10:00:00Z", "entity": "task", "entity_id": "T-1", "action": "update", "detail": "pr=https://github.com/o/r/pull/1"}`)
	f.Add("not json\n\n{\"ts\": 3}\n")
	f.Add(`{"ts": "2026-11-10T10:00:00Z", "entity": "redmain", "entity_id": "o/r", "action": "red-main-clear"}`)
	f.Add(`{"ts": "2026-11-10T10:00:00Z", "entity": "agent", "entity_id": "claude-coder-t-9", "action": "done", "detail": null}`)
	f.Fuzz(func(t *testing.T, in string) {
		ix, err := ReadEvents(strings.NewReader(in), "o/r", time.Time{})
		if err != nil {
			t.Fatalf("ReadEvents on an in-memory reader: %v", err)
		}
		if ix.Skipped > ix.Lines || ix.Lines > strings.Count(in, "\n")+1 {
			t.Fatalf("lines=%d skipped=%d for %d newlines", ix.Lines, ix.Skipped, strings.Count(in, "\n"))
		}
		t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
		for _, evs := range ix.Events {
			w := Stages(t0, t0.Add(48*time.Hour), evs, ix.Red)
			total := 0.0
			for _, h := range w.Hours() {
				if h < 0 {
					t.Fatalf("negative stage hours: %v", w.Hours())
				}
				total += h
			}
			if math.Abs(total-48) > 1e-6 {
				t.Fatalf("stage hours sum to %v, want 48", total)
			}
		}
	})
}

func FuzzDetail(f *testing.F) {
	f.Add("status=in_review pr=https://github.com/o/r/pull/12", "o/r")
	f.Add(`{"kind": "ci"}`, "o/r")
	f.Add("o/r factory/t-1", "o/r")
	f.Fuzz(func(t *testing.T, detail, repo string) {
		if n, ok := prNumber(detail, repo); ok && n <= 0 {
			t.Fatalf("prNumber(%q) = %d, true", detail, n)
		}
		if b, ok := prCreateBranch(detail, repo); ok && (b == "" || strings.ContainsAny(b, " \t\n")) {
			t.Fatalf("prCreateBranch(%q) = %q", detail, b)
		}
		jsonString(detail, "kind")
		coderAgent(detail)
	})
}

// redFixture is the T-1760 red-main fixture: repo a/b has red-main 11-04 10:00 and 12:00, a clear at 14:00 and a
// red-main on 11-05 09:00 with no clear; one red-main-unknown and one malformed line. Repo c/d has one event.
const redFixture = `{"ts": "2026-11-04T10:00:00Z", "entity": "redmain", "entity_id": "a/b", "action": "red-main"}
{"ts": "2026-11-04T12:00:00Z", "entity": "redmain", "entity_id": "a/b", "action": "red-main"}
{"ts": "2026-11-04T13:00:00Z", "entity": "redmain", "entity_id": "a/b", "action": "red-main-unknown"}
{"ts": "2026-11-04T14:00:00Z", "entity": "redmain", "entity_id": "a/b", "action": "red-main-clear"}
this line is malformed
{"ts": "2026-11-05T09:00:00Z", "entity": "redmain", "entity_id": "a/b", "action": "red-main"}
{"ts": "2026-11-05T10:00:00Z", "entity": "redmain", "entity_id": "c/d", "action": "red-main"}
`

var redNow = time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)

func redReport(t *testing.T, events, repo string, all bool) *Report {
	t.Helper()
	fakeGH(t)
	rep, err := Run(Options{Repo: repo, Since: 72 * time.Hour, Now: redNow, Events: strings.NewReader(events), RedMainAll: all})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

func TestRedMainCounts(t *testing.T) {
	rm := redReport(t, redFixture, "a/b", false).RedMain
	want := RedMain{Events: 3, EventsPerDay: 1, Windows: 2, RedHours: 28,
		PerDay: []RedDay{{"2026-11-04", 2}, {"2026-11-05", 1}}}
	if jsonOf(t, rm) != jsonOf(t, want) {
		t.Errorf("red main: got %s, want %s", jsonOf(t, rm), jsonOf(t, want))
	}
	text := redReport(t, redFixture, "a/b", false).Text()
	for _, line := range []string{"  events 3, events/day 1.00, windows 2, red hours 28.0", "    2026-11-04         2", "    2026-11-05         1"} {
		if !strings.Contains(text, line+"\n") {
			t.Errorf("text misses %q:\n%s", line, text)
		}
	}
}

func TestRedMainUnknownIsNotRed(t *testing.T) {
	only := `{"ts": "2026-11-05T10:00:00Z", "entity": "redmain", "entity_id": "a/b", "action": "red-main-unknown"}` + "\n"
	rm := redReport(t, only, "a/b", false).RedMain
	if rm.Events != 0 || rm.Windows != 0 || rm.RedHours != 0 || len(rm.PerDay) != 0 {
		t.Errorf("red-main-unknown counted as red: %+v", rm)
	}
}

func TestRedMainClipsToWindow(t *testing.T) {
	// window starts 11-03 09:00; a window opened 11-02 and cleared 11-03 12:00 counts 3 h, its event is outside.
	ev := `{"ts": "2026-11-02T10:00:00Z", "entity": "redmain", "entity_id": "a/b", "action": "red-main"}
{"ts": "2026-11-03T12:00:00Z", "entity": "redmain", "entity_id": "a/b", "action": "red-main-clear"}
`
	rm := redReport(t, ev, "a/b", false).RedMain
	if rm.Events != 0 || rm.Windows != 1 || rm.RedHours != 3 {
		t.Errorf("clipping: %+v, want 0 events, 1 window, 3 h", rm)
	}
}

// TestRedMainDayBuckets: the day is the date in the ts's own offset. It does not read the machine time zone, so the
// test passes under TZ=UTC and TZ=Asia/Tokyo.
func TestRedMainDayBuckets(t *testing.T) {
	ev := `{"ts": "2026-11-04T23:30:00+01:00", "entity": "redmain", "entity_id": "a/b", "action": "red-main"}
{"ts": "2026-11-05T00:30:00+01:00", "entity": "redmain", "entity_id": "a/b", "action": "red-main-clear"}
{"ts": "2026-11-05T00:45:00+01:00", "entity": "redmain", "entity_id": "a/b", "action": "red-main"}
`
	rm := redReport(t, ev, "a/b", false).RedMain
	want := []RedDay{{"2026-11-04", 1}, {"2026-11-05", 1}}
	if jsonOf(t, rm.PerDay) != jsonOf(t, want) {
		t.Errorf("per_day: got %s, want %s (UTC dates would be 2026-11-04 twice)", jsonOf(t, rm.PerDay), jsonOf(t, want))
	}
}

func TestRedMainRepoFilter(t *testing.T) {
	rep := redReport(t, redFixture, "a/b", false)
	if rep.RedMain.Events != 3 || len(rep.RedMainRepos) != 0 {
		t.Errorf("--repo a/b: events %d, repos %d, want 3 and none listed", rep.RedMain.Events, len(rep.RedMainRepos))
	}
	if strings.Contains(rep.Text(), "c/d") {
		t.Error("c/d shown without --red-main-all")
	}
	all := redReport(t, redFixture, "a/b", true)
	if len(all.RedMainRepos) != 2 || all.RedMainRepos[0].Repo != "a/b" || all.RedMainRepos[0].Events != 3 ||
		all.RedMainRepos[1].Repo != "c/d" || all.RedMainRepos[1].Events != 1 || all.RedMainRepos[1].Windows != 1 {
		t.Errorf("--red-main-all: %s", jsonOf(t, all.RedMainRepos))
	}
	if !strings.Contains(all.Text(), "red main repo c/d\n") {
		t.Errorf("text misses c/d:\n%s", all.Text())
	}
}

// TestRedMainSameWindowsAsStageMap: a PR that lies fully inside the red windows spends its judge time as red_main,
// and that time equals the red_hours of the same windows (the same window data, Index.RedRepos, feeds both).
func TestRedMainSameWindowsAsStageMap(t *testing.T) {
	ix, err := ReadEvents(strings.NewReader(redFixture), "a/b", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// PR opens 11-04 10:00 in judge and stays open to now: it spans both windows.
	created := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	ev := []TaskEvent{{At: created, Action: "judge-autospawn"}}
	w := Stages(created, redNow, ev, ix.Red)
	rm := redMainOf(ix.RedRepos["a/b"], created, redNow)
	if got := w.Hours()[StageRedMain]; math.Abs(got-rm.RedHours) > 0.001 || rm.RedHours != 28 {
		t.Errorf("stage map red_main %.2f h, red_hours %.2f h, want both 28", got, rm.RedHours)
	}
}

// TestExistingOutputUnchanged: the text up to the new section and every pre-T-1760 JSON key equal the golden files
// made with the code before the change.
func TestExistingOutputUnchanged(t *testing.T) {
	rep := fixtureReport(t)
	golden := string(readFixture(t, "report.text.golden"))
	text := rep.Text()
	if !strings.HasPrefix(text, golden) {
		t.Errorf("existing text lines changed:\n%s\nwant prefix:\n%s", text, golden)
	}
	if rest := strings.TrimPrefix(text, golden); !strings.HasPrefix(rest, "red main (") {
		t.Errorf("the added section does not follow the old text directly: %q", rest)
	}
	js, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readFixture(t, "report.json.golden"), &want); err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		var a, b any
		if json.Unmarshal(v, &a) != nil || json.Unmarshal(got[k], &b) != nil || jsonOf(t, a) != jsonOf(t, b) {
			t.Errorf("JSON key %q changed: got %s, want %s", k, got[k], v)
		}
	}
	for _, k := range []string{"events", "events_per_day", "windows", "red_hours", "per_day"} {
		var rm map[string]json.RawMessage
		if json.Unmarshal(got["red_main"], &rm) != nil || rm[k] == nil {
			t.Errorf("red_main.%s missing in %s", k, js)
		}
	}
}
