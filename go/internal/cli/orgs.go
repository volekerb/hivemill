package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Go port of scout/orgs.py and the org helpers of bin/factory_cli.py (enabled_orgs, goal_org, task_org, agent_org,
// org_for, oname). Org names are the keys of ORGS; orgs_test.go compares them with the Python source.

const orgDefault = "claude"

// orgNames is NAMES (ORGS order); orgRuntimes maps each to ORGS[org]["runtime"].
var orgNames = []string{"claude", "cursor"}

var orgRuntimes = map[string]string{"claude": "claude", "cursor": "cursor"}

var orgSingletonRoles = []string{"boss", "director", "staff-engineer"}

var orgPrefixRE = regexp.MustCompile(`^(claude|cursor)-`)

func orgKnown(org string) bool { return inList(orgNames, org) }

// orgCheck is check(): "" means the default org; ok=false is Python's ValueError (unknown org).
func orgCheck(org string) (string, bool) {
	org = pyStrip(org)
	if org == "" {
		return orgDefault, true
	}
	return org, orgKnown(org)
}

// orgPrefixOf is prefix_of: "cursor" for "cursor-coder-t-1", "" for an unprefixed name.
func orgPrefixOf(name string) string {
	if m := orgPrefixRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// orgStripPrefix is strip_prefix.
func orgStripPrefix(name string) string { return orgPrefixRE.ReplaceAllString(name, "") }

// orgName is name(): <org>-<base>, idempotent; ok=false for an unknown org.
func orgName(org, base string) (string, bool) {
	org, ok := orgCheck(org)
	if !ok {
		return "", false
	}
	if orgPrefixOf(base) != "" {
		return base, true
	}
	return org + "-" + base, true
}

// agentRow is the part of an agents row that of_agent reads.
type agentRow struct{ org, name, runtime, spawnArgs string }

// orgOfAgent is of_agent: the org column, else the name prefix, else the runtime (spawn_args for legacy rows), else
// the default org.
func orgOfAgent(r agentRow) string {
	if orgKnown(r.org) {
		return r.org
	}
	if p := orgPrefixOf(r.name); p != "" {
		return p
	}
	rt := r.runtime
	if rt == "" {
		var sa map[string]any
		if json.Unmarshal([]byte(r.spawnArgs), &sa) == nil {
			rt, _ = sa["runtime"].(string)
		}
	}
	for _, o := range orgNames {
		if orgRuntimes[o] == rt && rt != "" {
			return o
		}
	}
	return orgDefault
}

// enabledOrgs is enabled_orgs: config orgs_enabled, known and unique, else the default org alone.
func enabledOrgs(c *ctx) ([]string, error) {
	v, err := cfgText(c, "orgs_enabled")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, o := range strings.Split(v, ",") {
		o = pyStrip(o)
		if orgKnown(o) && !inList(out, o) {
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		return []string{orgDefault}, nil
	}
	return out, nil
}

// goalOrg is goal_org: the org of a goal, "" for no goal, an unknown goal or an unknown org. A DB without the
// goals.org column (pre G-028) also gives "".
func goalOrg(c *ctx, goal string) (string, error) {
	if goal == "" {
		return "", nil
	}
	var org *string
	err := c.db.QueryRow("SELECT org FROM goals WHERE id=?", goal).Scan(&org)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case strings.Contains(err.Error(), "no such column"):
		return "", nil
	default:
		return "", err
	}
	if org != nil && orgKnown(*org) {
		return *org, nil
	}
	return "", nil
}

// taskOrg is task_org.
func taskOrg(c *ctx, task string) (string, error) {
	if task == "" {
		return "", nil
	}
	var g *string
	err := c.db.QueryRow("SELECT goal_id FROM tasks WHERE id=?", task).Scan(&g)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if g == nil {
		return "", nil
	}
	return goalOrg(c, *g)
}

// agentOrg is agent_org: the org of a registered agent, the name prefix for a non-agent, "" for no name.
func agentOrg(c *ctx, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	var o, sa *string
	// agents has no runtime column, so of_agent only ever sees the org, the name and spawn_args.
	err := c.db.QueryRow("SELECT org, spawn_args FROM agents WHERE name=?", name).Scan(&o, &sa)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return orgPrefixOf(name), nil
		}
		return "", err
	}
	r := agentRow{name: name}
	if o != nil {
		r.org = *o
	}
	if sa != nil {
		r.spawnArgs = *sa
	}
	return orgOfAgent(r), nil
}

// orgFor is org_for: explicit, else the goal's, else the task's goal's, else the sender's, else the default org.
func orgFor(c *ctx, goal, task, sender, org string) (string, error) {
	if orgKnown(org) {
		return org, nil
	}
	if o, err := goalOrg(c, goal); err != nil || o != "" {
		return o, err
	}
	if o, err := taskOrg(c, task); err != nil || o != "" {
		return o, err
	}
	if o, err := agentOrg(c, sender); err != nil || o != "" {
		return o, err
	}
	return orgDefault, nil
}

// orgAgentName is oname: <org>-<base>. The default org's prefix is behind config org_prefix_default; an already
// prefixed base is returned unchanged.
func orgAgentName(c *ctx, org, base string) (string, error) {
	if !orgKnown(org) {
		org = orgDefault
	}
	if orgPrefixOf(base) != "" {
		return base, nil
	}
	if org == orgDefault {
		v, err := cfgText(c, "org_prefix_default")
		if err != nil {
			return "", err
		}
		if pyStrip(v) != "1" {
			return base, nil
		}
	}
	n, _ := orgName(org, base)
	return n, nil
}
