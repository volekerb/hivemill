package hm

import (
	"strings"
	"testing"
)

const good = "1. the page has a title - cmd:grep -c '<title>' index.html => exit 0\n" +
	"2. unit test passes - test:test_x.XTests.test_title\n" +
	"3. looks right on a phone - manual: only the owner has the device"

func TestCriteria(t *testing.T) {
	c := Criteria(good)
	if len(c) != 3 || c[0].Kind != "cmd" || c[1].Kind != "test" || c[2].Kind != "manual" {
		t.Fatalf("%+v", c)
	}
	if c[0].Proof != "grep -c '<title>' index.html" || c[0].Expect != "exit 0" {
		t.Errorf("cmd proof %q expect %q", c[0].Proof, c[0].Expect)
	}
	if p := LintProblems(good); len(p) != 0 {
		t.Errorf("lint %v", p)
	}
	inline := Criteria("Run from a clone. 1) a works test:test_a 2) b works cmd:true => exit 0")
	if len(inline) != 2 || inline[0].Proof != "test_a" || inline[1].Proof != "true" || inline[1].Expect != "exit 0" {
		t.Errorf("inline %+v", inline)
	}
	if Criteria("1. ran pytest: fine")[0].Kind != "" {
		t.Error("pytest: is not a test: proof")
	}
	dash := Criteria("- a cmd:true => 0\n- b manual: eyes")
	if len(dash) != 2 || dash[1].Kind != "manual" {
		t.Errorf("dash list %+v", dash)
	}
}

func TestLint(t *testing.T) {
	for in, want := range map[string]string{
		"":                                   "no acceptance criteria",
		"1. CI green\n2. x test:t_a":         "criterion 1 names no proof",
		"1. x cmd:true":                      "needs `=>",
		"1. x cmd:true => 0\n1. y manual: z": "numbered twice",
	} {
		p := LintProblems(in)
		if len(p) == 0 || !strings.Contains(strings.Join(p, ";"), want) {
			t.Errorf("%q: %v, want %q", in, p, want)
		}
	}
}

func TestExpect(t *testing.T) {
	for _, c := range []struct {
		exp  string
		rc   int
		out  string
		want bool
	}{{"exit 0", 0, "", true}, {"0", 1, "", false}, {"3", 0, "3\n", true}, {"2", 0, "1", false}, {"rc=1", 1, "", true}, {"empty", 0, " \n", true}, {"OK", 0, "Ran 3\nOK", true}, {"OK", 0, "FAILED", false}} {
		if ok, _ := ExpectOK(c.exp, c.rc, c.out); ok != c.want {
			t.Errorf("%+v: got %v", c, ok)
		}
	}
}

func TestKickoffCarriesCriterionText(t *testing.T) {
	k := Kickoff(&Run{Agent: "a", Branch: "b", Base: "main"}, &Task{ID: "T-1", Title: "t", Spec: "s",
		Acceptance: "1. start ctx is 33000 for a transcript with cache read 30000 - cmd:go test -run TestStartCtx => ok"})
	if !strings.Contains(k, "start ctx is 33000") || !strings.Contains(k, "cmd:go test -run TestStartCtx => ok") {
		t.Errorf("kickoff lost the criterion text or its proof: %q", k)
	}
}
