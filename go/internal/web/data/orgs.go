package data

import (
	"database/sql"
	"regexp"
	"strings"
)

// Orgs is scout/orgs.py NAMES (in order), DefaultOrg its DEFAULT_ORG. orgRuntime maps an org to its agent runtime.
var (
	Orgs       = []string{"claude", "cursor"}
	DefaultOrg = "claude"
	orgRuntime = map[string]string{"claude": "claude", "cursor": "cursor"}
	prefixRe   = regexp.MustCompile(`^(claude|cursor)-`)
)

func isOrg(s string) bool {
	for _, o := range Orgs {
		if o == s {
			return true
		}
	}
	return false
}

// OrgParam is queries.org_param: the ?org= filter, a known org name, else "" (all orgs).
func OrgParam(query map[string]string) string {
	if v := query["org"]; isOrg(v) {
		return v
	}
	return ""
}

// GoalOrgs is queries.goal_orgs: {goal id: org}. A database from before goals.org has no column, so the map is
// empty and callers fall back to DefaultOrg through OrgOfGoal.
func (c *Conn) GoalOrgs() (map[string]string, error) {
	has, err := c.HasColumn("goals", "org")
	if err != nil || !has {
		return map[string]string{}, err
	}
	out := map[string]string{}
	err = c.each("SELECT id, org FROM goals", nil, func(r rowScanner) error {
		var id string
		var org sql.NullString
		if err := r.Scan(&id, &org); err != nil {
			return err
		}
		if org.Valid && isOrg(org.String) {
			out[id] = org.String
		} else {
			out[id] = DefaultOrg
		}
		return nil
	})
	return out, err
}

// OrgOfGoal is queries.org_of_goal.
func OrgOfGoal(gorgs map[string]string, gid string) string {
	if gid == "" {
		return DefaultOrg
	}
	if o, ok := gorgs[gid]; ok {
		return o
	}
	return DefaultOrg
}

// AgentOrg is queries.agent_org: agents.org when set, else the name prefix, else an explicit spawn_args runtime,
// else the goal's org, else the default org.
func AgentOrg(orgCol, name, explicitRuntime, goalOrg string) string {
	if isOrg(orgCol) {
		return orgCol
	}
	if m := prefixRe.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if explicitRuntime != "" {
		for _, o := range Orgs {
			if orgRuntime[o] == explicitRuntime {
				return o
			}
		}
	}
	if isOrg(goalOrg) {
		return goalOrg
	}
	return DefaultOrg
}

// OrgRuntime is queries.org_runtime.
func OrgRuntime(org string) string {
	if r, ok := orgRuntime[org]; ok {
		return r
	}
	return orgRuntime[DefaultOrg]
}

func bossNames(withOwner bool) []string {
	out := []string{}
	if withOwner {
		out = append(out, "owner")
	}
	out = append(out, "boss")
	for _, o := range Orgs {
		out = append(out, o+"-boss")
	}
	return out
}

var _ = strings.TrimSpace
