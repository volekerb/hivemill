package hm

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestDecideFixtures runs the merge decision against the edge cases of the old merger's tests (testdata/merge_cases.json).
func TestDecideFixtures(t *testing.T) {
	b, err := os.ReadFile("testdata/merge_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name   string     `json:"name"`
			Input  MergeInput `json:"input"`
			Action string     `json:"action"`
			WhyHas string     `json:"why_has"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) < 20 {
		t.Fatalf("only %d cases", len(doc.Cases))
	}
	for _, c := range doc.Cases {
		d := Decide(c.Input)
		if d.Action != c.Action || !strings.Contains(d.Why, c.WhyHas) {
			t.Errorf("%s: got %s (%s), want %s (%s)", c.Name, d.Action, d.Why, c.Action, c.WhyHas)
		}
	}
}

func TestCIVerdict(t *testing.T) {
	run := func(status, concl, started string) map[string]any {
		return map[string]any{"__typename": "CheckRun", "workflowName": "tests", "name": "unit", "status": status, "conclusion": concl,
			"startedAt": started, "completedAt": started}
	}
	if v, _ := CIVerdict(nil); v != "pending" {
		t.Errorf("no checks = %s, want pending", v)
	}
	if v, _ := CIVerdict([]map[string]any{run("COMPLETED", "SUCCESS", "1")}); v != "green" {
		t.Errorf("success = %s", v)
	}
	if v, f := CIVerdict([]map[string]any{run("COMPLETED", "FAILURE", "1")}); v != "red" || f[0] != "unit" {
		t.Errorf("failure = %s %v", v, f)
	}
	// a re-run supersedes the failed attempt on the same head
	if v, _ := CIVerdict([]map[string]any{run("COMPLETED", "FAILURE", "1"), run("COMPLETED", "SUCCESS", "2")}); v != "green" {
		t.Errorf("rerun = %s, want green", v)
	}
	if v, _ := CIVerdict([]map[string]any{run("IN_PROGRESS", "", "")}); v != "pending" {
		t.Errorf("in progress = %s", v)
	}
	if v, _ := CIVerdict([]map[string]any{run("COMPLETED", "CANCELLED", "1")}); v != "pending" {
		t.Errorf("cancelled = %s, want pending (never green, never red)", v)
	}
	goCancelled := run("COMPLETED", "CANCELLED", "1")
	goCancelled["name"] = "go"
	if v, _ := CIVerdict([]map[string]any{run("COMPLETED", "SUCCESS", "1"), goCancelled}); v != "pending" {
		t.Errorf("one cancelled among green = %s, want pending", v)
	}
	c := run("COMPLETED", "CANCELLED", "2")
	c["detailsUrl"] = "https://github.com/o/r/actions/runs/42/job/7"
	if ids := ciCancelledRuns([]map[string]any{run("COMPLETED", "FAILURE", "1"), c}); len(ids) != 1 || ids[0] != "42" {
		t.Errorf("cancelled runs = %v, want [42]", ids)
	}
	if ids := ciCancelledRuns([]map[string]any{c, run("COMPLETED", "SUCCESS", "3")}); len(ids) != 0 {
		t.Errorf("a newer attempt supersedes the cancelled one: %v", ids)
	}
}

func TestNoreplyBeforeAndAfterMerge(t *testing.T) {
	r, s := testRunner(t, "")
	_, _ = s.db.Exec(`INSERT INTO config(key,value) VALUES('noreply_email','1+me@users.noreply.github.com')`)
	_, _ = s.db.Exec(`CREATE TABLE owner_alert_feed (id INTEGER PRIMARY KEY AUTOINCREMENT, org TEXT NOT NULL, kind TEXT NOT NULL,
		ref TEXT NOT NULL, since TEXT NOT NULL, link TEXT NOT NULL, first_seen TEXT NOT NULL, closed TEXT, sent TEXT)`)
	if !r.isNoreply("1+ME@users.noreply.github.com") || r.isNoreply("2+other@users.noreply.github.com") || r.isNoreply("me@example.com") {
		t.Error("only the configured noreply_email passes")
	}
	run := &Run{Task: "T-0001", Repo: "zz-test/www", MergeSHA: "abc"}
	for _, c := range []struct {
		out  string
		want int
	}{{"1+me@users.noreply.github.com noreply@github.com", 0}, {"me@example.com noreply@github.com", 1}} {
		out := c.out
		r.Tools.GH = func(string, ...string) (int, string, string) { return 0, out, "" }
		r.checkMergeCommit(run)
		var n int
		_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='hivemill-noreply'`).Scan(&n)
		if n != c.want {
			t.Errorf("%q: %d alerts, want %d", c.out, n, c.want)
		}
	}
}

// gh can exit non-zero after the merge went through (on the branch delete): GitHub's state decides.
func TestMergeNowConfirmsMerged(t *testing.T) {
	f := newT3(t, t3Clean)
	gh := f.r.Tools.GH
	f.r.Tools.GH = func(dir string, args ...string) (int, string, string) {
		rc, out, errs := gh(dir, args...)
		if len(args) > 1 && args[0] == "pr" && args[1] == "merge" {
			return 1, "", "zz: cannot delete the branch"
		}
		return rc, out, errs
	}
	f.r.Merge(f.run)
	if f.run.State != Merged || f.run.MergeHold != "" || len(f.merges) != 1 {
		t.Fatalf("state %s (%s), merges %v", f.run.State, f.run.MergeHold, f.merges)
	}
}
