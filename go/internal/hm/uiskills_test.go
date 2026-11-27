package hm

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

// Each registered skill's install command links the directory the entry names. With HM_NET_TESTS set, that directory
// also holds a SKILL.md at the pinned commit upstream.
func TestSkillsRegistryPathsHaveSkillMd(t *testing.T) {
	raw, err := Asset("skills.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Skills []struct {
			Name, Repo, Commit, Path, Install string
		}
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	checked := 0
	client := &http.Client{Timeout: 30 * time.Second}
	for _, s := range reg.Skills {
		if s.Repo == "" {
			continue // a kit plugin has no install command
		}
		checked++
		if path.Base(s.Path) != s.Name {
			t.Errorf("%s: path %q does not end in the skill name", s.Name, s.Path)
		}
		if link := "ln -sn $d/" + s.Path + " ~/.claude/skills/" + s.Name; !strings.HasSuffix(s.Install, link) {
			t.Errorf("%s: install does not end with %q: %s", s.Name, link, s.Install)
		}
		if os.Getenv("HM_NET_TESTS") == "" {
			continue
		}
		url := strings.Replace(s.Repo, "https://github.com/", "https://raw.githubusercontent.com/", 1) +
			"/" + s.Commit + "/" + s.Path + "/SKILL.md"
		resp, err := client.Head(url)
		if err != nil {
			t.Errorf("%s: %v", s.Name, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %s: %s", s.Name, url, resp.Status)
		}
	}
	if checked == 0 {
		t.Error("the registry has no external skill")
	}
}

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
		if !strings.HasSuffix(k, "stop when the PR is open and CI is green.") {
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
