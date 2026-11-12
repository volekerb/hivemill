package cli

import (
	"reflect"
	"testing"
)

// TestStmtOrderPlan: cmd_plan (bin/factory_cli.py 3163-3174) = INSERT plans, UPDATE goals, INSERT events. No branch
// dies after a write, so there is no partial-write case.
func TestStmtOrderPlan(t *testing.T) {
	code, out, errs, trace := gwTraceRun(t, "plan", "claude-pm-1", "", "plan", "G-1", "body")
	if code != 0 || out != "G-1 plan v1\n" {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	want := []Stmt{{"INSERT", "plans"}, {"UPDATE", "goals"}, {"INSERT", "events"}}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("statements = %v, want %v", trace, want)
	}
	code, _, _, trace = gwTraceRun(t, "plan", "claude-pm-1", "", "plan", "G-1", "")
	if code != 1 || len(trace) != 0 {
		t.Fatalf("empty plan: exit %d, statements %v", code, trace)
	}
}

func TestParsePlan(t *testing.T) {
	for _, a := range [][]string{{"G-1", "-"}, {"G-1", "-1"}, {"G-1", ""}} {
		if _, ok := parsePlan(a); !ok {
			t.Errorf("%v should parse", a)
		}
	}
	for _, a := range [][]string{{"G-1"}, {"G-1", "-x"}, {"G-1", "a", "b"}, {"--", "x"}, {"G-1", "--"}} {
		if _, ok := parsePlan(a); ok {
			t.Errorf("%v should delegate", a)
		}
	}
}
