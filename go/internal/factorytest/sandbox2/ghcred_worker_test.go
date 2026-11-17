package sandbox2

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_worker_ghcred.py (22 cases by distinct name: FlagOn 6, Refusal 8, shell path order 3 - each
// run once per shell, bash and zsh, as subtests - FlagOff 3, Sandboxed 1, CleanRemoteBranch 1). Every case drives
// spawn, judge, clean or tick, which the Go CLI still hands to Python: each calls Env.NeedPython (ghNewW). The gh
// of the home is the Python stub behind a wrapper that logs GH_TOKEN|GH_CONFIG_DIR, as in the original.
// not ported: none. Two differences: the unset-pane-shell case sets SHELL to "" through Env.Opts, which the Go
// harness turns into an unset variable (Python sets it to the empty string); the zsh variant skips when zsh is not
// installed unless CI is set (then it fails, as the original does).

func TestWorkerGhcredCoderGetsTheRwTokenFileAndTheGhcredEnv(t *testing.T) {
	w := ghNewW(t)
	w.flag(strings.Join(ghRoles, ","))
	w.spawnOK("coder", nil, "")
	env := w.started("coder", "")
	agentDir := filepath.Join(w.ghcredDir(), "zz-coder")
	ghEq(t, "GIT_CONFIG_GLOBAL", env["GIT_CONFIG_GLOBAL"], filepath.Join(agentDir, "gitconfig"))
	ghEq(t, "GH_CONFIG_DIR", env["GH_CONFIG_DIR"], filepath.Join(agentDir, "gh"))
	ghEq(t, "FACTORY_GH_TOKEN_FILE", env["FACTORY_GH_TOKEN_FILE"], filepath.Join(w.tdir, "gh-worker-rw-token"))
	if !strings.HasPrefix(env["PATH"], filepath.Join(agentDir, "bin")+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q, want the prefix %s:", env["PATH"], filepath.Join(agentDir, "bin"))
	}
	ghContains(t, "gitconfig", ghRead(t, filepath.Join(agentDir, "gitconfig")), "git-credential")
}

func TestWorkerGhcredEachRoleGetsTheTokenItsConfigSays(t *testing.T) {
	w := ghNewW(t)
	w.flag(strings.Join(ghRoles, ","))
	want := map[string]string{"coder": "rw", "judge": "rw", "cleaner": "rw", "reviewer": "ro", "qa": "ro", "pm": "ro"}
	for _, role := range ghRoles {
		if r := w.spawn(role, nil, ""); r.Exit != 0 {
			t.Fatalf("%s: exit %d: %s", role, r.Exit, r.Stderr)
		}
		ghEq(t, role+" token file", w.started(role, "")["FACTORY_GH_TOKEN_FILE"], filepath.Join(w.tdir, "gh-worker-"+want[role]+"-token"))
	}
}

func TestWorkerGhcredGhRwRolesMovesARoleToTheRwToken(t *testing.T) {
	w := ghNewW(t)
	w.flag(strings.Join(ghRoles, ","))
	w.OKWith(ft.Opts{Agent: ghDirector}, "config", "gh_rw_roles", "reviewer")
	w.spawn("reviewer", nil, "")
	ghEq(t, "reviewer token file", w.started("reviewer", "")["FACTORY_GH_TOKEN_FILE"], filepath.Join(w.tdir, "gh-worker-rw-token"))
}

func TestWorkerGhcredNoFakeTokenInHerdrCallsOrEvents(t *testing.T) {
	w := ghNewW(t)
	w.flag(strings.Join(ghRoles, ","))
	for _, role := range ghRoles {
		w.spawn(role, nil, "")
	}
	calls, err := json.Marshal(w.Calls("herdr"))
	ghMust(t, "marshal calls", err)
	var ev []string
	for _, row := range w.SQL("SELECT * FROM events") {
		ev = append(ev, strings.Join(row, "|"))
	}
	blob := string(calls) + strings.Join(ev, "\n")
	for _, tok := range []string{ghRO, ghRW} {
		ghNotContains(t, "herdr calls and events", blob, tok)
	}
	var files []string
	for _, f := range ghTree(t, w.ghcredDir()) {
		files = append(files, ghRead(t, f))
	}
	ghNotContains(t, "ghcred files", strings.Join(files, ""), ghRW)
}

func TestWorkerGhcredParentCredentialEnvIsScrubbedFromThePane(t *testing.T) {
	w := ghNewW(t)
	w.flag(strings.Join(ghRoles, ","))
	if r := w.spawn("coder", ghDangerous, ""); r.Exit != 0 {
		t.Fatalf("exit %d: %s", r.Exit, r.Stderr)
	}
	env := w.started("coder", "")
	ghEq(t, "GIT_CONFIG_COUNT", env["GIT_CONFIG_COUNT"], "0")
	for _, k := range append(append([]string{}, ghScrubbed...), "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0") {
		ghEq(t, k, env[k], "")
	}
}

func TestWorkerGhcredJudgeGhCallsUseTheRwTokenAndNotTheOwnerGhConfig(t *testing.T) {
	w := ghNewW(t)
	w.flag(strings.Join(ghRoles, ","))
	ghEq(t, "spawn judge exit", w.spawn("judge", nil, "").Exit, 0)
	penv := w.started("judge", "")
	w.exec("INSERT INTO projects(id,name,auto_merge,created) VALUES(?,?,?,'t')", "zz-test-proj", "zz-test-proj", 1)
	w.SeedTask(ft.Task{})
	w.SeedAgent(ft.AgentRow{Name: "zz-test-judge", Role: "judge", Goal: "G-001", Task: "T-0001"})
	sha := w.UseGitHead()
	w.setGh(map[string]any{"repo_url": w.RepoURL, "prs": map[string]any{ft.PRURL: map[string]any{
		"number": 1, "state": "OPEN", "headRefOid": sha, "headRefName": "factory/t-0001", "baseRefName": "main",
		"mergeable": "MERGEABLE", "isDraft": false, "url": ft.PRURL, "statusCheckRollup": []any{}}}})
	extra := map[string]string{}
	for _, k := range []string{"PATH", "GIT_CONFIG_GLOBAL", "GH_CONFIG_DIR", "FACTORY_GH_TOKEN_FILE"} {
		extra[k] = penv[k]
	}
	r := w.RunWith(ft.Opts{Agent: "zz-test-judge", Env: extra}, "judge", "T-0001", "--merge", "--reason", "zz-test")
	ghEq(t, "judge exit (no verdicts: refused, but only after it asked gh)\n"+r.Stderr, r.Exit, 4)
	log := strings.TrimSpace(ghRead(t, w.envlog))
	if log == "" {
		t.Fatal("the judge never called gh")
	}
	for _, line := range strings.Split(log, "\n") {
		tok, cfg, _ := strings.Cut(line, "|")
		ghEq(t, "GH_TOKEN", tok, ghRW)
		if cfg == filepath.Join(w.UserHome, ".config", "gh") {
			t.Fatalf("gh ran with the owner config dir %s", cfg)
		}
		ghEq(t, "GH_CONFIG_DIR", cfg, penv["GH_CONFIG_DIR"])
	}
}

// ---- refusals ----

// assertRefused is RefusalTests.assert_refused.
func (w *ghW) assertRefused(role, key string) ft.Result {
	w.t.Helper()
	before := len(w.Calls("herdr"))
	p := w.spawn(role, nil, "")
	if p.Exit != 3 {
		w.t.Fatalf("spawn exit %d, want 3\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	ghContains(w.t, "stderr", p.Stderr, "gh-cred")
	if key != "" {
		ghContains(w.t, "stderr", p.Stderr, key)
	}
	for _, tok := range []string{ghRO, ghRW, "gh-worker"} {
		ghNotContains(w.t, "stderr", p.Stderr, tok)
	}
	if got := w.placed(before); len(got) != 0 {
		w.t.Fatalf("calls that placed an agent after the refusal: %v", got)
	}
	ghEq(w.t, "live agents named zz-"+role, len(w.SQL("SELECT * FROM agents WHERE name=? AND status='live'", "zz-"+role)), 0)
	var ents []string
	for _, r := range w.SQL("SELECT entity_id FROM events WHERE action='gh-cred-refused'") {
		ents = append(ents, r[0])
	}
	if !reflect.DeepEqual(ents, []string{"zz-" + role}) {
		w.t.Fatalf("gh-cred-refused events for %v, want [zz-%s]", ents, role)
	}
	ghEq(w.t, "refusal messages", len(w.SQL("SELECT * FROM messages WHERE subject LIKE 'worker gh credential refused%'")), 1)
	return p
}

func TestWorkerGhcredMissingTokenFileRefusesWithExit3(t *testing.T) {
	w := ghNewW(t)
	w.flag("coder")
	ghMust(t, "remove token", os.Remove(filepath.Join(w.tdir, "gh-worker-rw-token")))
	w.assertRefused("coder", "gh_token_rw")
}

func TestWorkerGhcredTokenFileWithLooseModeIsRefused(t *testing.T) {
	w := ghNewW(t)
	w.flag("reviewer")
	ghMust(t, "chmod", os.Chmod(filepath.Join(w.tdir, "gh-worker-ro-token"), 0o644))
	w.assertRefused("reviewer", "gh_token_ro")
}

func TestWorkerGhcredSymlinkedTokenFileIsRefused(t *testing.T) {
	w := ghNewW(t)
	w.flag("coder")
	tok := filepath.Join(w.tdir, "gh-worker-rw-token")
	ghMust(t, "rename", os.Rename(tok, filepath.Join(w.tdir, "real")))
	ghMust(t, "symlink", os.Symlink(filepath.Join(w.tdir, "real"), tok))
	w.assertRefused("coder", "gh_token_rw")
}

func TestWorkerGhcredTokenFileOutsideTheConfigDirIsRefusedWithoutNamingIt(t *testing.T) {
	w := ghNewW(t)
	w.flag("coder")
	elsewhere := filepath.Join(w.UserHome, "zz-elsewhere-tok")
	ghWrite(t, elsewhere, ghRW+"\n", 0o600)
	w.OKWith(ft.Opts{Agent: ghDirector}, "config", "gh_token_rw", elsewhere)
	p := w.assertRefused("coder", "gh_token_rw")
	ghNotContains(t, "stderr", p.Stderr, "zz-elsewhere-tok")
	rows := w.SQL("SELECT body FROM messages WHERE subject LIKE 'worker gh credential refused%'")
	ghEq(t, "messages", len(rows), 1)
	ghNotContains(t, "message body", rows[0][0], "zz-elsewhere-tok")
}

func TestWorkerGhcredNoRepositoryRefuses(t *testing.T) {
	w := ghNewW(t)
	w.flag("coder")
	w.exec("DELETE FROM config WHERE key='worker_gh_repos'")
	w.exec("UPDATE config SET value='https://example.invalid/x' WHERE key='factory_remote_url'")
	w.assertRefused("coder", "worker_gh_repos")
}

func TestWorkerGhcredNoGhRefuses(t *testing.T) {
	w := ghNewW(t)
	w.flag("coder")
	only := filepath.Join(w.Root, "only-bin") // the multiplexer stub and git, no gh anywhere on the PATH
	ghMust(t, "mkdir", os.MkdirAll(only, 0o755))
	git, err := exec.LookPath("git")
	ghMust(t, "git", err)
	py, err := exec.LookPath("python3")
	ghMust(t, "python3", err)
	ghMust(t, "symlink herdr", os.Symlink(filepath.Join(w.StubBin, "herdr"), filepath.Join(only, "herdr")))
	ghMust(t, "symlink git", os.Symlink(git, filepath.Join(only, "git")))
	ghMust(t, "symlink python3", os.Symlink(py, filepath.Join(only, "python3"))) // the Python CLI's shebang is env python3
	p := w.RunWith(ft.Opts{Agent: w.Agent, Env: map[string]string{"PATH": only}}, "spawn", "coder", "--name", "zz-coder",
		"--goal", "G-001", "--team", "g-001-a", "--cwd", w.Home)
	if p.Exit != 3 {
		t.Fatalf("exit %d, want 3\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	ghContains(t, "stderr", p.Stderr, "gh-cred")
	ghContains(t, "stderr", p.Stderr, "no gh on the PATH")
}

func TestWorkerGhcredARefusedQueuedRowIsCancelledByTheTick(t *testing.T) {
	w := ghNewW(t)
	w.HerdrFail(true)
	ghContains(t, "queued spawn", w.spawn("coder", nil, "").Stdout, "QUEUED")
	w.HerdrFail(false)
	w.flag("coder")
	ghMust(t, "remove token", os.Remove(filepath.Join(w.tdir, "gh-worker-rw-token")))
	starts := len(w.HerdrStarts())
	w.OK("tick")
	ghEq(t, "status", w.SQL("SELECT status FROM agents WHERE name='zz-coder'")[0][0], "cancelled")
	ghEq(t, "agent start calls", len(w.HerdrStarts()), starts)
	var by []string
	for _, r := range w.SQL("SELECT detail FROM events WHERE action='dequeued'") {
		var d map[string]any
		ghMust(t, "event detail", json.Unmarshal([]byte(r[0]), &d))
		by = append(by, d["by"].(string))
	}
	if !reflect.DeepEqual(by, []string{"gh-cred-preflight"}) {
		t.Fatalf("dequeued by %v, want [gh-cred-preflight]", by)
	}
	ghEq(t, "gh-cred-refused events", len(w.SQL("SELECT * FROM events WHERE action='gh-cred-refused'")), 1)
	msg := w.SQL("SELECT body FROM messages WHERE subject LIKE 'gh-cred refused queued spawn%'")
	ghEq(t, "queued refusal messages", len(msg), 1)
	ghNotContains(t, "message body", msg[0][0], "gh-worker")
}

func TestWorkerGhcredConfigRejectsANonWorkerRole(t *testing.T) {
	w := ghNewW(t)
	p := w.FailWith(ft.Opts{Agent: ghDirector}, 2, "config", "worker_gh_roles", "coder,boss")
	ghContains(t, "stderr", p.Stderr, "boss")
}

// ---- shell startup files must not put another gh ahead of the shim (T-0746) ----

// ghEachShell runs fn once per shell whose startup file the pane would read: bash (BASH_ENV, runs wherever bash
// exists) and zsh (.zshenv, the live case). A missing zsh is a failure in CI and a skip elsewhere.
func ghEachShell(t *testing.T, fn func(t *testing.T, w *ghW, shell string, startup func(text string), extra map[string]string)) {
	t.Helper()
	for _, name := range []string{"bash", "zsh"} {
		t.Run(name, func(t *testing.T) {
			path, err := exec.LookPath(name)
			if err != nil {
				if name == "zsh" && os.Getenv("CI") != "" {
					t.Fatal("zsh missing in CI: install zsh on the runner")
				}
				t.Skipf("needs %s", name)
			}
			w := ghNewW(t)
			extra := map[string]string{"SHELL": path}
			startup := func(text string) {
				if name == "bash" {
					f := filepath.Join(w.UserHome, "bashenv")
					ghWrite(t, f, text, 0o644)
					extra["BASH_ENV"] = f
				} else {
					ghWrite(t, filepath.Join(w.UserHome, ".zshenv"), text, 0o644)
				}
			}
			fn(t, w, name, startup, extra)
		})
	}
}

func TestWorkerGhcredGhShadowedByAStartupFileIsRefusedAndNothingIsStarted(t *testing.T) {
	ghEachShell(t, func(t *testing.T, w *ghW, _ string, startup func(string), extra map[string]string) {
		w.flag("coder")
		startup("gh() { :; }\n")
		p := w.spawn("coder", extra, "")
		ghEq(t, "exit\n"+p.Stderr, p.Exit, 3)
		ghContains(t, "stderr", p.Stderr, "gh-cred refused")
		ghContains(t, "stderr", p.Stderr, "gh does not resolve")
		ghEq(t, "agent start calls", len(w.HerdrStarts()), 0)
	})
}

func TestWorkerGhcredGhResolvingToTheShimSpawnsAndReExportsPathInThePane(t *testing.T) {
	ghEachShell(t, func(t *testing.T, w *ghW, _ string, _ func(string), extra map[string]string) {
		w.flag("coder")
		p := w.spawn("coder", extra, "")
		ghEq(t, "exit\n"+p.Stderr, p.Exit, 0)
		var runs [][]string
		for _, c := range w.Calls("herdr") {
			if len(c.Argv) >= 2 && c.Argv[0] == "pane" && c.Argv[1] == "run" {
				runs = append(runs, c.Argv)
			}
		}
		for _, a := range runs {
			if len(a) > 3 && strings.HasPrefix(a[3], "export PATH=") && strings.Count(a[3], "&& hash -r") == 1 {
				return
			}
		}
		t.Fatalf("no pane run re-exports PATH with one `&& hash -r`: %v", runs)
	})
}

func TestWorkerGhcredAnUnsetPaneShellIsRefusedNotPassed(t *testing.T) {
	ghEachShell(t, func(t *testing.T, w *ghW, _ string, _ func(string), _ map[string]string) {
		w.flag("coder")
		p := w.spawn("coder", map[string]string{"SHELL": ""}, "")
		ghEq(t, "exit\n"+p.Stderr, p.Exit, 3)
		ghContains(t, "stderr", p.Stderr, "gh does not resolve")
	})
}

// ---- flag off ----

func TestWorkerGhcredFlagOffAddsNoEnvAndCreatesNothing(t *testing.T) {
	w := ghNewW(t)
	for _, c := range []struct {
		tag    string
		setSet bool
	}{{"", false}, {"-b", true}} { // never set, and explicitly empty
		if c.setSet {
			w.flag("")
		}
		for _, role := range []string{"coder", "judge", "cleaner"} {
			if p := w.spawn(role, ghDangerous, c.tag); p.Exit != 0 {
				t.Fatalf("%s: exit %d: %s", role, p.Exit, p.Stderr)
			}
			if got := ghKeys(w.started(role, c.tag)); !reflect.DeepEqual(got, []string{"FACTORY_AGENT", "FACTORY_HOME", "FACTORY_ROLE"}) {
				t.Fatalf("%s pane env keys = %v, want FACTORY_AGENT, FACTORY_HOME, FACTORY_ROLE", role, got)
			}
		}
	}
	if ghExists(w.ghcredDir()) {
		t.Fatalf("%s exists with the flag off", w.ghcredDir())
	}
}

func TestWorkerGhcredFlagOffLetsMissingTokensSpawn(t *testing.T) {
	w := ghNewW(t)
	ents, err := os.ReadDir(w.tdir)
	ghMust(t, "ReadDir", err)
	for _, e := range ents {
		ghMust(t, "remove token", os.Remove(filepath.Join(w.tdir, e.Name())))
	}
	ghEq(t, "spawn exit", w.spawn("coder", nil, "").Exit, 0)
}

func TestWorkerGhcredARoleNotListedIsUntouched(t *testing.T) {
	w := ghNewW(t)
	w.flag("reviewer")
	w.spawn("coder", ghDangerous, "")
	if got := ghKeys(w.started("coder", "")); !reflect.DeepEqual(got, []string{"FACTORY_AGENT", "FACTORY_HOME", "FACTORY_ROLE"}) {
		t.Fatalf("pane env keys = %v, want FACTORY_AGENT, FACTORY_HOME, FACTORY_ROLE", got)
	}
}

// ---- sandboxed ----

func TestWorkerGhcredASandboxedSpawnCreatesNoGhcredDir(t *testing.T) {
	w := ghNewW(t)
	w.flag("reviewer")
	w.OKWith(ft.Opts{Agent: ghDirector}, "config", "sandbox_roles", "reviewer")
	// sandbox preflight fails first (no worker oauth token): the point is that ghcred never ran for it
	p := w.spawn("reviewer", nil, "")
	ghContains(t, "stderr", p.Stderr, "sandbox refused")
	ghNotContains(t, "stderr", p.Stderr, "gh-cred")
	if ghExists(w.ghcredDir()) {
		t.Fatalf("%s exists for a sandboxed spawn", w.ghcredDir())
	}
}

// ---- clean --remote-branch ----

// ghGitIn runs git in dir on a scrubbed env (identity as the harness: zz-test), failing the test on an error.
func (w *ghW) gitIn(dir string, args ...string) string {
	w.t.Helper()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + w.UserHome, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=zz-test", "GIT_AUTHOR_EMAIL=zz-test@example.invalid",
		"GIT_COMMITTER_NAME=zz-test", "GIT_COMMITTER_EMAIL=zz-test@example.invalid"}
	r := ghGitOut(w.t, dir, env, true, args...)
	return strings.TrimSpace(r.Out)
}

func TestWorkerGhcredCleanRemoteBranchPushRunsWithTheGhcredGitconfig(t *testing.T) {
	w := ghNewW(t)
	origin := filepath.Join(w.Root, "origin.git")
	repo := w.Home
	if err := ft.AssertLocalRemote(origin, w.Root); err != nil {
		t.Fatal(err)
	}
	w.gitIn(w.Root, "init", "-q", "--bare", "-b", "main", origin)
	w.gitIn(repo, "remote", "add", "origin", origin)
	ghWrite(t, filepath.Join(repo, "README"), "x\n", 0o644)
	w.gitIn(repo, "add", "README")
	w.gitIn(repo, "commit", "-q", "-m", "init")
	w.gitIn(repo, "push", "-q", "origin", "main")
	w.gitIn(repo, "checkout", "-q", "-b", "factory/t-0007")
	ghWrite(t, filepath.Join(repo, "f"), "f", 0o644)
	w.gitIn(repo, "add", "f")
	w.gitIn(repo, "commit", "-q", "-m", "f")
	sha := w.gitIn(repo, "rev-parse", "HEAD")
	w.gitIn(repo, "push", "-q", "origin", "HEAD:refs/heads/factory/t-0007")
	w.gitIn(repo, "checkout", "-q", "main")
	w.gitIn(repo, "branch", "-q", "-D", "factory/t-0007")
	w.gitIn(repo, "--git-dir", origin, "update-ref", "refs/pull/1/head", sha)
	w.setGh(map[string]any{"pr_list": []any{map[string]any{"number": 1, "state": "MERGED", "headRefName": "factory/t-0007",
		"headRefOid": sha, "isCrossRepository": false}}})
	w.exec("INSERT INTO projects(id,name,auto_merge,created) VALUES(?,?,?,'t')", "zz-test-proj", "zz-test-proj", 1)
	w.SeedAgent(ft.AgentRow{Name: "zz-test-cleaner", Role: "cleaner", Goal: "G-001"})
	w.flag("cleaner")
	ghEq(t, "spawn exit", w.spawn("cleaner", nil, "").Exit, 0)
	penv := w.started("cleaner", "")
	gitlog := filepath.Join(w.Root, "git-env.log") // wraps the real git: records which gitconfig each call ran with
	var realGit string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if ghExists(filepath.Join(d, "git")) {
			realGit = d
			break
		}
	}
	if realGit == "" {
		t.Fatal("no git on PATH")
	}
	ghWrite(t, filepath.Join(w.StubBin, "git"), "#!/bin/sh\necho \"$GIT_CONFIG_GLOBAL\" >> "+gitlog+"\nexec "+realGit+"/git \"$@\"\n", 0o755)
	extra := map[string]string{}
	for _, k := range []string{"PATH", "GIT_CONFIG_GLOBAL", "GH_CONFIG_DIR", "FACTORY_GH_TOKEN_FILE"} {
		extra[k] = penv[k]
	}
	p := w.RunWith(ft.Opts{Agent: "zz-test-cleaner", Env: extra}, "clean", "--remote-branch", "factory/t-0007")
	if p.Exit != 0 {
		t.Fatalf("clean exit %d\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	ghContains(t, "stdout", p.Stdout, "deleted")
	used := map[string]bool{}
	for _, f := range strings.Fields(ghRead(t, gitlog)) {
		used[f] = true
	}
	if len(used) != 1 || !used[penv["GIT_CONFIG_GLOBAL"]] {
		t.Fatalf("git ran with gitconfigs %v, want only %s", used, penv["GIT_CONFIG_GLOBAL"])
	}
	ghContains(t, "pane gitconfig", ghRead(t, penv["GIT_CONFIG_GLOBAL"]), "git-credential")
}
