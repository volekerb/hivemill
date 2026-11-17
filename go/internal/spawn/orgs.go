package spawn

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DefaultOrg is orgs.DEFAULT_ORG.
const DefaultOrg = "claude"

// Roles is ROLES of the Python CLI, in its order.
var Roles = []string{"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner",
	"staff-engineer", "researcher", "scout", "retro", "evaluator"}

// Sandboxable is workerbox.SANDBOXABLE.
var Sandboxable = []string{"reviewer", "qa", "coder", "researcher"}

// OnDemand is ON_DEMAND, the staff-engineer role: never sandboxed.
const OnDemand = "staff-engineer"

type orgSpec struct {
	label, runtime string
	models         map[string]string
}

// OrgNames is orgs.NAMES in declaration order.
var OrgNames = []string{"claude", "cursor"}

var orgTable = map[string]orgSpec{
	"claude": {label: "Claude", runtime: "claude", models: map[string]string{
		"boss": "claude-opus-5-5", "director": "claude-opus-5-5", "lead": "claude-opus-5-5",
		"pm": "claude-sonnet-5-5", "reviewer": "claude-opus-5-5", "judge": "claude-sonnet-5-5",
		"staff-engineer": "claude-opus-5-5", "researcher": "claude-sonnet-5-5",
		"coder": "claude-sonnet-5-5", "qa": "claude-sonnet-5-5", "cleaner": "claude-haiku-5-5",
		"scout": "claude-sonnet-5-5", "retro": "claude-sonnet-5-5", "evaluator": "claude-sonnet-5-5"}},
	"cursor": {label: "Cursor", runtime: "cursor", models: map[string]string{
		"boss": "grok-4.7-high", "director": "grok-4.7-high", "lead": "grok-4.7-high",
		"pm": "grok-4.7-high", "reviewer": "grok-4.7-high", "judge": "grok-4.7-high",
		"staff-engineer": "grok-4.7-high", "researcher": "grok-4.7-high",
		"coder": "composer-2.5", "qa": "grok-4.7-high", "cleaner": "composer-2.5",
		"scout": "composer-2.5", "retro": "composer-2.5", "evaluator": "claude-sonnet-5-5"}},
}

var prefixRE = regexp.MustCompile(`^(claude|cursor)-`)

// ValidOrg reports whether org is a known org name (orgs.ORGS membership).
func ValidOrg(org string) bool { _, ok := orgTable[org]; return ok }

// CheckOrg is orgs.check: "" is the default org; an unknown name is an error with Python's text.
func CheckOrg(org string) (string, error) {
	org = pyStrip(org)
	if org == "" {
		org = DefaultOrg
	}
	if !ValidOrg(org) {
		return "", fmt.Errorf("unknown org %s; known: %s", pyRepr(org), strings.Join(OrgNames, ", "))
	}
	return org, nil
}

// OrgLabel is orgs.label.
func OrgLabel(org string) string { return orgTable[org].label }

// OrgRuntime is orgs.runtime.
func OrgRuntime(org string) string { return orgTable[org].runtime }

// OrgModel is orgs.model: the org's model for a role ("" for a role the map lacks; Python raises KeyError).
func OrgModel(org, role string) string { return orgTable[org].models[role] }

// PrefixOf is orgs.prefix_of: "cursor" for "cursor-coder-t-1", "" for an unprefixed name.
func PrefixOf(name string) string {
	if m := prefixRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// OrgName is orgs.name: the org-prefixed agent name; idempotent.
func OrgName(org, base string) string {
	if PrefixOf(base) != "" {
		return base
	}
	return org + "-" + base
}

// KnownRuntimes is agent_runtime.KNOWN; cursorRoles is cursor_harness.CURSOR_ROLES (the cursor allow-list).
var cursorRoles = []string{"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "scout"}

var knownRuntimes = []string{"claude", "cursor"}

func allowedRoles(rt string) []string {
	if rt == "cursor" {
		s := append([]string(nil), cursorRoles...)
		sort.Strings(s)
		return s
	}
	return nil
}

func sortedKnown() string { return strings.Join(knownRuntimes, ", ") }

// pyStrList is str(sorted(list)) of Python.
func pyStrList(l []string) string { return pyList(l) }

// ParseRuntimes is agent_runtime.parse_runtimes: {role: runtime} from the agent_runtimes and cursor_pilot_roles config
// values; a bad token is an error with Python's text.
func ParseRuntimes(agentRuntimes, cursorPilotRoles string) (map[string]string, error) {
	out := map[string]string{}
	if raw := pyStrip(agentRuntimes); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = pyStrip(part)
			if part == "" {
				continue
			}
			role, rt, ok := strings.Cut(part, "=")
			if !ok {
				return nil, fmt.Errorf("agent_runtimes entry %s must be role=runtime (known runtimes: %s; use *=cursor or all=cursor for every Cursor-capable role)", pyRepr(part), sortedKnown())
			}
			role, rt = pyStrip(role), pyStrip(rt)
			if !inList(knownRuntimes, rt) {
				return nil, fmt.Errorf("unknown runtime %s; known: %s", pyRepr(rt), sortedKnown())
			}
			if role == "*" || role == "all" {
				if rt == "claude" {
					continue
				}
				for _, r := range allowedRoles(rt) {
					out[r] = rt
				}
				continue
			}
			if rt != "claude" && !inList(allowedRoles(rt), role) {
				return nil, fmt.Errorf("runtime %s allow-list is %s; not %s", pyRepr(rt), pyStrList(allowedRoles(rt)), pyRepr(role))
			}
			out[role] = rt
		}
	}
	for _, role := range csv(cursorPilotRoles) {
		if !inList(allowedRoles("cursor"), role) {
			return nil, fmt.Errorf("cursor_pilot_roles: %s not in cursor allow-list (%s)", pyRepr(role), strings.Join(allowedRoles("cursor"), ", "))
		}
		if _, ok := out[role]; !ok {
			out[role] = "cursor"
		}
	}
	return out, nil
}

// OrgRuntimeFor is org_runtime(): the runtime an agent of role in org runs on.
func OrgRuntimeFor(role, org, agentRuntimes, cursorPilotRoles string) (string, error) {
	if role == "evaluator" {
		return "claude", nil
	}
	rt := OrgRuntime(org)
	if org != DefaultOrg {
		return rt, nil
	}
	m, err := ParseRuntimes(agentRuntimes, cursorPilotRoles)
	if err != nil {
		return "", err
	}
	if v, ok := m[role]; ok {
		return v, nil
	}
	return "claude", nil
}

// RoleModel is role_model(): a non-default org uses its own map; the claude org takes the config override model_<role>
// when set (every role has such a key), else the table.
func RoleModel(role, org string, cfg map[string]string) string {
	if org != "" && org != DefaultOrg {
		return OrgModel(org, role)
	}
	if v := pyStrip(cfg["model_"+role]); v != "" {
		return v
	}
	return orgTable[DefaultOrg].models[role]
}
