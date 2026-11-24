package hm

import "testing"

func TestJudgeEffort(t *testing.T) {
	r, s := testRunner(t, "")
	if e, err := r.JudgeEffort("medium"); err != nil || e != "medium" {
		t.Errorf("default follows the task effort: %q %v", e, err)
	}
	_ = s.SetCfg("judge_effort", "high")
	if e, err := r.JudgeEffort("medium"); err != nil || e != "high" {
		t.Errorf("judge_effort high: %q %v", e, err)
	}
	_ = s.SetCfg("judge_effort", "fast")
	if _, err := r.JudgeEffort("medium"); err == nil {
		t.Error("a bad value is refused")
	}
}
