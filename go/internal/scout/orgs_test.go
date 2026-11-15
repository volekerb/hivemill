package scout

import "testing"

// TestCursorQAOnJudgementModel (T-1615, owner decision 2026-11-15): QA is part of the review gate, so cursor qa runs on
// the org's judgement model; the cursor coder and the claude qa keep their build models.
func TestCursorQAOnJudgementModel(t *testing.T) {
	for _, c := range []struct{ org, role, want string }{
		{"cursor", "qa", "grok-4.7-high"},
		{"cursor", "reviewer", "grok-4.7-high"},
		{"cursor", "coder", "composer-2.5"},
		{"claude", "qa", "claude-sonnet-5-5"},
		{"cursor", "cleaner", "composer-2.5"},
		{"cursor", "scout", "composer-2.5"},
		{"cursor", "retro", "composer-2.5"},
		{"cursor", "evaluator", "claude-sonnet-5-5"},
	} {
		got, err := Model(c.org, c.role)
		if err != nil || got != c.want {
			t.Errorf("Model(%s, %s) = %q, %v; want %q", c.org, c.role, got, err, c.want)
		}
	}
}
