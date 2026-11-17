package installer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ------------------------------------------------------------------------------------------------ TestCheck

func TestHivemillInstallCheckAllPresentExit0AndReadOnly(t *testing.T) {
	h := hiNew(t, hiHome(t))
	rc, out, _ := hiRun(h, "--check")
	hiEq(t, rc, 0, "rc: "+out)
	var names []string
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) > 1 && (f[0] == "ok" || f[0] == "missing" || f[0] == "warn") {
			names = append(names, f[1])
		}
	}
	want := "python3>=3.11,git,gh,gh-auth,herdr,claude,claude-auth,systemd-user,ram,cpu,go"
	hiEq(t, strings.Join(names, ","), want, "check names")
	hiIn(t, out, "warn go ")
	hiEq(t, hiLast(out), "suggested-max-agents 6", "last line")
	hiEq(t, len(h.muts), 0, "mutations")
}

func TestHivemillInstallCheckMissingItemExit3(t *testing.T) {
	rc, out, _ := hiRun(hiNew(t, hiHome(t), hiTools("git", "gh")), "--check")
	hiEq(t, rc, 3, "rc")
	hiIn(t, out, "missing herdr ")
	hiIn(t, out, "missing claude ")
}

func TestHivemillInstallCheckNotLoggedInIsMissing(t *testing.T) {
	h := hiNew(t, hiHome(t))
	h.ghAuth = false
	rc, out, _ := hiRun(h, "--check")
	hiEq(t, rc, 3, "rc")
	hiIn(t, out, "missing gh-auth ")
}

func TestHivemillInstallCheckRAMCPUGoAreWarningsOnly(t *testing.T) {
	h := hiNew(t, hiHome(t))
	h.mem, h.cpus = 3.5, 1
	rc, out, _ := hiRun(h, "--check")
	hiEq(t, rc, 0, "rc: "+out)
	hiIn(t, out, "warn ram ")
	hiIn(t, out, "warn cpu ")
	hiIn(t, out, "suggested-max-agents 3")
}

func TestHivemillInstallSuggestedMaxAgents(t *testing.T) {
	for _, c := range []struct {
		gib  float64
		want int
	}{{1.0, 2}, {2.9, 2}, {3.5, 3}, {5.2, 5}, {6.0, 6}, {64.0, 6}} {
		h := hiNew(t, hiHome(t))
		h.mem = c.gib
		_, out, _ := hiRun(h, "--check")
		hiEq(t, hiLast(out), fmt.Sprintf("suggested-max-agents %d", c.want), fmt.Sprint(c.gib, " GiB"))
	}
}

func TestHivemillInstallCheckNoSystemdSessionIsMissingUnlessNoSystemd(t *testing.T) {
	home := hiHome(t)
	h := hiNew(t, home)
	h.systemd = false
	rc, out, _ := hiRun(h, "--check")
	hiEq(t, rc, 3, "rc")
	hiIn(t, out, "missing systemd-user")
	h = hiNew(t, home)
	h.systemd = false
	rc, out, _ = hiRun(h, "--check", "--no-systemd")
	hiEq(t, rc, 0, "rc: "+out)
	hiIn(t, out, "warn systemd-user")
}

func TestHivemillInstallCheckOldPythonIsMissing(t *testing.T) {
	h := hiNew(t, hiHome(t), hiRules(hiRule1("python3 --version", 0, "Python 3.9.2")))
	rc, out, _ := hiRun(h, "--check")
	hiEq(t, rc, 3, "rc")
	hiIn(t, out, "missing python3>=3.11")
}

// ------------------------------------------------------------------------------------------------ TestModes

func TestHivemillInstallDryRunChangesNothingAndListsArgv(t *testing.T) {
	home := hiHome(t)
	fh := filepath.Join(home, "factory")
	h := hiNew(t, home, hiTools("git"))
	rc, out, _ := hiRun(h, "--dry-run", "--project-id", "demo", "--project-repo", "/r", "--max-agents", "3")
	hiEq(t, rc, 0, "rc: "+out)
	hiEq(t, len(h.muts), 0, "mutations")
	hiEq(t, h.mutateCalls(), 0, "mutating calls")
	if hiExists(fh) {
		t.Errorf("%s exists after --dry-run", fh)
	}
	hiIn(t, out, fmt.Sprintf("git clone -- %s %s", hiRepo, fh+".installing"))
	hiIn(t, out, "checkout --detach refs/tags/v0.10.0")
	hiIn(t, out, "config max_live_agents 3")
	hiIn(t, out, "project add demo --name demo --repo /r")
	hiIn(t, out, "systemctl --user start herdr-server.service factory-tick.timer factory-serve.service factory-up.service")
}

