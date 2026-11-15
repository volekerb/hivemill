package s2msgs

import (
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_verdict_gates.py (16 cases).
// not ported: TestVerdictGatesParseEndsAtNextHeadingAndIgnoresBlank, TestVerdictGatesParseKeepsCmdWithEqualsAndSpaces —
// parse_gates unit tests; parity in go/internal/cli/cmd_verdict_test.go TestParseGates and indirect coverage here.

func newVerdictGatesEnv(t *testing.T) *ft.Env {
	t.Helper()
	t.Parallel()
	e := ft.New(t)
	seedProject(e, "zz-test-proj")
	e.SeedGoal("G-001", "zz-test-proj", "zz-test goal")
	e.SeedTask(ft.Task{})
	seedPRView(t, e, prStubOpts{})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-qa", Role: "qa", Goal: "G-001", Task: "T-0001"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-reviewer", Role: "reviewer", Goal: "G-001", Task: "T-0001"})
	return e
}

func TestVerdictGatesQaPassWithGatesRecorded(t *testing.T) {
	e := newVerdictGatesEnv(t)
	okAs(e, "zz-test-qa", "verdict", "T-0001", "--pass", "--summary", "all ok\n\n"+goodGates)
	row := e.SQL("SELECT head_sha, role, verdict FROM verdicts")
	if len(row) != 1 {
		t.Fatalf("verdicts = %v, want one row", row)
	}
	if row[0][0] != shaA || row[0][1] != "qa" || row[0][2] != "pass" {
		t.Fatalf("verdict row = %v %v %v, want %s qa pass", row[0][0], row[0][1], row[0][2], shaA)
	}
}

func TestVerdictGatesQaPassNoSection(t *testing.T) {
	e := newVerdictGatesEnv(t)
	assertGateRefused(t, e, "zz-test-qa", "ok", nil, "missing")
}

func TestVerdictGatesQaPassEmptySection(t *testing.T) {
	e := newVerdictGatesEnv(t)
	assertGateRefused(t, e, "zz-test-qa", "ok\n## Gates\n\n## Other\n- x exit=0 cmd=y\n", nil, "no gate lines")
}

func TestVerdictGatesQaPassMalformedLine(t *testing.T) {
	e := newVerdictGatesEnv(t)
	assertGateRefused(t, e, "zz-test-qa", "## Gates\n- unittest ok cmd=x\n", nil, "line 2 malformed")
}

func TestVerdictGatesQaPassDuplicateName(t *testing.T) {
	e := newVerdictGatesEnv(t)
	assertGateRefused(t, e, "zz-test-qa", goodGates+"- unittest exit=0 cmd=again\n", nil, "duplicate name unittest")
}

func TestVerdictGatesQaPassNonzeroExit(t *testing.T) {
	e := newVerdictGatesEnv(t)
	assertGateRefused(t, e, "zz-test-qa", "## Gates\n- unittest exit=1 cmd=x\n", nil, "gate unittest exit=1")
}

func TestVerdictGatesQaPassUnknownExit(t *testing.T) {
	e := newVerdictGatesEnv(t)
	assertGateRefused(t, e, "zz-test-qa", "## Gates\n- unittest exit=unknown cmd=x\n", nil, "exit=unknown")
}

func TestVerdictGatesQaPassTwoSections(t *testing.T) {
	e := newVerdictGatesEnv(t)
	assertGateRefused(t, e, "zz-test-qa", goodGates+"\n## Gates\n- lint exit=0 cmd=ruff\n", nil, "2 '## Gates' sections")
}

func TestVerdictGatesQaFailWithoutSectionRecorded(t *testing.T) {
	e := newVerdictGatesEnv(t)
	okAs(e, "zz-test-qa", "verdict", "T-0001", "--fail", "--cause", "bug", "--summary", "broken")
	v, _ := verdictCounts(e)
	if v != 1 {
		t.Fatalf("verdict count = %d, want 1", v)
	}
}

func TestVerdictGatesReviewerPassWithoutSectionRecorded(t *testing.T) {
	e := newVerdictGatesEnv(t)
	okAs(e, "zz-test-reviewer", "verdict", "T-0001", "--pass", "--summary", "lgtm")
	rows := e.SQL("SELECT role FROM verdicts")
	if len(rows) != 1 || rows[0][0] != "reviewer" {
		t.Fatalf("verdict role = %v, want reviewer", rows)
	}
}

func TestVerdictGatesReviewerPassWithValidSectionRecorded(t *testing.T) {
	e := newVerdictGatesEnv(t)
	okAs(e, "zz-test-reviewer", "verdict", "T-0001", "--pass", "--summary", goodGates)
}

func TestVerdictGatesReviewerPassMalformedSectionRefused(t *testing.T) {
	e := newVerdictGatesEnv(t)
	assertGateRefused(t, e, "zz-test-reviewer", "lgtm\n## Gates\n- bad line\n", nil, "malformed")
}

func TestVerdictGatesOwnerAsQaFollowsQaRules(t *testing.T) {
	e := newVerdictGatesEnv(t)
	assertGateRefused(t, e, "owner", "ok", []string{"--as", "qa"}, "missing")
	okAs(e, "owner", "verdict", "T-0001", "--pass", "--summary", gatesSummary(), "--as", "qa")
}

func TestVerdictGatesOwnerAsReviewerOptional(t *testing.T) {
	e := newVerdictGatesEnv(t)
	okAs(e, "owner", "verdict", "T-0001", "--pass", "--summary", "ok", "--as", "reviewer")
	assertGateRefused(t, e, "owner", "## Gates\n- x exit=2 cmd=y\n", []string{"--as", "reviewer"}, "")
}

func TestVerdictGatesParseEndsAtNextHeadingAndIgnoresBlank(t *testing.T) {
	t.Skip("not ported: parse_gates unit case; see go/internal/cli/cmd_verdict_test.go TestParseGates")
}

func TestVerdictGatesParseKeepsCmdWithEqualsAndSpaces(t *testing.T) {
	t.Skip("not ported: parse_gates unit case; see go/internal/cli/cmd_verdict_test.go TestParseGates")
}
