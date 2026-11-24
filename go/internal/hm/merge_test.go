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
	if v, _ := CIVerdict([]map[string]any{run("COMPLETED", "CANCELLED", "1")}); v != "red" {
		t.Errorf("cancelled = %s, want red (never green)", v)
	}
}
