//go:build linux

package sandbox2

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_sandbox_spawn.py (58 cases): the sandbox_roles flag, the fail-closed spawn preflight, the
// rollback golden test. Every case drives spawn, relaunch, tick or msg, which the Go CLI still hands to Python, so
// each calls Env.NeedPython (through spNew) and skips where python3 is absent.
//
// not ported: test_live_factory_refuses_code_run_from_outside_its_checkout - calls the Python module function
//   sandbox_preflight with LIVE_HOMES patched; Go has no code-root preflight to call (skips, last resort).
// not ported: test_sandboxed_claude_argv_is_counted_once_and_not_flagged - Python internals claude_procs,
//   flagless_state, claude_outside_spawn, total_claude_procs; Go has no counterpart yet (skips, last resort).
// The Python PathOrder mixin is one case per test_ method; each runs its bash and zsh variants as subtests.

func spSandboxRolesValue(e *spEnv) string {
	e.T.Helper()
	out := e.OK("config").Stdout
	m := regexp.MustCompile(`(?m)^sandbox_roles = (.*)$`).FindStringSubmatch(out)
	if m == nil {
		e.T.Fatalf("no sandbox_roles line in %q", out)
	}
	return m[1]
}

// ---- ConfigGuardTests ----

func TestSandboxSpawnBlockedRolesExit2ValueUnchanged(t *testing.T) {
	e := spNew(t)
	e.OK("config", "sandbox_roles", "reviewer")
	for _, bad := range []string{"judge", "lead", "boss", "cleaner", "evaluator", "foo", "pm", "director", "staff-engineer",
		"scout", "reviewer,judge", "reviewer,,qa"} {
		p := e.Fail(2, "config", "sandbox_roles", bad)
		spHas(t, bad, p.Stderr, "cannot be sandboxed")
		spEq(t, "value after "+bad, spSandboxRolesValue(e), "reviewer")
	}
}

func TestSandboxSpawnAllowedRolesStoredAndPrinted(t *testing.T) {
	e := spNew(t)
	e.OK("config", "sandbox_roles", "reviewer,qa")
	spEq(t, "value", spSandboxRolesValue(e), "reviewer,qa")
	e.OK("config", "sandbox_roles", " coder , reviewer ")
	spEq(t, "value", spSandboxRolesValue(e), "coder,reviewer")
	e.OK("config", "sandbox_roles", "")
	spEq(t, "value", spSandboxRolesValue(e), "")
}

func TestSandboxSpawnDefaultIsOff(t *testing.T) {
	e := spNew(t)
	spEq(t, "value", spSandboxRolesValue(e), "")
}

