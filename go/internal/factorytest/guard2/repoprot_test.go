package guard2

import (
	"strings"
	"testing"
)

// Port of tests/test_worker_guard_repoprot.py (R-a, T-0638): no agent changes branch protection or repository
// settings through gh.

var (
	rpWorkerRoles = []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator", "staff-engineer"}
	rpHQRoles     = []string{"boss", "director", "lead"}
)

const rpP = "repos/acme/hive/branches/main/protection"

var rpBlocked = []string{
	"gh api -X PUT " + rpP + " -F required_pull_request_reviews=null",
	"gh api --method PATCH " + rpP + "/enforce_admins",
	"gh api --method=DELETE " + rpP,
	"gh api -XPOST " + rpP + "/required_signatures",
	"gh api " + rpP + " -X delete",
	"gh api -H 'Accept: application/vnd.github+json' -X PUT " + rpP,
	"gh api -X PUT repos/acme/hive/branches/release/x/protection",
	"gh api " + rpP + " -f enforce_admins=false",
	"gh api -X POST repos/acme/hive/rulesets --input r.json",
	"gh api -p luke-cage -X PUT " + rpP,
	"gh api --preview luke-cage --method PUT " + rpP,
	"gh api graphql -f query='mutation{updateBranchProtectionRule(input:{}){clientMutationId}}'",
	"gh api graphql -f query='mutation{updateRepository(input:{}){clientMutationId}}'",
	"gh api graphql -F query=@q.graphql",
	"gh alias set x 'api -X PUT repos/a/b/branches/main/protection'",
	"gh api -X PUT '" + rpP + "#x'",
	"gh api --field=a=1 " + rpP,
	"gh api -X PUT '" + rpP + "?x=1'",
	"gh api -X PUT repos/acme/hive/rules/branches/main",
	"gh repo edit acme/hive --default-branch x",
	"gh repo edit --visibility private",
	"gh -R acme/hive repo edit --enable-issues=false",
	"gh repo archive acme/hive --yes",
	"gh api -X PATCH repos/acme/hive -f private=true",
	"gh api --method PATCH /repos/acme/hive",
	"gh api -X DELETE repos/acme/hive",
	"env GH_TOKEN=x gh repo edit acme/hive",
	"sh -c 'gh api -X PUT repos/acme/hive/branches/main/protection'",
	"gh api -X=DELETE " + rpP,
	"gh api --method=PATCH " + rpP + "/enforce_admins",
	"gh api -iX DELETE " + rpP,
	"gh api -iXDELETE " + rpP,
	"gh api -i -X=PUT " + rpP,
	"gh api -X DELETE repos/acme/hive/branches/main/%70rotection",
	"gh api -X PUT repos/acme/hive/branches/main/%50ROTECTION",
	"gh api -X PATCH repos/acme/hive%2F",
}

var rpAllowed = []string{
	"gh api " + rpP,
	"gh api -X GET " + rpP,
	"gh api --method=GET " + rpP + "/enforce_admins",
	"gh api -X=GET " + rpP,
	"gh api -iX GET " + rpP,
	"gh api -iXGET " + rpP,
	"gh api repos/acme/hive/branches/main/%70rotection",
	"gh api repos/acme/hive",
	"gh api repos/acme/hive --jq .private",
	"gh repo view acme/hive --json name",
	"gh api -X POST repos/acme/hive/issues/3/comments -f body=hello",
	"gh api -X PATCH repos/acme/hive/pulls/3 -f title=x",
	"gh api -X PUT repos/acme/hive/topics",
	"gh api graphql -f query='query{viewer{login}}'",
	"gh alias list",
	"gh pr view 3",
	"echo gh repo edit",
	"grep -n 'gh api -X PUT repos/a/b/branches/main/protection' notes.txt",
}

func rpRun(t *testing.T, tmp, cmd, role string) res {
	t.Helper()
	args := roleArgs(role)
	for _, h := range rpHQRoles {
		if role == h {
			args = []string{"--hq"}
		}
	}
	// FACTORY_HOME has no DB: the public-text rule stays off, so only R-a decides.
	return bash(t, cmd, hookOpts{args: args, cwd: repoRoot(t),
		drop: []string{"WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH"}, set: map[string]string{"FACTORY_HOME": tmp}})
}

func TestRepoProtEveryRoleIsRefusedEachChangingShape(t *testing.T) {
	tmp := t.TempDir()
	for _, role := range append(append([]string{}, rpWorkerRoles...), rpHQRoles...) {
		for _, cmd := range rpBlocked {
			r := rpRun(t, tmp, cmd, role)
			if r.code != 2 {
				t.Errorf("should block (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
			}
			if !strings.Contains(r.stderr, "branch protection and repository settings") {
				t.Errorf("%s: %q: stderr lacks the R-a reason: %q", role, cmd, r.stderr)
			}
		}
	}
}

func TestRepoProtReadsAndUnrelatedWritesStayAllowed(t *testing.T) {
	tmp := t.TempDir()
	for _, role := range []string{"coder", "judge", "director"} {
		for _, cmd := range rpAllowed {
			r := rpRun(t, tmp, cmd, role)
			if r.code != 0 {
				t.Errorf("should allow (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
			}
		}
	}
}
