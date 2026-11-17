package s2tasks

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_project_update_location.py (5 of 8 cases; PARTIAL for P7a, T-1495).
// not ported: GitConfigIdentityTests (3: test_name_and_noreply_email_are_allowed, test_other_email_is_refused,
// test_include_is_still_refused) - they call the Python function git_config_refusal(home) through
// harness load_factory(); no Go counterpart exists (go/internal has no git config allow-list function), so
// there is nothing to drive from the CLI.

const (
	pulDirector = "zz-test-director"
	pulProj     = "zz-test-proj"
)

func pulEnv(t *testing.T) (*ft.Env, string) {
	e := ft.New(t)
	seedProject(e, pulProj, 1)
	e.SeedAgent(ft.AgentRow{Name: pulDirector, Role: "director"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-boss", Role: "boss"})
	repo := filepath.Join(e.Root, "clone")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := osexec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return e, repo
}

func pulRow(e *ft.Env) string {
	return strings.Join(sql(e, "SELECT COALESCE(gh_repo,'NULL'), COALESCE(repo_path,'NULL') FROM projects WHERE id=?", pulProj)[0], "|")
}

func pulEvents(e *ft.Env) []string {
	return col(e, "SELECT detail FROM events WHERE entity='project' AND action='update' ORDER BY id")
}

// pyRepr is Python's repr of a str or None as the CLI prints it in the old -> new event.
func pyRepr(v string, null bool) string {
	if null {
		return "None"
	}
	return "'" + v + "'"
}

func TestProjectUpdateLocationSetsGhAndRepoAndRecordsEvents(t *testing.T) {
	e, repo := pulEnv(t)
	e.NeedPython() // project update is not ported to Go
	old := sql(e, "SELECT gh_repo, repo_path FROM projects WHERE id=?", pulProj)[0]
	oldGH, oldRepo := old[0], old[1]
	ok(e, pulDirector, "project", "update", pulProj, "--gh", "example-org/example-repo", "--repo", repo)
	eq(t, "row", pulRow(e), "example-org/example-repo|"+repo)
	ev := strings.Join(pulEvents(e), "\n")
	contains(t, "events", ev, "gh_repo: "+pyRepr(oldGH, oldGH == "NULL")+" -> 'example-org/example-repo'")
	contains(t, "events", ev, "repo_path: "+pyRepr(oldRepo, oldRepo == "NULL")+" -> "+pyRepr(repo, false))
}

func TestProjectUpdateLocationBadGhShapeIsRefused(t *testing.T) {
	e, _ := pulEnv(t)
	e.NeedPython() // project update is not ported to Go
	before := pulRow(e)
	for _, bad := range []string{"noslash", "a/b/c", "a b/c", "a/", "/b", "a/b\n"} {
		refused(e, 2, pulDirector, "project", "update", pulProj, "--gh", bad)
	}
	eq(t, "row", pulRow(e), before)
	eqS(t, "events", pulEvents(e), nil)
}

func TestProjectUpdateLocationRepoMustExistAndBeAGitRepo(t *testing.T) {
	e, _ := pulEnv(t)
	e.NeedPython() // project update is not ported to Go
	before := pulRow(e)
	plain := filepath.Join(e.Root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	refused(e, 2, pulDirector, "project", "update", pulProj, "--repo", plain)
	refused(e, 2, pulDirector, "project", "update", pulProj, "--repo", filepath.Join(e.Root, "missing"))
	eq(t, "row", pulRow(e), before)
}

func TestProjectUpdateLocationNonDirectorIsRefused(t *testing.T) {
	e, repo := pulEnv(t)
	e.NeedPython() // project update is not ported to Go
	before := pulRow(e)
	for _, who := range []string{"zz-test-coder", "zz-test-boss"} {
		refused(e, 3, who, "project", "update", pulProj, "--gh", "example-org/example-repo")
		refused(e, 3, who, "project", "update", pulProj, "--repo", repo)
	}
	eq(t, "row", pulRow(e), before)
	eqS(t, "events", pulEvents(e), nil)
}

func TestProjectUpdateLocationEmptyUpdateNamesTheNewFlags(t *testing.T) {
	e, _ := pulEnv(t)
	p := refused(e, 1, pulDirector, "project", "update", pulProj)
	contains(t, "output", p.Stderr+p.Stdout, "--gh")
}