func TestSandboxSpawnAWorkerMayNotSetIt(t *testing.T) {
	e := spNew(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-coder", Role: "coder"})
	e.FailWith(ft.Opts{Agent: "zz-coder"}, 3, "config", "sandbox_roles", "reviewer")
}

// ---- GoldenTests: SC3, the rollback ----

func TestSandboxSpawnGoldenFlagUnsetIsByteIdenticalToMain(t *testing.T) {
	spEq(t, "argv per role", spRecord(t, nil), spGolden(t))
}

func TestSandboxSpawnGoldenFlagEmptyIsByteIdenticalToMain(t *testing.T) {
	got := spRecord(t, func(e *spEnv) { e.Run([]string{"config", "sandbox_roles", ""}, "") })
	spEq(t, "argv per role", got, spGolden(t))
}

func TestSandboxSpawnGoldenFlagOnThenOffIsByteIdenticalToMain(t *testing.T) {
	got := spRecord(t, func(e *spEnv) {
		for _, v := range []string{"reviewer", ""} {
			if r := e.Run([]string{"config", "sandbox_roles", v}, ""); r.Exit != 0 {
				t.Fatalf("config sandbox_roles %q: exit %d", v, r.Exit)
			}
		}
	})
	spEq(t, "argv per role", got, spGolden(t))
}

func TestSandboxSpawnGoldenReviewerOnDiffersOnlyByTheReviewerPanePath(t *testing.T) {
	golden := spGolden(t)
	got := spRecord(t, func(e *spEnv) { e.Run([]string{"config", "sandbox_roles", "reviewer"}, "") })
	for _, role := range []string{"lead", "qa", "coder"} {
		spEq(t, role, got[role], golden[role])
	}
	// T-0746: the PATH is also re-exported in the pane shell (one extra `pane run`) after the pane is placed
	var reexport, rest [][]string
	for _, a := range got["reviewer"] {
		if len(a) > 3 && a[0] == "pane" && a[1] == "run" && strings.HasPrefix(a[3], "export PATH=") {
			reexport = append(reexport, a)
		} else {
			rest = append(rest, a)
		}
	}
	if len(reexport) != 1 {
		t.Fatalf("PATH re-exports = %v in %v", reexport, got["reviewer"])
	}
	var stripped [][]string
	var gone []string
	for _, a := range rest {
		s, g := spStripPathEnv(a)
		stripped = append(stripped, s)
		gone = append(gone, g...)
	}
	spEq(t, "reviewer argv without PATH", stripped, golden["reviewer"])
	if len(gone) != 1 {
		t.Fatalf("removed PATH values = %v", gone)
	}
	if !strings.HasPrefix(gone[0], "PATH=<TMP>/userhome/.local/state/factory/workers/zz-reviewer/bin:") {
		t.Fatalf("removed PATH = %s", gone[0])
	}
	for _, c := range golden["reviewer"] {
		if _, g := spStripPathEnv(c); len(g) != 0 {
			t.Fatalf("golden reviewer call carries a PATH: %v", c)
		}
	}
}

// ---- PreflightBlockedTests (sandbox_roles = reviewer,qa,coder) ----

func spBlocked(t *testing.T) *spEnv {
	t.Helper()
	e := spNewPrepped(t)
	e.spSetRoles("reviewer,qa,coder")
	return e
}

func TestSandboxSpawnUsernsUnavailable(t *testing.T) {
	e := spBlocked(t)
	e.spStubUnshare("#!/bin/sh\necho denied >&2\nexit 1\n")
	e.spAssertRefused("reviewer", "userns", "", true)
}

func TestSandboxSpawnLegacyDbLayout(t *testing.T) {
	e := spBlocked(t)
	legacy := filepath.Join(e.Home, "factory.db")
	if err := os.Rename(e.DBPath, legacy); err != nil {
		t.Fatal(err)
	}
	e.DBPath = legacy // the test helpers read the DB where it is now
	e.spAssertRefused("reviewer", "db-layout", "", true)
}

func TestSandboxSpawnTokenFileMissing(t *testing.T) {
	e := spBlocked(t)
	os.Remove(e.spTok("worker-oauth-token"))
	e.spAssertRefused("reviewer", "token", "", true)
}

func TestSandboxSpawnGhTokenMissingForTheRole(t *testing.T) {
	e := spBlocked(t)
	os.Remove(e.spTok("gh-worker-rw-token"))
	e.spAssertRefused("coder", "token", "", true)
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 { // the ro token is all a reviewer needs
		t.Fatalf("reviewer spawn: exit %d\n%s", r.Exit, r.Stderr)
	}
}

func TestSandboxSpawnTokenFileMode0644(t *testing.T) {
	e := spBlocked(t)
	if err := os.Chmod(e.spTok("gh-worker-ro-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.spAssertRefused("reviewer", "token", "", true)
}

func TestSandboxSpawnTokenFileIsASymlink(t *testing.T) {
	e := spBlocked(t)
	real := filepath.Join(e.Root, "real-token")
	spWrite(t, real, spMarker, 0o600)
	tok := e.spTok("worker-oauth-token")
	os.Remove(tok)
	if err := os.Symlink(real, tok); err != nil {
		t.Fatal(err)
	}
	e.spAssertRefused("reviewer", "token", "", true)
}

func TestSandboxSpawnTokenFileEmpty(t *testing.T) {
	e := spBlocked(t)
	spWrite(t, e.spTok("worker-oauth-token"), "", 0)
	e.spAssertRefused("reviewer", "token", "", true)
}

func TestSandboxSpawnNoClaudeOnPath(t *testing.T) {
	e := spBlocked(t)
	os.Remove(filepath.Join(e.StubBin, "claude"))
	path := e.StubBin + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin"
	for _, d := range strings.Split(path, string(os.PathListSeparator)) {
		if fi, err := os.Stat(filepath.Join(d, "claude")); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			t.Skip("a claude is installed in " + d)
		}
	}
	p := e.RunWith(ft.Opts{Env: map[string]string{"PATH": path}}, spSpawnArgs(e, "reviewer", "", true)...)
	if p.Exit != 3 {
		t.Fatalf("exit %d, want 3\n%s", p.Exit, p.Stderr)
	}
	spEq(t, "reasons", e.spReasons("sandbox-refused", ""), []string{"claude"})
	spEq(t, "agent start", e.spMux("agent", "start"), [][]string{})
}

func TestSandboxSpawnQaAndCoderWithALegacyWorktree(t *testing.T) {
	e := spBlocked(t)
	for _, role := range []string{"qa", "coder"} {
		wt := e.spCwd(role)
		if err := os.RemoveAll(filepath.Join(wt, ".git")); err != nil {
			t.Fatal(err)
		}
		spWrite(t, filepath.Join(wt, ".git"), "gitdir: /nowhere\n", 0)
		p := e.spAssertRefused(role, "clone", "", true)
		spHas(t, "stderr", p.Stderr, "per-task clone (G-010 T-G)")
	}
}

func TestSandboxSpawnReviewerCwdIsTheFactoryHome(t *testing.T) {
	e := spBlocked(t)
	e.spAssertRefused("reviewer", "cwd", "", false) // no task worktree: cwd falls back to the factory home
}

func TestSandboxSpawnLiveFactoryRefusesCodeRunFromOutsideItsCheckout(t *testing.T) {
	t.Skip("not ported: Python-only sandbox_preflight(...) with LIVE_HOMES patched; Go has no code-root preflight to call")
}

func TestSandboxSpawnRefusalMessageNamesNoTokenPath(t *testing.T) {
	e := spBlocked(t)
	os.Remove(e.spTok("worker-oauth-token"))
	p := e.spAssertRefused("reviewer", "token", "", true)
	spNotHas(t, "output", p.Stdout+p.Stderr, "worker-oauth-token")
	for _, r := range e.spRows("SELECT subject, body FROM messages WHERE subject LIKE 'sandbox refused%'") {
		spNotHas(t, "message", r["subject"]+r["body"], "oauth-token")
	}
}

func TestSandboxSpawnOtherRolesAndTheFlagOffAreNotAffected(t *testing.T) {
	e := spBlocked(t)
	e.spStubUnshare("#!/bin/sh\nexit 1\n")
	if r := e.spSpawn("lead", ""); r.Exit != 0 { // not listed (and not sandboxable)
		t.Fatalf("lead spawn: exit %d\n%s", r.Exit, r.Stderr)
	}
	e.spSetRoles("")
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 {
		t.Fatalf("reviewer spawn: exit %d\n%s", r.Exit, r.Stderr)
	}
}

// ---- PreflightThrottleTests ----

func spMsgs(e *spEnv) []spRow {
	return e.spRows("SELECT recipient, subject FROM messages WHERE subject LIKE 'sandbox refused%' ORDER BY id")
}

func TestSandboxSpawnSameReasonWithinAnHourSendsOneDirectorMessage(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	e.spStubUnshare("#!/bin/sh\nexit 1\n")
	e.spAssertRefused("reviewer", "userns", "zz-rev-1", true)
	e.spAssertRefused("reviewer", "userns", "zz-rev-2", true)
	spEq(t, "events", len(e.spEvents("sandbox-refused")), 2)
	var rcpt []string
	for _, m := range spMsgs(e) {
		rcpt = append(rcpt, m["recipient"])
	}
	spEq(t, "recipients", rcpt, []string{"director"})
}

func TestSandboxSpawnAnotherReasonSendsItsOwnMessageAndAnOldOneIsResent(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	e.spStubUnshare("#!/bin/sh\nexit 1\n")
	e.spAssertRefused("reviewer", "userns", "zz-rev-1", true)
	os.Remove(e.spTok("worker-oauth-token"))
	e.spStubUnshare("#!/bin/sh\nexit 0\n")
	e.spAssertRefused("reviewer", "token", "zz-rev-2", true)
	spEq(t, "messages", len(spMsgs(e)), 2)
	e.spExec("UPDATE throttle SET ts=? WHERE key='sandbox_refused:token'", "0")
	e.spAssertRefused("reviewer", "token", "zz-rev-3", true)
	spEq(t, "messages", len(spMsgs(e)), 3)
}

// ---- PreflightAllowedTests ----

func TestSandboxSpawnReviewerGoesLiveWithTheAgentBinFirstOnThePanePath(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	p := e.spSpawn("reviewer", "")
	if p.Exit != 0 {
		t.Fatalf("exit %d\n%s", p.Exit, p.Stderr)
	}
	spEq(t, "status", e.spAgents("zz-reviewer")[0]["status"], "live")
	agentDir := e.spAgentDir("zz-reviewer")
	paneEnv := e.spPanePaths()
	if len(paneEnv) != 1 {
		t.Fatalf("pane PATH envs = %v, want one", paneEnv)
	}
	spTrue(t, "pane PATH "+paneEnv[0], strings.HasPrefix(paneEnv[0], "PATH="+agentDir+"/bin:"+e.StubBin))
	fi, err := os.Stat(filepath.Join(agentDir, "bin", "claude"))
	if err != nil {
		t.Fatal(err)
	}
	spEq(t, "shim mode", fi.Mode().Perm(), os.FileMode(0o755))
	spTrue(t, "spawn_args.sandbox", spTruthy(e.spSpawnArgsOf("zz-reviewer"), "sandbox"))
	ev := e.spRows("SELECT detail FROM events WHERE action='spawn' AND entity_id='zz-reviewer'")
	spTrue(t, "spawn event sandbox", spTruthy(spJSONObj(t, ev[0]["detail"]), "sandbox"))
}

func TestSandboxSpawnDefaultConfigKeepsTodaysTokenPaths(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer,coder")
	for _, c := range [][2]string{{"reviewer", "gh-worker-ro-token"}, {"coder", "gh-worker-rw-token"}} {
		if r := e.spSpawn(c[0], ""); r.Exit != 0 {
			t.Fatalf("%s spawn: exit %d\n%s", c[0], r.Exit, r.Stderr)
		}
		spEq(t, c[0]+" gh_token", e.spShimCfg(spRoleNames[c[0]])["gh_token"], e.spTok(c[1]))
	}
}

func TestSandboxSpawnGhTokenRwConfigNamesTheTokenTheShimReads(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("coder")
	other := e.spTok("gh-other-token")
	spWrite(t, other, spMarker+"\n", 0o600)
	e.OK("config", "gh_token_rw", other)
	if r := e.spSpawn("coder", ""); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	spEq(t, "gh_token", e.spShimCfg("zz-coder")["gh_token"], other)
}

func TestSandboxSpawnGhTokenRoConfigNamesTheTokenForAReviewer(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	other := e.spTok("gh-other-ro")
	spWrite(t, other, spMarker+"\n", 0o600)
	e.OK("config", "gh_token_ro", other)
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	spEq(t, "gh_token", e.spShimCfg("zz-reviewer")["gh_token"], other)
}

func TestSandboxSpawnGhRwRolesPicksTheRwTokenForAReviewer(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	e.OK("config", "gh_rw_roles", "reviewer")
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	tok, _ := e.spShimCfg("zz-reviewer")["gh_token"].(string)
	spTrue(t, "gh_token "+tok, strings.HasSuffix(tok, "gh-worker-rw-token"))
}

func TestSandboxSpawnGhTokenConfigOutsideTheConfigDirIsRefused(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("coder")
	elsewhere := filepath.Join(e.Root, "elsewhere-token")
	spWrite(t, elsewhere, spMarker+"\n", 0o600)
	e.OK("config", "gh_token_rw", elsewhere)
	p := e.spAssertRefused("coder", "token", "", true)
	spHas(t, "stderr", p.Stderr, "gh_token_rw")
	spNotHas(t, "stderr", p.Stderr, elsewhere)
}

func TestSandboxSpawnTheGitconfigHelperIsScopedToTheWorkerRepos(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	e.OK("config", "worker_gh_repos", "own/one,own/two")
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	gitconfig := filepath.Join(e.spAgentDir("zz-reviewer"), "gitconfig")
	out, err := exec.Command("git", "config", "-f", gitconfig, "--get", "credential.https://github.com.helper").Output()
	if err != nil {
		t.Fatalf("git config -f %s: %v", gitconfig, err)
	}
	got := strings.TrimSpace(string(out))
	spTrue(t, "helper "+got, strings.HasSuffix(got, "scout/ghcred.py git-credential own/one,own/two") ||
		strings.HasSuffix(got, "git-credential own/one,own/two")) // the Go helper is `<self> git-credential <slugs>`
}

func TestSandboxSpawnTheShimImportsFromTheFactoryHome(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	shim := spRead(t, filepath.Join(e.spAgentDir("zz-reviewer"), "bin", "claude"))
	want := "sys.path.insert(0, '" + e.Home + "')"
	spHas(t, "shim", shim, want)
	lines := strings.Split(shim, "\n")
	spTrue(t, "shim has 3 lines", len(lines) > 2)
	spEq(t, "shim line 3", lines[2], want)
}

func TestSandboxSpawnAStaffEngineerIsNeverSandboxed(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer,qa,coder")
	e.SeedAgent(ft.AgentRow{Name: "zz-director", Role: "director"})
	e.OKWith(ft.Opts{Agent: "zz-director"}, "msg", "staff-engineer", "q", "ctx", "--kind", "request")
	starts := e.spMux("agent", "start")
	if len(starts) == 0 || len(starts[0]) < 3 {
		t.Fatalf("agent start calls = %v", starts)
	}
	spEq(t, "started agent", starts[0][2], "staff-engineer")
	for _, c := range e.Calls("herdr") {
		for _, x := range c.Argv {
			spNotHas(t, "herdr argv", x, "PATH=")
		}
	}
	spTrue(t, "no agent dir", !spExists(filepath.Join(e.UserHome, ".local/state/factory/workers/staff-engineer")))
}

// ---- QueueTests ----

func spQueueReviewer(e *spEnv) {
	e.T.Helper()
	e.HerdrFail(true)
	spHas(e.T, "stdout", e.OK(spSpawnArgs(e, "reviewer", "", true)...).Stdout, "QUEUED")
	e.HerdrFail(false)
	e.spSetRoles("reviewer")
}

func TestSandboxSpawnARefusedQueuedSpawnIsCancelledOnceAndTheLeadTold(t *testing.T) {
	e := spNewPrepped(t)
	spQueueReviewer(e)
	probes := filepath.Join(e.Root, "unshare-probes") // the userns probe runs once per preflight
	e.spStubUnshare("#!/bin/sh\necho probe >> " + probes + "\nexit 0\n")
	os.Remove(e.spTok("worker-oauth-token"))
	startsBefore := len(e.spMux("agent", "start"))
	for i := 0; i < 3; i++ {
		e.OK("tick")
	}
	spEq(t, "unshare probes", len(strings.Fields(spRead(t, probes))), 1) // no retry loop: preflight ran on the first pass only
	spEq(t, "status", e.spAgents("zz-reviewer")[0]["status"], "cancelled")
	spEq(t, "agent starts", len(e.spMux("agent", "start")), startsBefore) // fail closed: no pane for the refused row
	spEq(t, "live rows", e.spLive("zz-reviewer"), []spRow(nil))
	msgs := e.spRows("SELECT recipient, subject, body FROM messages WHERE subject LIKE 'sandbox refused queued spawn%'")
	if len(msgs) != 1 || msgs[0]["recipient"] != "zz-lead" || msgs[0]["subject"] != "sandbox refused queued spawn zz-reviewer" {
		t.Fatalf("messages = %v", msgs)
	}
	spHas(t, "body", msgs[0]["body"], "token")
	spHas(t, "body", msgs[0]["body"], "spawn again")
	for _, bad := range []string{"worker-oauth-token", "gh-worker"} {
		spNotHas(t, "body", msgs[0]["body"], bad)
	}
	var by []any
	for _, r := range e.spEvents("dequeued") {
		if r["entity_id"] == "zz-reviewer" {
			by = append(by, spJSONObj(t, r["detail"])["by"])
		}
	}
	spEq(t, "dequeued by", by, []any{"sandbox-preflight"})
	spEq(t, "refusal reasons", e.spReasons("sandbox-refused", ""), []string{"token"})
}

func TestSandboxSpawnACancelledRefusedRowIsNotStartedAfterTheFix(t *testing.T) {
	e := spNewPrepped(t)
	spQueueReviewer(e)
	e.spStubUnshare("#!/bin/sh\nexit 1\n")
	e.OK("tick")
	e.spStubUnshare("#!/bin/sh\nexit 0\n") // fixed: too late, the row is gone
	e.OK("tick")
	spEq(t, "status", e.spAgents("zz-reviewer")[0]["status"], "cancelled")
	spEq(t, "agent start", e.spMux("agent", "start"), [][]string{})
	e.OK(spSpawnArgs(e, "reviewer", "", true)...) // the lead spawns again: it starts
	spEq(t, "status", e.spAgents("zz-reviewer")[0]["status"], "live")
}

func TestSandboxSpawnAQueuedSpawnWithABrokenTemplateFailsOnceAndTheLeadIsToldOnce(t *testing.T) {
	e := spNewPrepped(t)
	spQueueReviewer(e)
	e.spBreakTemplate("reviewer")
	for i := 0; i < 3; i++ {
		e.OK("tick")
	}
	spEq(t, "status", e.spAgents("zz-reviewer")[0]["status"], "cancelled") // terminal: later ticks skip it
	spEq(t, "agent start", e.spMux("agent", "start"), [][]string{})
	var by, reasons []any
	for _, r := range e.spEvents("dequeued") {
		if r["entity_id"] == "zz-reviewer" {
			d := spJSONObj(t, r["detail"])
			by = append(by, d["by"])
			reasons = append(reasons, d["reason"])
		}
	}
	spEq(t, "dequeued by", by, []any{"settings-render-preflight"})
	s, _ := reasons[0].(string)
	spHas(t, "reason", s, "invalid JSON")
	msgs := e.spRows("SELECT recipient, subject, body FROM messages WHERE subject LIKE 'settings-render refused%'")
	if len(msgs) != 1 || msgs[0]["recipient"] != "zz-lead" || msgs[0]["subject"] != "settings-render refused queued spawn zz-reviewer" {
		t.Fatalf("messages = %v", msgs)
	}
	spHas(t, "body", msgs[0]["body"], "spawn again")
	spTrue(t, "no agent dir", !spExists(e.spAgentDir("zz-reviewer")))
}

func TestSandboxSpawnARenderFailureLeavesNoAgentDirRowOrPane(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	e.spBreakTemplate("reviewer")
	p := e.spSpawn("reviewer", "")
	if p.Exit != 3 {
		t.Fatalf("exit %d, want 3\n%s", p.Exit, p.Stderr)
	}
	spHas(t, "stderr", p.Stderr, "settings render failed")
	spTrue(t, "no agent dir", !spExists(e.spAgentDir("zz-reviewer")))
	spEq(t, "rows", e.spAgents("zz-reviewer"), []spRow(nil))
	spEq(t, "agent start", e.spMux("agent", "start"), [][]string{})
	n := len(e.spMux("pane", "split")) + len(e.spMux("tab", "create")) + len(e.spMux("workspace", "create"))
	spEq(t, "placements", n, 0)
}

func TestSandboxSpawnADirectSandboxedSpawnThatIsRefusedExits3AndQueuesNothing(t *testing.T) {
	e := spNewPrepped(t)
	e.spStubUnshare("#!/bin/sh\nexit 1\n")
	p := e.Run(append(spSpawnArgs(e, "reviewer", "", true), "--sandbox"), "")
	if p.Exit != 3 {
		t.Fatalf("exit %d, want 3\n%s", p.Exit, p.Stderr)
	}
	spEq(t, "rows", e.spAgents("zz-reviewer"), []spRow(nil))
	spEq(t, "agent start", e.spMux("agent", "start"), [][]string{})
}

// ---- PerSpawnOptInTests (T-0389) ----

func (e *spEnv) spOptin(role, name string, extra ...string) ft.Result {
	e.T.Helper()
	return e.Run(append(append(spSpawnArgs(e, role, name, true), "--sandbox"), extra...), "")
}

func TestSandboxSpawnOneSpawnIsSandboxedAndAnotherOfTheSameRoleIsNot(t *testing.T) {
	e := spNewPrepped(t)
	p := e.spOptin("reviewer", "zz-spike")
	if p.Exit != 0 {
		t.Fatalf("exit %d\n%s", p.Exit, p.Stderr)
	}
	for _, r := range e.spRows("SELECT value FROM config WHERE key='sandbox_roles'") {
		spEq(t, "sandbox_roles", r["value"], "")
	}
	args := e.spSpawnArgsOf("zz-spike")
	spTrue(t, "sandbox and sandbox_optin", spTruthy(args, "sandbox") && spTruthy(args, "sandbox_optin"))
	fi, err := os.Stat(e.spAgentDir("zz-spike"))
	spTrue(t, "agent dir", err == nil && fi.IsDir())
	if r := e.spSpawn("reviewer", "zz-other"); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	other := e.spSpawnArgsOf("zz-other")
	for _, k := range []string{"sandbox", "sandbox_optin"} {
		if _, ok := other[k]; ok {
			t.Fatalf("zz-other spawn_args has %s: %v", k, other)
		}
	}
	spTrue(t, "no agent dir for zz-other", !spExists(e.spAgentDir("zz-other")))
}

func TestSandboxSpawnAQueuedOptinIsKeptWhenTheTickStartsIt(t *testing.T) {
	e := spNewPrepped(t)
	e.HerdrFail(true)
	spHas(t, "stdout", e.spOptin("reviewer", "zz-spike").Stdout, "QUEUED")
	spTrue(t, "sandbox_optin queued", spTruthy(e.spSpawnArgsOf("zz-spike"), "sandbox_optin"))
	e.HerdrFail(false)
	e.OK("tick")
	row := e.spAgents("zz-spike")[0]
	spEq(t, "status", row["status"], "live")
	spTrue(t, "sandbox", spTruthy(spJSONObj(t, row["spawn_args"]), "sandbox"))
}

func TestSandboxSpawnAQueuedPlainSpawnStaysUnsandboxed(t *testing.T) {
	e := spNewPrepped(t)
	e.HerdrFail(true)
	spHas(t, "stdout", e.spSpawn("reviewer", "zz-other").Stdout, "QUEUED")
	e.HerdrFail(false)
	e.OK("tick")
	row := e.spAgents("zz-other")[0]
	spEq(t, "status", row["status"], "live")
	if _, ok := spJSONObj(t, row["spawn_args"])["sandbox"]; ok {
		t.Fatalf("spawn_args has sandbox: %s", row["spawn_args"])
	}
}

func TestSandboxSpawnRelaunchKeepsTheOptin(t *testing.T) {
	e := spNewPrepped(t)
	if r := e.spOptin("reviewer", "zz-spike"); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	e.OK("relaunch", "zz-spike", "--force")
	spTrue(t, "sandbox_optin", spTruthy(e.spSpawnArgsOf("zz-spike"), "sandbox_optin"))
	paths := e.spPanePaths()
	spEq(t, "pane PATH count", len(paths), 2)
	for _, x := range paths {
		spHas(t, "pane PATH", x, "/workers/zz-spike/bin:")
	}
}

func TestSandboxSpawnOptinStillRunsThePreflight(t *testing.T) {
	e := spNewPrepped(t)
	e.spStubUnshare("#!/bin/sh\nexit 1\n")
	p := e.spOptin("reviewer", "zz-spike")
	if p.Exit == 0 {
		t.Fatal("exit 0, want a refusal")
	}
	spHas(t, "stderr", p.Stderr, "sandbox refused")
	spEq(t, "agent start", e.spMux("agent", "start"), [][]string{})
}

func TestSandboxSpawnRolesThatCannotBeSandboxedExit2(t *testing.T) {
	e := spNewPrepped(t)
	for _, role := range []string{"lead", "pm", "judge", "cleaner", "staff-engineer"} {
		before := len(e.Calls("herdr"))
		name := "zz-x-" + role
		p := e.Run([]string{"spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a", "--sandbox"}, "")
		if p.Exit != 2 {
			t.Fatalf("%s: exit %d, want 2\n%s", role, p.Exit, p.Stderr)
		}
		spHas(t, role+" stderr", p.Stderr, "cannot be sandboxed")
		spEq(t, role+" herdr calls", len(e.Calls("herdr")), before)
		spEq(t, role+" rows", e.spAgents(name), []spRow(nil))
	}
}

func TestSandboxSpawnAWorkerCannotUseIt(t *testing.T) {
	e := spNewPrepped(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a"})
	p := e.RunWith(ft.Opts{Agent: "zz-test-coder"}, append(spSpawnArgs(e, "reviewer", "zz-spike", true), "--sandbox")...)
	if p.Exit != 3 {
		t.Fatalf("exit %d, want 3\n%s", p.Exit, p.Stderr)
	}
	spHas(t, "stderr", p.Stderr, "workers may not run")
	spEq(t, "rows", e.spAgents("zz-spike"), []spRow(nil))
}

// ---- RelaunchTests ----

func TestSandboxSpawnRelaunchReusesTheAgentDirAndResumesTheSession(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	sid := e.spAgents("zz-reviewer")[0]["session_id"]
	agentDir := e.spAgentDir("zz-reviewer")
	shimBefore := spRead(t, filepath.Join(agentDir, "bin", "claude"))
	e.OK("relaunch", "zz-reviewer", "--force")
	starts := e.spMux("agent", "start")
	spEq(t, "agent starts", len(starts), 2)
	i := -1
	for k, w := range starts[1] {
		if w == "--resume" {
			i = k
		}
	}
	if i < 0 || i+1 >= len(starts[1]) {
		t.Fatalf("no --resume in %v", starts[1])
	}
	spEq(t, "resumed session", starts[1][i+1], sid)
	paths := e.spPanePaths()
	spEq(t, "pane PATH count", len(paths), 2)
	spEq(t, "pane PATHs", paths[0], paths[1])
	spTrue(t, "pane PATH", strings.HasPrefix(paths[1], "PATH="+agentDir+"/bin:"))
	ents, err := os.ReadDir(filepath.Dir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range ents {
		names = append(names, d.Name())
	}
	spEq(t, "workers dir", names, []string{"zz-reviewer"})
	spEq(t, "shim", spRead(t, filepath.Join(agentDir, "bin", "claude")), shimBefore)
}

func TestSandboxSpawnARefusedRelaunchDoesNotCloseTheRunningPane(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	e.spStubUnshare("#!/bin/sh\nexit 1\n")
	p := e.Run([]string{"relaunch", "zz-reviewer", "--force"}, "")
	if p.Exit == 0 {
		t.Fatal("relaunch exit 0, want a refusal")
	}
	spEq(t, "pane close", e.spMux("pane", "close"), [][]string{})
	spEq(t, "status", e.spAgents("zz-reviewer")[0]["status"], "live")
}

// spLiveReviewerInFactoryHome is _live_reviewer_in_factory_home: a reviewer spawned while the flag is off with cwd =
// the factory home: legal then, refused once sandboxed. Returns its pane id.
func spLiveReviewerInFactoryHome(e *spEnv) string {
	e.T.Helper()
	p := e.Run([]string{"spawn", "reviewer", "--name", "zz-reviewer", "--goal", "G-001", "--team", "g-001-a", "--cwd", e.Home}, "")
	if p.Exit != 0 {
		e.T.Fatalf("exit %d\n%s", p.Exit, p.Stderr)
	}
	e.spSetRoles("reviewer")
	return e.spAgents("zz-reviewer")[0]["pane_id"]
}

func TestSandboxSpawnRelaunchRefusedByTheCwdRuleKeepsThePaneAndTheRow(t *testing.T) {
	e := spNewPrepped(t)
	pane := spLiveReviewerInFactoryHome(e)
	p := e.Run([]string{"relaunch", "zz-reviewer", "--force"}, "")
	if p.Exit == 0 {
		t.Fatal("relaunch exit 0, want a refusal")
	}
	spHas(t, "stderr", p.Stderr, "sandbox refused")
	spEq(t, "pane close", e.spMux("pane", "close"), [][]string{})
	row := e.spAgents("zz-reviewer")[0]
	spEq(t, "status and pane", [2]string{row["status"], row["pane_id"]}, [2]string{"live", pane})
	spEq(t, "reasons", e.spReasons("sandbox-refused", ""), []string{"cwd"})
}

func TestSandboxSpawnWhenIdleRelaunchRefusedByTheCwdRuleKeepsThePaneAndTheRow(t *testing.T) {
	e := spNewPrepped(t)
	pane := spLiveReviewerInFactoryHome(e)
	e.OK("relaunch", "zz-reviewer", "--when-idle")
	e.OK("tick")
	spEq(t, "pane close", e.spMux("pane", "close"), [][]string{})
	row := e.spAgents("zz-reviewer")[0]
	spEq(t, "status and pane", [2]string{row["status"], row["pane_id"]}, [2]string{"live", pane})
	spEq(t, "reasons", e.spReasons("sandbox-refused", ""), []string{"cwd"})
	spEq(t, "relaunch-failed events", len(e.spEvents("relaunch-failed")), 1)
}

func TestSandboxSpawnRelaunchOfARowWithoutCwdIsACleanCwdRefusal(t *testing.T) {
	e := spNewPrepped(t)
	pane := spLiveReviewerInFactoryHome(e)
	e.spExec("UPDATE agents SET cwd=NULL WHERE name='zz-reviewer'")
	p := e.Run([]string{"relaunch", "zz-reviewer", "--force"}, "")
	if p.Exit == 0 {
		t.Fatal("relaunch exit 0, want a refusal")
	}
	spHas(t, "stderr", p.Stderr, "sandbox refused")
	spNotHas(t, "stderr", p.Stderr, "Traceback")
	spEq(t, "pane close", e.spMux("pane", "close"), [][]string{})
	row := e.spAgents("zz-reviewer")[0]
	spEq(t, "status and pane", [2]string{row["status"], row["pane_id"]}, [2]string{"live", pane})
	spEq(t, "reasons", e.spReasons("sandbox-refused", ""), []string{"cwd"})
}

func TestSandboxSpawnRelaunchWhosePrepareFailsAfterThePaneClosedLeavesNoLiveRow(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes into a 0500 directory")
	}
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	agentDir := e.spAgentDir("zz-reviewer")
	if err := os.Chmod(agentDir, 0o500); err != nil { // preflight passes; prepare_agent cannot write its files
		t.Fatal(err)
	}
	p := e.Run([]string{"relaunch", "zz-reviewer", "--force"}, "")
	if err := os.Chmod(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if p.Exit == 0 {
		t.Fatal("relaunch exit 0, want a failure")
	}
	spEq(t, "pane close", len(e.spMux("pane", "close")), 1)
	row := e.spAgents("zz-reviewer")[0]
	spEq(t, "status and pane", [2]string{row["status"], row["pane_id"]}, [2]string{"exited", "NULL"})
	spEq(t, "relaunch-failed-after-close", len(e.spEvents("relaunch-failed-after-close")), 1)
}

// ---- NoWorktreeOnRefusalTests ----

func TestSandboxSpawnARefusedSpawnWithoutCwdCreatesNoWorktree(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("coder")
	os.Remove(e.spTok("worker-oauth-token"))
	repo := filepath.Join(e.Root, "proj-repo") // a project with a repo: without the early preflight prepare_worktree adds a worktree
	for _, argv := range [][]string{{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"}} {
		if out, err := exec.Command("git", argv...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}
	e.spExec("INSERT INTO projects(id,name,auto_merge,created) VALUES('zz-proj','zz-proj',1,'t')")
	e.spExec("UPDATE projects SET repo_path=?, base_branch='main' WHERE id='zz-proj'", repo)
	e.spExec("UPDATE goals SET project_id='zz-proj' WHERE id='G-001'")
	e.SeedTask(ft.Task{ID: "T-0001", Goal: "G-001", Role: "coder", Status: "in_progress", Team: "g-001-a"})
	list := func() []string {
		ents, err := os.ReadDir(filepath.Join(e.Home, "worktrees"))
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, d := range ents {
			out = append(out, d.Name())
		}
		return out
	}
	before := list()
	p := e.Run([]string{"spawn", "coder", "--name", "zz-coder", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001"}, "")
	if p.Exit == 0 {
		t.Fatal("spawn exit 0, want a refusal")
	}
	spHas(t, "stderr", p.Stderr, "(token)")
	spEq(t, "worktrees", list(), before)
	spEq(t, "fixtures only", before, []string{"w-coder", "w-qa", "w-reviewer"}) // prepare_worktree never ran
}

// ---- NoLeakTests ----

func TestSandboxSpawnTokenContentAndPathAppearNowhereButTheShimPath(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	if r := e.spSpawn("reviewer", ""); r.Exit != 0 {
		t.Fatalf("exit %d\n%s", r.Exit, r.Stderr)
	}
	os.Remove(e.spTok("gh-worker-rw-token"))
	e.spSetRoles("reviewer,coder")
	e.spAssertRefused("coder", "token", "", true) // the refusal path leaks nothing either
	blobs := map[string]string{"multiplexer calls": spRead(t, e.CallsFile)}
	for _, table := range []string{"events", "messages", "agents", "throttle", "config"} {
		blobs[table] = strings.Join(rowsText(e.SQL("SELECT * FROM "+table)), "\n")
	}
	for name, blob := range blobs {
		spNotHas(t, name, blob, spMarker)
		spNotHas(t, name, blob, "oauth-token")
		spNotHas(t, name, blob, "gh-worker")
	}
	err := filepath.WalkDir(e.spAgentDir("zz-reviewer"), func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		spNotHas(t, p, string(b), spMarker) // the shim names the files, never copies them
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func rowsText(rows [][]string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = strings.Join(r, "\x1f")
	}
	return out
}

// ---- DetectionTests ----

func TestSandboxSpawnSandboxedClaudeArgvIsCountedOnceAndNotFlagged(t *testing.T) {
	t.Skip("not ported: Python-only claude_procs, flagless_state, claude_outside_spawn and total_claude_procs; Go has no counterpart yet")
}

// ---- InsideTests: the real shim chain in a temp FACTORY_HOME ----

func TestSandboxSpawnFactoryMsgAndDoneWorkFromInside(t *testing.T) {
	if ok, why := spUserns(); !ok {
		t.Skip("user namespaces are not available on this box: " + why)
	}
	e := spNew(t)
	e.spPrep(false)
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"bin", "scout", "web"} { // the shim and the CLI inside run the code under the temp factory home
		if out, err := exec.Command("cp", "-a", filepath.Join(repo, d), filepath.Join(e.Home, d)).CombinedOutput(); err != nil {
			t.Fatalf("copy %s: %v\n%s", d, err, out)
		}
		os.RemoveAll(filepath.Join(e.Home, d, "__pycache__"))
	}
	e.SeedAgent(ft.AgentRow{Name: "zz-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	spWrite(t, filepath.Join(e.StubBin, "claude"), "#!"+py+`
import os, subprocess, sys
cli = [sys.executable, os.path.join(os.environ["FACTORY_HOME"], "bin", "factory")]
def run(*a):
    p = subprocess.run(cli + list(a), capture_output=True, text=True)
    print("rc", p.returncode, p.stdout, p.stderr, flush=True)
    return p.returncode
sys.exit(run("inbox") or run("msg", "zz-lead", "hello from inside", "body") or run("done", "inside is done"))
`, 0o755)
	e.OK("config", "sandbox_roles", "reviewer")
	p := e.OK(spSpawnArgs(e, "reviewer", "", true)...)
	agentDir := e.spAgentDir("zz-reviewer")
	cmd := exec.Command(filepath.Join(agentDir, "bin", "claude"))
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		pathEnv = "/usr/bin:/bin"
	}
	cmd.Env = []string{"PATH=" + agentDir + "/bin:" + e.StubBin + ":" + pathEnv, "FACTORY_AGENT=zz-reviewer",
		"FACTORY_ROLE=reviewer", "FACTORY_HOME=" + e.Home, "TERM=dumb"}
	if lang := os.Getenv("LANG"); lang != "" {
		cmd.Env = append(cmd.Env, "LANG="+lang)
	}
	cmd.Dir = e.spCwd("reviewer")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shim run: %v\nspawn: %s\n%s", err, p.Stdout, out)
	}
	spEq(t, "rc 0 lines", strings.Count(string(out), "rc 0"), 3)
	msgs := e.spRows("SELECT sender, recipient, subject FROM messages WHERE subject='hello from inside'")
	spEq(t, "messages", msgs, []spRow{{"sender": "zz-reviewer", "recipient": "zz-lead", "subject": "hello from inside"}})
	spEq(t, "status", e.spAgents("zz-reviewer")[0]["status"], "done")
}

// ---- LaunchLineOnlyTests (T-0749) ----

func TestSandboxSpawnSandboxSpawnHasOneClaudeLaunchAndNoCommandOverride(t *testing.T) {
	e := spNewPrepped(t)
	e.spSetRoles("reviewer")
	p := e.spSpawn("reviewer", "")
	if p.Exit != 0 {
		t.Fatalf("exit %d\n%s", p.Exit, p.Stderr)
	}
	var calls [][]string
	for _, c := range e.Calls("herdr") {
		calls = append(calls, c.Argv)
	}
	var starts [][]string
	for _, a := range calls {
		if len(a) >= 2 && a[0] == "agent" && a[1] == "start" {
			starts = append(starts, a)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("agent starts = %v", starts)
	}
	start := starts[0]
	idx := func(w string) int {
		for i, x := range start {
			if x == w {
				return i
			}
		}
		return -1
	}
	if k := idx("--kind"); k < 0 || start[k+1] != "claude" {
		t.Fatalf("no --kind claude in %v", start)
	}
	dd := idx("--")
	if dd < 0 {
		t.Fatalf("no -- in %v", start)
	}
	tail := start[dd+1:]
	spTrue(t, "launch args must be flags only", len(tail) > 0 && strings.HasPrefix(tail[0], "--"))
	override := map[string]bool{"--cmd": true, "--command": true, "--exec": true, "--program": true, "--bin": true}
	for _, w := range start[:dd] {
		spTrue(t, "no override flag "+w, !override[w])
	}
	for _, a := range calls {
		for _, w := range a { // calls_flags: a word starting with --cmd or --command
			spTrue(t, "no --cmd word "+w, !strings.HasPrefix(w, "--cmd") && !strings.HasPrefix(w, "--command"))
		}
		if len(a) >= 2 {
			switch a[0] + " " + a[1] {
			case "pane split", "tab create", "workspace create": // no creation call carries a program to run
				for _, w := range a {
					spTrue(t, "creation call "+w, !override[w])
				}
			}
		}
	}
	claudeRE := regexp.MustCompile(`(^|[\s;&|/])claude(\s|$)`)
	runs := []string{}
	for _, a := range e.spMux("pane", "run") { // the only text run in a pane is the env export, never a claude launch
		text := strings.Join(a[3:], " ")
		spTrue(t, "pane run "+text, !claudeRE.MatchString(text))
		runs = append(runs, text)
	}
	found := false
	for _, r := range runs {
		found = found || strings.Contains(r, "PATH="+e.spAgentDir("zz-reviewer")+"/bin:")
	}
	spTrue(t, "a pane run exports the agent PATH: "+strings.Join(runs, " | "), found)
}

// ---- PathOrderBase: bash (BASH_ENV) and zsh (.zshenv) variants ----

// spPathOrder runs fn once per pane shell that exists: startup(text) installs the shell startup file and returns the
// extra env that makes the shell read it.
func spPathOrder(t *testing.T, fn func(t *testing.T, e *spEnv, shell string, startup func(text string) map[string]string)) {
	t.Helper()
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if err != nil {
				// follow-up: the CI go job installs no zsh yet; once it does, make a missing zsh a failure under CI.
				t.Skip("needs " + shell)
			}
			e := spNewPrepped(t)
			e.spSetRoles("reviewer")
			spWrite(t, filepath.Join(e.UserHome, ".local", "bin", "claude"), "#!/bin/sh\nexit 0\n", 0o755)
			startup := func(text string) map[string]string {
				if shell == "bash" {
					f := filepath.Join(e.UserHome, "bashenv")
					spWrite(t, f, text, 0)
					return map[string]string{"BASH_ENV": f}
				}
				spWrite(t, filepath.Join(e.UserHome, ".zshenv"), text, 0)
				return nil
			}
			fn(t, e, path, startup)
		})
	}
}

func (e *spEnv) spSpawnShell(shell string, extra map[string]string) ft.Result {
	e.T.Helper()
	env := map[string]string{"SHELL": shell}
	for k, v := range extra {
		env[k] = v
	}
	return e.RunWith(ft.Opts{Env: env}, spSpawnArgs(e, "reviewer", "", true)...)
}

func TestSandboxSpawnStartupFilePrependingLocalBinStillGetsTheShimExportedInThePane(t *testing.T) {
	spPathOrder(t, func(t *testing.T, e *spEnv, shell string, startup func(string) map[string]string) {
		extra := startup("export PATH=\"$HOME/.local/bin:$PATH\"\n")
		p := e.spSpawnShell(shell, extra)
		if p.Exit != 0 {
			t.Fatalf("exit %d\n%s", p.Exit, p.Stderr)
		}
		agentDir := e.spAgentDir("zz-reviewer")
		var runs []string
		ok := false
		for _, a := range e.spMux("pane", "run") {
			r := strings.Join(a[3:], " ")
			runs = append(runs, r)
			ok = ok || (strings.HasPrefix(r, "export PATH="+agentDir+"/bin:") && strings.Contains(r, "&& hash -r"))
		}
		spTrue(t, "export with hash -r in "+strings.Join(runs, " | "), ok)
		run, start := -1, -1
		for i, c := range e.Calls("herdr") {
			if len(c.Argv) >= 2 {
				k := c.Argv[0] + " " + c.Argv[1]
				if k == "pane run" && run < 0 {
					run = i
				}
				if k == "agent start" && start < 0 {
					start = i
				}
			}
		}
		spTrue(t, "pane run before agent start", run >= 0 && start >= 0 && run < start)
	})
}

func TestSandboxSpawnAStartupFileThatShadowsClaudeAfterTheExportIsRefused(t *testing.T) {
	spPathOrder(t, func(t *testing.T, e *spEnv, shell string, startup func(string) map[string]string) {
		extra := startup("claude() { :; }\n")
		before := len(e.Calls("herdr"))
		p := e.spSpawnShell(shell, extra)
		e.spCheckRefused(p, before, "reviewer", "path", "zz-reviewer")
	})
}

func TestSandboxSpawnAnUnsetPaneShellIsRefusedNotPassed(t *testing.T) {
	spPathOrder(t, func(t *testing.T, e *spEnv, shell string, startup func(string) map[string]string) {
		before := len(e.Calls("herdr"))
		p := e.RunWith(ft.Opts{Env: map[string]string{"SHELL": ""}}, spSpawnArgs(e, "reviewer", "", true)...)
		e.spCheckRefused(p, before, "reviewer", "path", "zz-reviewer")
	})
}
