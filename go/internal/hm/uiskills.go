package hm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// uiRe finds the signs of a task that creates or changes a user interface: UI source files, template and obs page
// paths, and the words for them.
var uiRe = regexp.MustCompile(`(?i)\.(html?|gohtml|tmpl|css|scss|sass|less|jsx|tsx|vue|svelte|astro)\b|` +
	`\b(front-?end|dashboards?|user interface)\b|\btemplates/|assets/obs\b|\bobs(\s+\w+)?\s+pages?\b`)

// uiSkills are the skills of the built-in kit/skills.json whose use_when is about a user interface.
func uiSkills() []string {
	raw, err := Asset("skills.json")
	if err != nil {
		return nil
	}
	var reg struct {
		Skills []struct {
			Name    string `json:"name"`
			UseWhen string `json:"use_when"`
		} `json:"skills"`
	}
	if json.Unmarshal(raw, &reg) != nil {
		return nil
	}
	var names []string
	for _, s := range reg.Skills {
		if strings.Contains(s.UseWhen, "user interface") {
			names = append(names, s.Name)
		}
	}
	return names
}

// uiSkillNote is the kickoff paragraph of a task whose title, spec or criteria touch the user interface, else "".
func uiSkillNote(t *Task) string {
	names := uiSkills()
	if len(names) == 0 || !uiRe.MatchString(t.Title+"\n"+t.Spec+"\n"+t.Acceptance) {
		return ""
	}
	return fmt.Sprintf("\nThis task touches the user interface. Before you write any UI code, load these skills with the "+
		"Skill tool: %s (skip one that is not installed).\n", strings.Join(names, ", "))
}