func TestHivemillInstallServeBindBeforeAnyServeStartInBothPlans(t *testing.T) {
	for _, c := range []struct {
		extra []string
		start string
	}{{nil, "systemctl --user start"}, {[]string{"--no-systemd"}, "serve --bind"}} {
		_, out, _ := hiRun(hiNew(t, hiHome(t)), append([]string{"--dry-run"}, c.extra...)...)
		bind, serve := -1, -1
		for i, ln := range strings.Split(out, "\n") {
			if bind < 0 && strings.Contains(ln, "config serve_bind 127.0.0.1") {
				bind = i
			}
			if serve < 0 && strings.Contains(ln, c.start) {
				serve = i
			}
		}
		if bind < 0 || serve < 0 {
			t.Errorf("%v: bind=%d serve=%d in\n%s", c.extra, bind, serve, out)
			continue
		}
		if bind >= serve {
			t.Errorf("%v: serve_bind (line %d) must precede the serve start (line %d)", c.extra, bind, serve)
		}
	}
}

func TestHivemillInstallNoModeIsUsage(t *testing.T) {
	rc, _, errb := hiRun(hiNew(t, hiHome(t)))
	hiEq(t, rc, 2, "rc")
	hiIn(t, errb, "--check")
}

func TestHivemillInstallTwoModesIsUsage(t *testing.T) {
	rc, _, _ := hiRun(hiNew(t, hiHome(t)), "--check", "--yes")
	hiEq(t, rc, 2, "rc")
}

func TestHivemillInstallRealEntryPointUsageAndNoTokenOption(t *testing.T) {
	for _, argv := range [][]string{nil, {"--gh-token", "x"}, {"--token", "x", "--yes"}, {"--project-id", "a", "--check"}} {
		rc, _, errb := hiBin(t, hiPlainEnv(t.TempDir()), argv...)
		hiEq(t, rc, 2, fmt.Sprintf("rc of %v: %s", argv, errb))
	}
}

func TestHivemillInstallRealEntryPointNeverEchoesARejectedToken(t *testing.T) {
	rc, out, errb := hiBin(t, hiPlainEnv(t.TempDir()), "--dry-run", "--gh-token", hiToken)
	hiEq(t, rc, 2, "rc")
	hiNotIn(t, out+errb, hiToken)
}

func TestHivemillInstallRealEntryPointNeverEchoesABadOptionValue(t *testing.T) {
	for _, opt := range []string{"--port", "--max-agents"} {
		rc, out, errb := hiBin(t, hiPlainEnv(t.TempDir()), "--dry-run", opt, hiToken)
		hiEq(t, rc, 2, opt)
		hiNotIn(t, out+errb, hiToken)
	}
}

func TestHivemillInstallRealEntryPointRefusesInsideAnAgentPane(t *testing.T) {
	env := append(hiPlainEnv(t.TempDir()), "FACTORY_AGENT=x")
	rc, out, errb := hiBin(t, env, "--dry-run")
	hiEq(t, rc, 1, "rc: "+out+errb)
	hiIn(t, out, "FACTORY_AGENT")
}

func TestHivemillInstallPortRangeAndDashRefAreUsage(t *testing.T) {
	rc, _, _ := hiRun(hiNew(t, hiHome(t)), "--dry-run", "--port", "70000")
	hiEq(t, rc, 2, "--port 70000")
	rc, _, _ = hiRun(hiNew(t, hiHome(t)), "--dry-run", "--ref", "--upload-pack=x")
	hiEq(t, rc, 2, "--ref --upload-pack=x")
}

// ------------------------------------------------------------------------------------------------ TestRefusals

func TestHivemillInstallRootRefusedBeforeAnyHostCall(t *testing.T) {
	h := hiNew(t, hiHome(t))
	h.euid = 0
	rc, out, _ := hiRun(h, "--yes")
	hiEq(t, rc, 1, "rc")
	hiIn(t, out, "root")
	hiEq(t, len(h.calls), 0, "host calls")
	hiEq(t, len(h.muts), 0, "mutations")
}

func TestHivemillInstallFactoryAgentRefused(t *testing.T) {
	h := hiNew(t, hiHome(t), hiEnv(map[string]string{"FACTORY_AGENT": "x", "PATH": "/usr/bin"}))
	rc, out, _ := hiRun(h, "--yes")
	hiEq(t, rc, 1, "rc")
	hiIn(t, out, "FACTORY_AGENT")
	hiEq(t, len(h.calls), 0, "host calls")
}

func TestHivemillInstallPlainUserPassesPreflight(t *testing.T) {
	rc, out, _ := hiRun(hiNew(t, hiHome(t)), "--dry-run")
	hiEq(t, rc, 0, "rc: "+out)
}

