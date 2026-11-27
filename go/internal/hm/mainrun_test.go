package hm

import "testing"

func mainRunTools(state string) Tools {
	return Tools{
		Git: func(string, ...string) (int, string, string) { return 0, "headsha", "" },
		GH: func(_ string, args ...string) (int, string, string) {
			if len(args) > 0 && args[0] == "api" {
				return 0, state + "\n", ""
			}
			// an older head has a run, main head has none
			return 0, `[{"status":"completed","conclusion":"success","headSha":"oldsha"}]`, ""
		},
	}
}

func TestMainRunDisabledWorkflowIsNone(t *testing.T) {
	r := &Runner{Tools: mainRunTools("disabled_manually")}
	if got := r.mainRun("o/r", "/wt", "main"); got != "none" {
		t.Fatalf("disabled tests.yml, no run for main head: got %q, want none", got)
	}
}

func TestMainRunActiveWorkflowStaysPending(t *testing.T) {
	r := &Runner{Tools: mainRunTools("active")}
	if got := r.mainRun("o/r", "/wt", "main"); got != "pending" {
		t.Fatalf("active tests.yml, no run yet for main head: got %q, want pending", got)
	}
	if workflowDisabled(Tools{GH: func(string, ...string) (int, string, string) { return 1, "", "boom" }}, "o/r", "tests.yml") {
		t.Fatal("a gh error must not count as disabled")
	}
}
