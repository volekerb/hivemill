package hm

import (
	"strings"
	"testing"
)

func TestKickoffNamesTheUISkillsForAUIScopedSpec(t *testing.T) {
	run := &Run{Agent: "a", Branch: "b", Base: "main"}
	for _, spec := range []string{
		"FILES: go/internal/hm/assets/obs/flow.html", "restyle the header in app.css", "change the frontend",
		"add a banner to the obs Flow page", "a new dashboard for spend", "edit templates/mail.txt",
		"web/App.tsx shows the total",
		"obs Flow rule-6 largest-stage banner", "FILES: go/internal/hm/obs_pages.go",
	} {
		k := Kickoff(run, &Task{ID: "T-1", Title: "t", Spec: spec})
		for _, want := range []string{"ui-ux-pro-max", "taste-skill", "impeccable", "Before you write any UI code"} {
			if !strings.Contains(k, want) {
				t.Errorf("%q: kickoff lacks %q:\n%s", spec, want, k)
			}
		}
		if !strings.HasSuffix(k, "push, open the PR and stop; the runner watches CI.") {
			t.Errorf("%q: the closing line must stay last (eval.go trims it):\n%s", spec, k)
		}
	}
}

func TestKickoffLeavesOtherSpecsAlone(t *testing.T) {
	for _, spec := range []string{
		"fix the merge queue order in go/internal/hm/run.go", "copy kit/template into a temp repo",
		"observe mode accepts done", "page through the API results",
	} {
		k := Kickoff(&Run{Agent: "a", Branch: "b", Base: "main"}, &Task{ID: "T-1", Title: "t", Spec: spec})
		if strings.Contains(k, "impeccable") {
			t.Errorf("%q: kickoff names the UI skills:\n%s", spec, k)
		}
	}
}

// The planner and task role prompts tell the sessions about the same skills.
func TestRolePromptsNameTheUISkills(t *testing.T) {
	for _, asset := range []string{"planner.md", "task.md"} {
		b, err := Asset(asset)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range uiSkills() {
			if !strings.Contains(string(b), name) {
				t.Errorf("%s does not name %s", asset, name)
			}
		}
	}
	if len(uiSkills()) != 3 {
		t.Errorf("UI skills: %v", uiSkills())
	}
}
