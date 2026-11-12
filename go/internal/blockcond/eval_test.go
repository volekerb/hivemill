package blockcond

import (
	"strings"
	"testing"
)

func TestEvalGoalReleasedCapped(t *testing.T) {
	rels := make([]ReleaseFacts, releaseListCap)
	for i := range rels {
		rels[i] = ReleaseFacts{Tag: "v0.0.1", PublishedAt: "2020-01-01T00:00:00Z"}
	}
	f := Facts{
		Now: "2026-11-12T00:00:00Z",
		Goals: map[string]GoalFacts{
			"G-040": {Status: "done", ClosedAt: "2026-11-11T00:00:00Z"},
		},
		Releases:   rels,
		Conditions: []Cond{{Type: "goal", Arg: "G-040", Form: "released"}},
	}
	out, err := Eval(f)
	if err != nil {
		t.Fatal(err)
	}
	if out.Results[0].Met || out.Results[0].Detail != "release-list-capped" {
		t.Fatalf("got met=%v detail=%q", out.Results[0].Met, out.Results[0].Detail)
	}
}

func TestEvalUnknownCheckProbe(t *testing.T) {
	f := Facts{
		Now:        "2026-11-12T00:00:00Z",
		Conditions: []Cond{{Type: "check", Arg: "evil-probe"}},
	}
	_, err := Eval(f)
	if err == nil || !strings.Contains(err.Error(), "unknown probe") {
		t.Fatalf("Eval: got %v", err)
	}
}

func TestEvalAllMetNeedsNonempty(t *testing.T) {
	out, err := Eval(Facts{Now: "2026-11-12T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if out.AllMet {
		t.Fatal("empty conditions must not set all_met")
	}
}
