// PARTIAL for P7a (T-1495): see the 'not ported' lines below; keep the Python module until they have Go counterparts.
package s2msgs

import (
	"reflect"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_rejection_cause.py (6 cases).
// not ported: TestRejectionCauseCounts — uses record_rejection_incident/rejection_cause_counts in-process
// (FactoryTestCase.INPROC); no Go counterpart until P7a.

func newRejectionCauseEnv(t *testing.T) *ft.Env {
	t.Helper()
	t.Parallel()
	e := ft.New(t)
	seedProject(e, "zz-test-proj")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTask(ft.Task{})
	seedPRView(t, e, prStubOpts{})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-qa", Role: "qa", Goal: "G-001", Task: "T-0001"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-reviewer", Role: "reviewer", Goal: "G-001", Task: "T-0001"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-judge", Role: "judge", Goal: "G-001", Task: "T-0001", Status: "exited"})
	return e
}

func TestRejectionCauseVerdictFailWithoutCauseRefused(t *testing.T) {
	e := newRejectionCauseEnv(t)
	before := e.Count("SELECT count(*) FROM verdicts")
	r := failAs(e, "zz-test-qa", 2, "verdict", "T-0001", "--fail", "--summary", "bad")
	contains(t, "stderr", r.Stderr, "pass --cause")
	if got := e.Count("SELECT count(*) FROM verdicts"); got != before {
		t.Fatalf("verdicts = %d, want %d unchanged", got, before)
	}
}

func TestRejectionCauseVerdictFailUnknownCauseRefused(t *testing.T) {
	e := newRejectionCauseEnv(t)
	before := e.Count("SELECT count(*) FROM rejection_incidents")
	r := failAs(e, "zz-test-reviewer", 2, "verdict", "T-0001", "--fail", "--cause", "not_a_real_cause", "--summary", "bad")
	contains(t, "stderr", r.Stderr, "unknown rejection cause")
	if got := e.Count("SELECT count(*) FROM rejection_incidents"); got != before {
		t.Fatalf("rejection_incidents = %d, want %d unchanged", got, before)
	}
}

func TestRejectionCauseVerdictFailWithCauseRecordsIncident(t *testing.T) {
	e := newRejectionCauseEnv(t)
	r := okAs(e, "zz-test-qa", "verdict", "T-0001", "--fail", "--cause", "bug", "--summary", "bad")
	contains(t, "stdout", r.Stdout, "FAIL")
	rows := e.SQL("SELECT cause, kind, task_id FROM rejection_incidents")
	want := []map[string]string{{"cause": "bug", "kind": "qa", "task_id": "T-0001"}}
	got := make([]map[string]string, len(rows))
	for i, r := range rows {
		got[i] = map[string]string{"cause": r[0], "kind": r[1], "task_id": r[2]}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rejection_incidents = %v, want %v", got, want)
	}
}

func TestRejectionCauseJudgeRejectWithoutCauseRefused(t *testing.T) {
	e := newRejectionCauseEnv(t)
	e.NeedPython()
	for _, pair := range [][2]string{{"reviewer", "zz-test-reviewer"}, {"qa", "zz-test-qa"}} {
		okAs(e, pair[1], "verdict", "T-0001", "--pass", "--summary", gatesSummary())
	}
	before := e.Count("SELECT count(*) FROM verdicts WHERE role='judge'")
	r := failAs(e, "zz-test-judge", 2, "judge", "T-0001", "--reject", "--reason", "nope")
	contains(t, "stderr", r.Stderr, "pass --cause")
	if got := e.Count("SELECT count(*) FROM verdicts WHERE role='judge'"); got != before {
		t.Fatalf("judge verdicts = %d, want %d unchanged", got, before)
	}
}

func TestRejectionCauseGateRefusalRecordsIncident(t *testing.T) {
	e := newRejectionCauseEnv(t)
	e.NeedPython()
	e.UseGitHead()
	head := e.HeadSHA
	seedPRView(t, e, prStubOpts{sha: head})
	for _, pair := range [][2]string{{"reviewer", "zz-test-reviewer"}, {"qa", "zz-test-qa"}} {
		okAs(e, pair[1], "verdict", "T-0001", "--pass", "--summary", gatesSummary(), "--sha", head)
	}
	seedPRView(t, e, prStubOpts{sha: head, checks: []map[string]string{
		{"name": "build", "status": "COMPLETED", "conclusion": "FAILURE"},
	}})
	failAs(e, "zz-test-judge", 4, "judge", "T-0001", "--merge", "--reason", "ok")
	rows := e.SQL("SELECT cause, kind FROM rejection_incidents WHERE kind='gate'")
	if len(rows) != 1 {
		t.Fatalf("gate incidents = %d rows, want 1", len(rows))
	}
	cause := rows[0][0]
	for _, ok := range []string{"missing_test", "gate_refusal", "stale_or_conflicting_branch"} {
		if cause == ok {
			return
		}
	}
	t.Fatalf("gate cause = %q, want one of missing_test, gate_refusal, stale_or_conflicting_branch", cause)
}

func TestRejectionCauseCounts(t *testing.T) {
	t.Skip("not ported: in-process rejection_cause_counts (see tests/test_rejection_cause.py test_rejection_cause_counts)")
}
