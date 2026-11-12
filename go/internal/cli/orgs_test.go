package cli

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func pyOrgsSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(repoFile(t, "scout/orgs.py"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOrgsMatchPython(t *testing.T) {
	src := pyOrgsSrc(t)
	// NAMES = tuple(ORGS): the keys of the ORGS dict, in order; each with its runtime.
	var names []string
	rts := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^    "(\w+)": \{\n        "label": "[^"]*",\n        "runtime": "(\w+)"`).FindAllStringSubmatch(src, -1) {
		names = append(names, m[1])
		rts[m[1]] = m[2]
	}
	if !reflect.DeepEqual(names, orgNames) || !reflect.DeepEqual(rts, orgRuntimes) {
		t.Fatalf("orgNames %v / runtimes %v differ from scout/orgs.py %v / %v", orgNames, orgRuntimes, names, rts)
	}
	if m := regexp.MustCompile(`(?m)^DEFAULT_ORG = "(\w+)"`).FindStringSubmatch(src); m == nil || m[1] != orgDefault {
		t.Fatalf("orgDefault %q differs from scout/orgs.py", orgDefault)
	}
	m := regexp.MustCompile(`(?m)^SINGLETON_ROLES = \(([^)]*)\)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("SINGLETON_ROLES not found")
	}
	var roles []string
	for _, w := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		roles = append(roles, w[1])
	}
	if !reflect.DeepEqual(roles, orgSingletonRoles) {
		t.Fatalf("orgSingletonRoles %v differ from %v", orgSingletonRoles, roles)
	}
	if !strings.Contains(src, `PREFIX_RE = re.compile(r"^(%s)-" % "|".join(map(re.escape, NAMES)))`) {
		t.Fatal("PREFIX_RE changed; update orgPrefixRE")
	}
}

func TestOrgsNameHelpers(t *testing.T) {
	if p := orgPrefixOf("cursor-coder-t-1"); p != "cursor" {
		t.Fatal(p)
	}
	if p := orgPrefixOf("coder-t-1"); p != "" {
		t.Fatal(p)
	}
	if s := orgStripPrefix("claude-boss"); s != "boss" {
		t.Fatal(s)
	}
	if s := orgStripPrefix("claude-claude-x"); s != "claude-x" {
		t.Fatal("strip only once: " + s)
	}
	if n, ok := orgName("cursor", "coder-t-0840"); !ok || n != "cursor-coder-t-0840" {
		t.Fatal(n)
	}
	if n, _ := orgName("cursor", "claude-boss"); n != "claude-boss" {
		t.Fatalf("idempotent: %q", n)
	}
	if n, _ := orgName("", "boss"); n != "claude-boss" {
		t.Fatalf("default org: %q", n)
	}
	if _, ok := orgName("nope", "boss"); ok {
		t.Fatal("unknown org must fail")
	}
}

func TestOrgsOfAgent(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    agentRow
		want string
	}{
		{"org column wins", agentRow{org: "cursor", name: "claude-x"}, "cursor"},
		{"unknown org column falls to prefix", agentRow{org: "zzz", name: "cursor-x"}, "cursor"},
		{"name prefix", agentRow{name: "cursor-lead-g-1"}, "cursor"},
		{"runtime", agentRow{name: "legacy", runtime: "cursor"}, "cursor"},
		{"spawn_args runtime", agentRow{name: "legacy", spawnArgs: `{"runtime": "cursor"}`}, "cursor"},
		{"spawn_args junk", agentRow{name: "legacy", spawnArgs: `not json`}, "claude"},
		{"default", agentRow{name: "legacy"}, "claude"},
	} {
		if got := orgOfAgent(tc.r); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOrgsDBHelpers(t *testing.T) {
	c, r := wctx(t, "owner")
	r.sql("INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-001','g','active','x','x','cursor'),('G-002','g','active','x','x','weird')")
	r.sql("INSERT INTO tasks(id,goal_id,title,role,status,created,updated) VALUES('T-0001','G-001','t','coder','todo','x','x')")
	r.sql("INSERT INTO agents(name,role,status,org) VALUES('legacy-a','coder','live','cursor'),('claude-b','coder','live',NULL)")

	for goal, want := range map[string]string{"G-001": "cursor", "G-002": "", "G-404": "", "": ""} {
		if got, err := goalOrg(c, goal); err != nil || got != want {
			t.Errorf("goalOrg(%q) = %q %v, want %q", goal, got, err, want)
		}
	}
	if got, _ := taskOrg(c, "T-0001"); got != "cursor" {
		t.Errorf("taskOrg %q", got)
	}
	if got, _ := taskOrg(c, "T-9999"); got != "" {
		t.Errorf("taskOrg missing %q", got)
	}
	for name, want := range map[string]string{"legacy-a": "cursor", "claude-b": "claude", "cursor-new": "cursor", "stranger": "", "": ""} {
		if got, err := agentOrg(c, name); err != nil || got != want {
			t.Errorf("agentOrg(%q) = %q %v, want %q", name, got, err, want)
		}
	}
	// org_for: explicit, goal, task, sender, default
	for _, tc := range []struct{ goal, task, sender, org, want string }{
		{"G-001", "", "", "claude", "claude"},
		{"G-001", "", "claude-b", "", "cursor"},
		{"", "T-0001", "claude-b", "", "cursor"},
		{"", "", "legacy-a", "", "cursor"},
		{"", "", "stranger", "", "claude"},
		{"G-002", "", "", "bogus", "claude"},
	} {
		if got, err := orgFor(c, tc.goal, tc.task, tc.sender, tc.org); err != nil || got != tc.want {
			t.Errorf("orgFor(%+v) = %q %v", tc, got, err)
		}
	}
}

func TestOrgsEnabledAndAgentName(t *testing.T) {
	c, r := wctx(t, "owner")
	r.sql("DELETE FROM config")
	if got, _ := enabledOrgs(c); !reflect.DeepEqual(got, []string{"claude"}) {
		t.Fatalf("default %v", got)
	}
	r.sql("INSERT INTO config(key,value) VALUES('orgs_enabled',' cursor, bogus,claude,cursor ')")
	if got, _ := enabledOrgs(c); !reflect.DeepEqual(got, []string{"cursor", "claude"}) {
		t.Fatalf("listed %v", got)
	}
	// oname: the default org is unprefixed until org_prefix_default=1
	if n, _ := orgAgentName(c, "claude", "boss"); n != "boss" {
		t.Fatalf("off: %q", n)
	}
	if n, _ := orgAgentName(c, "cursor", "boss"); n != "cursor-boss" {
		t.Fatalf("cursor: %q", n)
	}
	r.sql("INSERT INTO config(key,value) VALUES('org_prefix_default','1')")
	if n, _ := orgAgentName(c, "claude", "coder-t-1"); n != "claude-coder-t-1" {
		t.Fatalf("on: %q", n)
	}
	if n, _ := orgAgentName(c, "claude", "cursor-boss"); n != "cursor-boss" {
		t.Fatalf("already prefixed: %q", n)
	}
	if n, _ := orgAgentName(c, "bogus", "boss"); n != "claude-boss" {
		t.Fatalf("unknown org -> default: %q", n)
	}
}
