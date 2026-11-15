package spawn

import (
	"fmt"
	"path/filepath"
	"strings"
)

// OrgSection is org_prompt_section(org, multi): appended to the prompt of an agent in a non-default org, or of every
// agent once more than one org is enabled. A single-org factory keeps the plain role prompt ("").
func OrgSection(org string, multi bool) string {
	if org == "" {
		org = DefaultOrg
	}
	if org == DefaultOrg && !multi {
		return ""
	}
	var others []string
	for _, o := range OrgNames {
		if o != org {
			others = append(others, o)
		}
	}
	lines := []string{
		"",
		"## Your org",
		fmt.Sprintf("You belong to the **%s org** (%s): runtime %s. Every agent name in your org "+
			"starts with `%s-`. Your boss is `%s-boss`, your director is `%s-director`; `factory msg boss|director|owner` "+
			"reaches your own org's HQ.", OrgLabel(org), org, OrgRuntime(org), org, org, org),
		fmt.Sprintf("Models in your org (these override any model rule above): judgement roles %s, "+
			"build roles %s. Subagents you start use the same models. Never use Fable.",
			OrgModel(org, "director"), OrgModel(org, "coder")),
	}
	if len(others) > 0 {
		var labels []string
		for _, o := range others {
			labels = append(labels, fmt.Sprintf("%s (`%s-*`)", OrgLabel(o), o))
		}
		lines = append(lines, "Other orgs on this box: "+strings.Join(labels, ", ")+". "+
			"They share the box capacity (one agent cap; P1 goals first across orgs) and the repos. Only the boss and "+
			"the director talk across orgs (e.g. `factory msg "+others[0]+"-director ...` for capacity, file "+
			"conflicts or red main); leads and workers never do.")
	}
	return strings.Join(lines, "\n") + "\n"
}

// Brief is role_prompt_text(): the text of run/<name>.prompt.md, assembled from the already read texts of
// roles/_common.md and roles/<role>.md.
func Brief(common, roleText, org string, multi bool) string {
	return common + "\n\n" + roleText + OrgSection(org, multi)
}

// BriefFromDir reads roles/_common.md and roles/<role>.md from rolesDir (Path.read_text semantics) and assembles the
// brief. A missing or undecodable file is an error naming the path.
func BriefFromDir(rolesDir, role, org string, multi bool) (string, error) {
	common, err := ReadText(filepath.Join(rolesDir, "_common.md"))
	if err != nil {
		return "", fmt.Errorf("role brief: %w", err)
	}
	rt, err := ReadText(filepath.Join(rolesDir, role+".md"))
	if err != nil {
		return "", fmt.Errorf("role brief: %w", err)
	}
	return Brief(common, rt, org, multi), nil
}

// Kickoff is kickoff_text(): the first prompt of an agent. extra is the (already assembled) caller prompt.
func Kickoff(role, name, goal, team, task, extra string) string {
	lines := []string{fmt.Sprintf("You are `%s`, the factory %s. Run `factory whoami` and `factory inbox` first.", name, role)}
	if task != "" {
		lines = append(lines, fmt.Sprintf("Your task is %s: read it with `factory task show %s`.", task, task))
	}
	if goal != "" {
		lines = append(lines, fmt.Sprintf("Goal context: `factory goal show %s`.", goal))
	}
	if team != "" {
		lines = append(lines, fmt.Sprintf("Your team is %s.", team))
	}
	if extra != "" {
		lines = append(lines, extra)
	}
	return strings.Join(lines, " ")
}
