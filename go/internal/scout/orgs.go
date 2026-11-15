package scout

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Port of scout/orgs.py: the organisations of one Hivemill, their runtimes and model maps.

const DefaultOrg = "claude"

var (
	JudgementRoles = []string{"boss", "director", "lead", "pm", "reviewer", "judge", "staff-engineer", "researcher"}
	BuildRoles     = []string{"coder", "qa", "cleaner", "scout", "retro", "evaluator"}
	SingletonRoles = []string{"boss", "director", "staff-engineer"}
)

// Org is one entry of ORGS.
type Org struct {
	Label   string
	Runtime string
	Models  map[string]string
}

// Orgs is ORGS. Never Fable (owner rule).
var Orgs = map[string]Org{
	"claude": {Label: "Claude", Runtime: "claude", Models: map[string]string{
		"boss": "claude-opus-5-5", "director": "claude-opus-5-5", "lead": "claude-opus-5-5",
		"pm": "claude-opus-5-5", "reviewer": "claude-opus-5-5", "judge": "claude-opus-5-5",
		"staff-engineer": "claude-opus-5-5", "researcher": "claude-opus-5-5",
		"coder": "claude-sonnet-5-5", "qa": "claude-sonnet-5-5", "cleaner": "claude-haiku-5-5",
		"scout": "claude-sonnet-5-5", "retro": "claude-sonnet-5-5", "evaluator": "claude-sonnet-5-5",
	}},
	"cursor": {Label: "Cursor", Runtime: "cursor", Models: map[string]string{
		"boss": "grok-4.7-high", "director": "grok-4.7-high", "lead": "grok-4.7-high",
		"pm": "grok-4.7-high", "reviewer": "grok-4.7-high", "judge": "grok-4.7-high",
		"staff-engineer": "grok-4.7-high", "researcher": "grok-4.7-high",
		"coder": "composer-2.5", "qa": "composer-2.5", "cleaner": "composer-2.5",
		"scout": "composer-2.5", "retro": "composer-2.5",
		"evaluator": "claude-sonnet-5-5",
	}},
}

// OrgNames is NAMES in definition order.
var OrgNames = []string{"claude", "cursor"}

var prefixRe = regexp.MustCompile(`^(claude|cursor)-`)

// CheckOrg validates an org name; "" means the default org.
func CheckOrg(org string) (string, error) {
	org = strings.TrimSpace(org)
	if org == "" {
		org = DefaultOrg
	}
	if _, ok := Orgs[org]; !ok {
		return "", fmt.Errorf("unknown org %q; known: %s", org, strings.Join(OrgNames, ", "))
	}
	return org, nil
}

func Runtime(org string) (string, error) {
	o, err := CheckOrg(org)
	if err != nil {
		return "", err
	}
	return Orgs[o].Runtime, nil
}

func Model(org, role string) (string, error) {
	o, err := CheckOrg(org)
	if err != nil {
		return "", err
	}
	m, ok := Orgs[o].Models[role]
	if !ok {
		return "", fmt.Errorf("no model for role %q in org %q", role, o)
	}
	return m, nil
}

func Label(org string) (string, error) {
	o, err := CheckOrg(org)
	if err != nil {
		return "", err
	}
	return Orgs[o].Label, nil
}

// PrefixOf is prefix_of: "cursor" for "cursor-coder-t-1", "" for a legacy unprefixed name.
func PrefixOf(name string) string {
	m := prefixRe.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return m[1]
}

// StripPrefix is strip_prefix.
func StripPrefix(name string) string { return prefixRe.ReplaceAllString(name, "") }

// OrgName is name(): the org-prefixed agent name, idempotent.
func OrgName(org, base string) (string, error) {
	o, err := CheckOrg(org)
	if err != nil {
		return "", err
	}
	if PrefixOf(base) != "" {
		return base, nil
	}
	return o + "-" + base, nil
}

// AgentRow is the part of an agents row OfAgent reads.
type AgentRow struct {
	Org       string
	Name      string
	Runtime   string
	SpawnArgs string
}

// OfAgent is of_agent: the org column when set, else the name prefix, else the runtime recorded in spawn_args
// (legacy rows), else the default org.
func OfAgent(row AgentRow) string {
	if _, ok := Orgs[row.Org]; ok {
		return row.Org
	}
	if p := PrefixOf(row.Name); p != "" {
		return p
	}
	rt := row.Runtime
	if rt == "" {
		var args map[string]any
		if json.Unmarshal([]byte(orDefault(row.SpawnArgs, "{}")), &args) == nil {
			rt, _ = args["runtime"].(string)
		}
	}
	for _, n := range OrgNames {
		if Orgs[n].Runtime == rt && rt != "" {
			return n
		}
	}
	return DefaultOrg
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