func TestHivemillInstallExistingDatabaseWithoutResumeExit4Untouched(t *testing.T) {
	home := hiHome(t)
	hiMakeInstall(t, home, true)
	h := hiNew(t, home)
	rc, out, _ := hiRun(h, "--yes")
	hiEq(t, rc, 4, "rc: "+out)
	hiEq(t, len(h.muts), 0, "mutations")
	joined := strings.Join(h.argvs(), " ")
	for _, word := range []string{"clone", "checkout", " config ", "systemctl", "init"} {
		hiNotIn(t, joined, word)
	}
}

func TestHivemillInstallPartialCloneWithoutResumeExit4(t *testing.T) {
	home := hiHome(t)
	hiMakeInstall(t, home, false)
	h := hiNew(t, home)
	rc, _, _ := hiRun(h, "--yes")
	hiEq(t, rc, 4, "rc")
	hiEq(t, len(h.muts), 0, "mutations")
}

func TestHivemillInstallOtherOriginExit1Untouched(t *testing.T) {
	home := hiHome(t)
	hiMakeInstall(t, home, true)
	h := hiNew(t, home)
	h.origin = "https://github.com/someone/else.git"
	rc, out, _ := hiRun(h, "--yes", "--resume")
	hiEq(t, rc, 1, "rc: "+out)
	hiEq(t, len(h.muts), 0, "mutations")
	hiNotIn(t, strings.Join(h.argvs(), " "), "config")
}

func TestHivemillInstallNotAGitDirExit1(t *testing.T) {
	home := hiHome(t)
	hiMakeInstall(t, home, true)
	h := hiNew(t, home)
	h.origin = ""
	rc, _, _ := hiRun(h, "--yes")
	hiEq(t, rc, 1, "rc")
	hiEq(t, len(h.muts), 0, "mutations")
}

func TestHivemillInstallSameOriginSpellingVariantsMatch(t *testing.T) {
	home := hiHome(t)
	hiMakeInstall(t, home, true)
	h := hiNew(t, home)
	h.origin = "https://github.com/VOLEKERB/hivemill"
	rc, _, _ := hiRun(h, "--yes")
	hiEq(t, rc, 4, "rc")
}

func TestHivemillInstallResumeRecordsNoCheckoutAndKeepsConfig(t *testing.T) {
	home := hiHome(t)
	hiMakeInstall(t, home, true)
	h := hiNew(t, home, hiRules(hiRule1("config serve_bind", 0, hiOther+"\n"), hiRule1("config serve_port", 0, "9100\n")))
	rc, out, _ := hiRun(h, "--yes", "--resume", "--no-systemd")
	hiEq(t, rc, 0, "rc: "+out)
	joined := h.argvs()
	if bad := hiFind(joined, func(a string) bool {
		return strings.Contains(a, "checkout") || strings.Contains(a, "clone") || strings.Contains(a, "fetch") || strings.Contains(a, "ls-remote")
	}); len(bad) > 0 {
		t.Errorf("a resume must not touch the clone: %v", bad)
	}
	setRe := regexp.MustCompile(`factory config \w+ \S+`)
	if bad := hiFind(joined, func(a string) bool { return setRe.MatchString(a) && !strings.Contains(a, "config serve_") }); len(bad) > 0 {
		t.Errorf("config was set on resume: %v", bad)
	}
	if bad := hiFind(joined, hiSub("config serve_bind 127.0.0.1")); len(bad) > 0 {
		t.Errorf("an existing bind must be kept: %v", bad)
	}
	if bad := hiFind(joined, func(a string) bool { return strings.HasSuffix(a, " init") }); len(bad) > 0 {
		t.Errorf("init ran on resume: %v", bad)
	}
	hiEq(t, hiLast(out), "RESULT PASS url=http://127.0.0.1:9100/ version=0.3.0", "result line")
}

func TestHivemillInstallResumeSetsOnlyPassedOptions(t *testing.T) {
	home := hiHome(t)
	hiMakeInstall(t, home, true)
	h := hiNew(t, home, hiRules(hiRule1("config serve_bind", 0, hiOther+"\n")))
	hiRun(h, "--yes", "--resume", "--no-systemd", "--max-agents", "4")
	setRe := regexp.MustCompile(`config (max_live_agents|serve_\w+|scout_enabled) \S+$`)
	sets := hiFind(h.argvs(), setRe.MatchString)
	hiEq(t, len(sets), 1, fmt.Sprint("config sets: ", sets))
	if len(sets) == 1 && !strings.HasSuffix(sets[0], "config max_live_agents 4") {
		t.Errorf("sets = %v", sets)
	}
}
