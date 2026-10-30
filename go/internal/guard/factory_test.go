package guard

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from tests/test_worker_guard_repoprot.py (R-a, gh), tests/test_worker_guard_data.py (the kill rule),
// the evaluator / identity / factory-unit cases of tests/test_worker_guard.py and the live-home derivation of
// worker-guard.py:derive_live_homes. Command texts verbatim; the expected exit is the Python test's.

const repoProtP = "repos/acme/hive/branches/main/protection"

var repoProtBlocked = []string{
	"gh api -X PUT " + repoProtP + " -F required_pull_request_reviews=null",
	"gh api --method PATCH " + repoProtP + "/enforce_admins",
	"gh api --method=DELETE " + repoProtP,
	"gh api -XPOST " + repoProtP + "/required_signatures",
	"gh api " + repoProtP + " -X delete",
	"gh api -H 'Accept: application/vnd.github+json' -X PUT " + repoProtP,
	"gh api -X PUT repos/acme/hive/branches/release/x/protection",
	"gh api " + repoProtP + " -f enforce_admins=false", // a body without -X makes gh use POST
	"gh api -X POST repos/acme/hive/rulesets --input r.json",
	"gh api -p luke-cage -X PUT " + repoProtP,
	"gh api --preview luke-cage --method PUT " + repoProtP,
	"gh api graphql -f query='mutation{updateBranchProtectionRule(input:{}){clientMutationId}}'",
	"gh api graphql -f query='mutation{updateRepository(input:{}){clientMutationId}}'",
	"gh api graphql -F query=@q.graphql",
	"gh alias set x 'api -X PUT repos/a/b/branches/main/protection'",
	"gh api -X PUT '" + repoProtP + "#x'",
	"gh api --field=a=1 " + repoProtP,
	"gh api -X PUT '" + repoProtP + "?x=1'",
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
	"gh api -X=DELETE " + repoProtP,
	"gh api --method=PATCH " + repoProtP + "/enforce_admins",
	"gh api -iX DELETE " + repoProtP,
	"gh api -iXDELETE " + repoProtP,
	"gh api -i -X=PUT " + repoProtP,
	"gh api -X DELETE repos/acme/hive/branches/main/%70rotection",
	"gh api -X PUT repos/acme/hive/branches/main/%50ROTECTION",
	"gh api -X PATCH repos/acme/hive%2F",
}

var repoProtAllowed = []string{
	"gh api " + repoProtP,
	"gh api -X GET " + repoProtP,
	"gh api --method=GET " + repoProtP + "/enforce_admins",
	"gh api -X=GET " + repoProtP,
	"gh api -iX GET " + repoProtP,
	"gh api -iXGET " + repoProtP,
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

func TestRepoProtEveryRoleIsRefusedEachChangingShape(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator", "staff-engineer", "boss", "director", "lead"} {
		hq := role == "boss" || role == "director" || role == "lead"
		r := role
		if hq {
			r = ""
		}
		for _, cmd := range repoProtBlocked {
			err := Check(cmd, "", r, hq)
			if err == nil {
				t.Errorf("should block (%s): %q", role, cmd)
				continue
			}
			if !strings.Contains(Explain(err), "branch protection and repository settings") {
				t.Errorf("%s %q: stderr %q", role, cmd, Explain(err))
			}
		}
	}
}

func TestRepoProtReadsAndUnrelatedWritesStayAllowed(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range []string{"coder", "judge", "director"} {
		hq := role == "director"
		r := role
		if hq {
			r = ""
		}
		for _, cmd := range repoProtAllowed {
			if err := Check(cmd, "", r, hq); err != nil {
				t.Errorf("should allow (%s): %q: %v", role, cmd, err)
			}
		}
	}
}

func TestGhRepoChangeHelpers(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"repo", "edit", "x"}, "gh repo edit"},
		{[]string{"alias", "set", "x", "y"}, "gh alias set"},
		{[]string{"alias", "import", "f"}, "gh alias import"},
		{[]string{"api", "-X", "PUT", "repos/o/r/rulesets"}, "gh api PUT repos/o/r/rulesets"},
		{[]string{"api", "-f", "a=1", "repos/o/r"}, "gh api POST repos/o/r"},
		{[]string{"api", "repos/o/r/rulesets"}, ""},
		{[]string{"api"}, ""},
		{[]string{}, ""},
		{[]string{"api", "graphql", "-f", "query=mutation{createRepositoryRuleset(input:{})}"}, "gh api graphql (settings mutation or a query read from a file)"},
		{[]string{"api", "graphql", "-f", "query=mutation{unarchiveRepository(input:{})}"}, "gh api graphql (settings mutation or a query read from a file)"},
		{[]string{"api", "graphql", "--input", "q"}, "gh api graphql (settings mutation or a query read from a file)"},
		{[]string{"api", "-X", "POST", "repos/o/r/rulesetsx"}, ""},
		{[]string{"api", "-X", "POST", "repos/o/r/branches/m/protectionx"}, ""},
		{[]string{"api", "-X", "POST", "repos/o/r/branches/m/protection\n"}, "gh api POST repos/o/r/branches/m/protection\n"},
	} {
		if got := ghRepoChange(c.args); got != c.want {
			t.Errorf("ghRepoChange(%q) = %q, want %q", c.args, got, c.want)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"a%2Fb", "a/b"}, {"%70rotection", "protection"}, {"%zz", "%zz"}, {"%e2%82", "�"}, {"a%", "a%"}, {"%2", "%2"},
	} {
		if got := pyUnquote(c.in); got != c.want {
			t.Errorf("pyUnquote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range []struct {
		in   string
		want string
	}{{"a#b", "a"}, {"a#b\nc", "a\nc"}, {"x", "x"}} {
		if got := stripHashComment(c.in); got != c.want {
			t.Errorf("stripHashComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── tests/test_worker_guard_data.py: the kill rule ──

func TestKillRuleBlockedWorkerMode(t *testing.T) {
	clearTermsEnv(t)
	for _, cmd := range []string{
		"pkill -f 'unittest discover'", "killall python3", "killall -9 python3", "nohup pkill -f x",
		"kill 1234", "kill -9 1234", "kill -s KILL 1234", "kill 0", "kill -9 -1", "kill -s KILL -1",
		"kill -- -123", "kill -TERM -123", "kill -1", "kill", "kill $pid", "kill $(pgrep unittest)",
		"timeout 5 kill 1", "env kill 5", "ls | xargs kill", "find . -name x -exec kill {} \\;",
		"bash -c 'kill 5'", "/bin/kill 5", "sh -c 'pkill x'", "echo a; kill 7",
	} {
		if err := Check(cmd, "", "", false); err == nil || !strings.Contains(Explain(err), "worker-guard") {
			t.Errorf("should block: %q (%v)", cmd, err)
		}
	}
}

func TestKillRuleOwnJobAndTextAllowed(t *testing.T) {
	clearTermsEnv(t)
	for _, cmd := range []string{
		"kill %1", "kill %%", "kill -TERM $!", "kill -s TERM %1", "kill -9 $!", "kill $!", "kill -l",
		"sleep 60 & kill $!", "grep pkill README.md", "ps aux | grep unittest", `git commit -m "no pkill"`,
		"echo 'killall and kill 1234 are blocked'",
		"gh pr comment 5 --body 'pkill is refused'",
	} {
		if err := Check(cmd, "", "", false); err != nil {
			t.Errorf("should allow: %q: %v", cmd, err)
		}
	}
	for _, cmd := range []string{"pkill -f x", "killall x", "kill 1234", "kill -9 -1"} {
		if err := Check(cmd, "", "", true); err != nil {
			t.Errorf("hq mode unaffected: %q: %v", cmd, err)
		}
	}
}

// ── identity, scrub, factory units, evaluator ──

func factoryFix(t *testing.T, agent string) {
	t.Helper()
	clearTermsEnv(t)
	SessionID = ""
	t.Setenv("FACTORY_HOME", t.TempDir())
	if agent == "" {
		unsetEnv(t, "FACTORY_AGENT")
	} else {
		t.Setenv("FACTORY_AGENT", agent)
	}
	unsetEnv(t, "TMPDIR")
}

func TestIdentityVariables(t *testing.T) {
	factoryFix(t, "coder-t-9")
	ok := []string{"FACTORY_AGENT=coder-t-9 factory whoami", "FACTORY_AGENT=coder-t-9 ls", "ls",
		"FACTORY_HOME=$(mktemp -d) FACTORY_AGENT=other factory whoami", "FACTORY_ROLE=x FACTORY_HOME=/tmp/zz-h ls",
		"export FACTORY_AGENT=coder-t-9", "FACTORY_HOME=$(mktemp -d -p /var/tmp) HERDR_PANE_ID=1 ls"}
	for _, cmd := range ok {
		if err := Check(cmd, "", "coder", false); err != nil {
			t.Errorf("should allow: %q: %v", cmd, err)
		}
	}
	bad := []string{"FACTORY_AGENT=owner factory whoami", "FACTORY_AGENT=other ls", "FACTORY_ROLE=x ls", "HERDR_PANE_ID=1 ls",
		"export FACTORY_ROLE=judge", "declare -x FACTORY_AGENT=x", "FACTORY_AGENT=coder-t-9 FACTORY_ROLE=pm ls",
		"FACTORY_ROLE=x FACTORY_HOME=$HOME/factory ls", "FACTORY_ROLE=x FACTORY_HOME=~/factory/worktrees/x ls",
		"FACTORY_ROLE=x FACTORY_HOME=$(mktemp -d -p ~/factory) ls", "FACTORY_ROLE=x FACTORY_HOME=$(mktemp -u) ls",
		"FACTORY_ROLE=x ls; FACTORY_HOME=/tmp/zz ls", "FACTORY_ROLE+=x ls", "FACTORY_ROLE=x"}
	for _, cmd := range bad {
		if err := Check(cmd, "", "coder", false); err == nil {
			t.Errorf("should block: %q", cmd)
		}
	}
	// the exemption is for the hook's own FACTORY_AGENT only: an unset one, or the owner, never matches
	factoryFix(t, "")
	if err := Check("FACTORY_AGENT=x ls", "", "coder", false); err == nil {
		t.Error("FACTORY_AGENT=x allowed without an own agent")
	}
	factoryFix(t, "owner")
	if err := Check("FACTORY_AGENT=owner ls", "", "coder", false); err == nil {
		t.Error("FACTORY_AGENT=owner allowed")
	}
}

func TestMktempHomeFallsBackToTMPDIR(t *testing.T) {
	factoryFix(t, "")
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", home) // outside the live home: the bare mktemp form is a temp home
	if err := Check("FACTORY_ROLE=x FACTORY_HOME=$(mktemp -d) ls", "", "coder", false); err != nil {
		t.Errorf("mktemp under a TMPDIR outside the live home: %v", err)
	}
	live := mustHost().liveHome
	t.Setenv("TMPDIR", live)
	if err := Check("FACTORY_ROLE=x FACTORY_HOME=$(mktemp -d) ls", "", "coder", false); err == nil {
		t.Error("mktemp under TMPDIR=<live home> allowed")
	}
	t.Setenv("TMPDIR", home)
	if err := Check("TMPDIR=/x FACTORY_ROLE=x FACTORY_HOME=$(mktemp -d) ls", "", "coder", false); err == nil {
		t.Error("a TMPDIR= assignment next to a bare mktemp allowed")
	}
}

func TestScrubVariables(t *testing.T) {
	factoryFix(t, "")
	for _, cmd := range []string{"unset FACTORY_ROLE", "unset HERDR_PANE_ID", "env -u FACTORY_ROLE ls", "env --unset=HERDR_PANE_ID ls",
		"env --unset FACTORY_ROLE ls", "env -uFACTORY_ROLE ls", "env -i factory whoami", "env - factory whoami",
		"env --ignore-environment factory whoami", "env -iu X factory whoami", "disown; factory whoami", "setsid ls"} {
		if err := Check(cmd, "", "coder", false); err == nil {
			t.Errorf("should block: %q", cmd)
		}
	}
	for _, cmd := range []string{"env -i ls", "env -u PATH ls", "unset FOO", "env FOO=1 factory whoami"} {
		if err := Check(cmd, "", "coder", false); err != nil {
			t.Errorf("should allow: %q: %v", cmd, err)
		}
	}
}

func TestFactoryUnitCommands(t *testing.T) {
	factoryFix(t, "")
	tmp := t.TempDir()
	for _, cmd := range []string{"factory up", "factory tick", "factory relaunch x", "factory spawn coder x", "factory clean",
		"python3 bin/factory up", "python3 bin/factory_cli.py spawn x", "python3 -m factory_cli up", "python3 -c 'import factory_cli'",
		"FACTORY_HOME=$HOME/factory factory up", "FACTORY_HOME=$X factory up", "FACTORY_HOME= factory up",
		"timeout 5 factory up", "bash -c 'factory up'", "sh -c 'FACTORY_HOME=~/factory factory tick'"} {
		if err := Check(cmd, "", "coder", false); err == nil {
			t.Errorf("should block: %q", cmd)
		}
	}
	for _, cmd := range []string{"FACTORY_HOME=" + tmp + " factory up", "FACTORY_HOME=" + tmp + " python3 bin/factory spawn x",
		"factory task show T-1", "factory whoami", "factory inbox", "factory msg lead x y", "python3 bin/factory whoami",
		"env FACTORY_HOME=" + tmp + " factory tick"} {
		if err := Check(cmd, "", "coder", false); err != nil {
			t.Errorf("should allow: %q: %v", cmd, err)
		}
	}
	// the cleaner may run `factory clean` against the live home, no other role
	if err := Check("factory clean", "", "cleaner", false); err != nil {
		t.Errorf("cleaner factory clean: %v", err)
	}
	if err := Check("factory up", "", "cleaner", false); err == nil {
		t.Error("cleaner factory up allowed")
	}
}

func TestBroadReads(t *testing.T) {
	factoryFix(t, "")
	tmp := t.TempDir()
	for _, cmd := range []string{"factory res", "factory agents", "factory scorecard", "factory digest", "factory config",
		"factory goal list", "factory task list", "factory task ready", "factory team list", "factory project list",
		"factory task --goal G-005 list", "factory goal --all list", "factory goal show G-005", "factory goal show",
		"factory goal show --full G-005", "factory --x res"} {
		if err := Check(cmd, "", "coder", false); err == nil {
			t.Errorf("should block: %q", cmd)
		} else if !strings.Contains(Explain(err), "broad factory reads") {
			t.Errorf("%q: stderr %q", cmd, Explain(err))
		}
	}
	for _, cmd := range []string{"factory goal audit G-005 --ready", "factory task show T-1", "factory goal", "factory task",
		"FACTORY_HOME=" + tmp + " factory res", "FACTORY_HOME=$(mktemp -d) factory task list", "factory res --help"[:0] + "factory whoami"} {
		if err := Check(cmd, "", "coder", false); err != nil {
			t.Errorf("should allow: %q: %v", cmd, err)
		}
	}
	if err := Check("factory res", "", "staff-engineer", false); err != nil {
		t.Errorf("staff-engineer factory res: %v", err)
	}
	if err := Check("factory agents", "", "cleaner", false); err != nil {
		t.Errorf("cleaner factory agents: %v", err)
	}
	if err := Check("factory res", "", "cleaner", false); err == nil {
		t.Error("cleaner factory res allowed")
	}
}

func TestEvaluatorFactorySubset(t *testing.T) {
	factoryFix(t, "")
	temp := t.TempDir()
	t.Setenv("FACTORY_HOME", temp)
	for _, cmd := range []string{"factory whoami", "factory inbox", "factory msg a b c", "factory done x", "factory task show T-1"} {
		if err := Check(cmd, "", "evaluator", false); err != nil {
			t.Errorf("should allow: %q: %v", cmd, err)
		}
	}
	for _, cmd := range []string{"factory spawn x", "factory task list", "factory res", "factory", "factory --x", "factory task update T-1",
		"factory goal show G-1", "python3 bin/factory goal list"} {
		if err := Check(cmd, "", "evaluator", false); err == nil {
			t.Errorf("should block: %q", cmd)
		}
	}
	// only a temp FACTORY_HOME: the hook's env pointing at the live home (or nowhere) refuses everything
	live := mustHost().liveHome
	for _, home := range []string{live, filepath.Join(live, "worktrees", "x"), ""} {
		if home == "" {
			unsetEnv(t, "FACTORY_HOME")
		} else {
			t.Setenv("FACTORY_HOME", home)
		}
		if err := Check("factory whoami", "", "evaluator", false); err == nil {
			t.Errorf("evaluator factory whoami with FACTORY_HOME=%q allowed", home)
		}
	}
	// the command's own FACTORY_HOME= decides when given
	t.Setenv("FACTORY_HOME", live)
	if err := Check("FACTORY_HOME="+temp+" factory whoami", "", "evaluator", false); err != nil {
		t.Errorf("evaluator with its own temp home: %v", err)
	}
	if err := Check("FACTORY_HOME="+live+" factory whoami", "", "evaluator", false); err == nil {
		t.Error("evaluator with the live home allowed")
	}
	// FACTORY_HOME may appear only as one leading prefix (evaluator_home_text)
	for _, cmd := range []string{"export FACTORY_HOME=" + temp, "echo FACTORY_HOME", "FACTORY_HOME=" + temp + " factory whoami; FACTORY_HOME=x ls"} {
		if err := Check(cmd, "", "evaluator", false); err == nil {
			t.Errorf("should block: %q", cmd)
		}
	}
}

// ── the ~/.config/factory and run/settings detector (T-0212) ──

func TestCfgDirWritesBlockedReadsAllowed(t *testing.T) {
	factoryFix(t, "")
	pw := pwHome(t)
	for _, cmd := range []string{"echo /x > ~/.config/factory/live_home", "echo /x >> " + pw + "/.config/factory/live_home",
		"echo x | tee ~/.config/factory/live_home", "rm ~/.config/factory/live_home", "rm -r ~/.config", "rmdir ~/.config/factory",
		"mv ~/.config /tmp/c", "mv /tmp/x ~/.config/factory", "cp /tmp/x ~/.config/factory/live_home", "install /tmp/x ~/.config/factory/",
		"cp -t ~/.config/factory /tmp/x", "dd if=/tmp/x of=$HOME/.config/factory/live_home", "touch ~/.config/factory/live_home",
		"sed -i s/a/b/ ~/.config/factory/live_home", "python3 -c \"open('/x/.config/factory/live_home','w')\"",
		"vim ~/.config/factory/live_home", "perl -pi -e 1 ~/.config/factory/live_home",
		"factory init --live-home /x", "factory init --live /x", "python3 bin/factory init --live-home=/x",
		"cd ~/.config/factory && echo x > live_home", "cd && cd .config/factory && touch live_home",
		"echo x > " + mustHost().liveHome + "/run/settings/coder.json", "chmod 777 " + mustHost().liveHome + "/run/settings",
		"echo x > $X/.config/factory/live_home", "ln -sf /tmp/x ~/.config/factory/live_home"} {
		if err := Check(cmd, "", "coder", false); err == nil {
			t.Errorf("should block: %q", cmd)
		}
	}
	for _, cmd := range []string{"cat ~/.config/factory/live_home", "grep x ~/.config/factory/live_home", "cp ~/.config/factory/live_home /tmp/x",
		"ls ~/.config", "ls ~/.config/factory", "echo x > ~/.config/factoryx", "factory init", "echo live_homes > /tmp/x",
		"cd /tmp && echo x > live_home_x", "cat " + mustHost().liveHome + "/run/settings/coder.json", "rm -r ~/.cache", "mv /tmp/a /tmp/b",
		"python3 -c \"open('live_'+'home','w')\"", "echo x > ~/.config/f*/live_home"} {
		if err := Check(cmd, "", "coder", false); err != nil {
			t.Errorf("should allow: %q: %v", cmd, err)
		}
	}
	// HQ mode runs only the PRIV pass
	if err := Check("echo /x > ~/.config/factory/live_home", "", "", true); err != nil {
		t.Errorf("hq redirect: %v", err)
	}
}

func TestCfgHelpers(t *testing.T) {
	pw := mustHost().pwDir
	for _, c := range []struct{ in, want string }{
		{"~", pw}, {"~/x", pw + "/x"}, {"$HOME/x", pw + "/x"}, {"${HOME}/x", pw + "/x"}, {"$HOMEX", "$HOMEX"}, {"~x", "~x"},
		{"x/~", "x/~"}, {"~\n", pw + "\n"}, {"", ""},
	} {
		if got := cfgExpand(c.in); got != c.want {
			t.Errorf("cfgExpand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	a, err := NewAnalyzer("/work", "coder", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		rest []string
		want string
		ok   bool
	}{
		{"cd", []string{"sub"}, "/work/sub", true}, {"cd", []string{"/abs"}, "/abs", true}, {"cd", nil, pw, true},
		{"cd", []string{"-"}, "", false}, {"cd", []string{"$X"}, "", false}, {"popd", nil, "", false},
		{"cd", []string{"~/y"}, pw + "/y", true},
	} {
		a.ccwd, a.ccwdKnown = "/work", true
		a.cfgCd(c.name, c.rest)
		if a.ccwdKnown != c.ok || (c.ok && a.ccwd != c.want) {
			t.Errorf("cfgCd(%s %v) = %q known %v, want %q known %v", c.name, c.rest, a.ccwd, a.ccwdKnown, c.want, c.ok)
		}
	}
	// after an unknown directory a relative target becomes the directory itself (Python: `p if ... or ccwd is None`)
	a.ccwd, a.ccwdKnown = "", false
	a.cfgCd("cd", []string{"rel"})
	if !a.ccwdKnown || a.ccwd != "rel" {
		t.Errorf("cfgCd after an unknown dir = %q known %v, want rel", a.ccwd, a.ccwdKnown)
	}
	for _, c := range []struct {
		in   string
		want bool
	}{{"live_home", true}, {"x.live_home", true}, {"live_homes", false}, {"alive_home", false}, {"'live_'+'home'", true},
		{".config/factory", true}, {"run/settings", true}, {"live home", false}, {"l i v e", false}, {"live_hom", false}} {
		if got := CfgCode(c.in); got != c.want {
			t.Errorf("CfgCode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// ── derive_live_homes (acceptance 3: the Go function and the Python one print the same homes for the same input) ──

const pyDeriveScript = `
import importlib.machinery, importlib.util, json, pwd, sys, types
base, hook = sys.argv[1], sys.argv[2]
loader = importlib.machinery.SourceFileLoader("wg", hook)
mod = importlib.util.module_from_spec(importlib.util.spec_from_loader("wg", loader))
loader.exec_module(mod)
pwd.getpwuid = lambda uid: types.SimpleNamespace(pw_dir=base)
primary, homes = mod.derive_live_homes()
print(json.dumps([primary, sorted(homes)]))
`

func TestDeriveLiveHomesMatchesPython(t *testing.T) {
	hook, err := filepath.Abs("../../../roles/hooks/worker-guard.py")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(live string, perm os.FileMode) string {
		base, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if live == "-" { // no file at all
			return base
		}
		dir := filepath.Join(base, ".config", "factory")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "live_home")
		if err := os.WriteFile(p, []byte(live), perm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, perm); err != nil {
			t.Fatal(err)
		}
		return base
	}
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "lnk")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		base string
	}{
		{"no file", mk("-", 0)},
		{"valid", mk(other+"\n", 0o600)},
		{"valid without a newline", mk(other, 0o644)},
		{"symlinked target resolves", mk(link+"\n", 0o600)},
		{"group writable", mk(other+"\n", 0o620)},
		{"other writable", mk(other+"\n", 0o602)},
		{"relative", mk("rel/path\n", 0o600)},
		{"two lines", mk(other+"\n"+other+"\n", 0o600)},
		{"empty", mk("", 0o600)},
		{"nul", mk(other+"\x00x\n", 0o600)},
		{"not utf-8", mk("\xff"+other+"\n", 0o600)},
		{"cr line end", mk(other+"\r\n", 0o600)},
		{"default only", mk(filepath.Join(mkTmpFactory(t), "factory")+"\n", 0o600)},
	} {
		out, err := exec.Command("python3", "-I", "-c", pyDeriveScript, c.base, hook).Output()
		if err != nil {
			t.Fatalf("%s: python: %v", c.name, err)
		}
		var py []any
		if err := json.Unmarshal(out, &py); err != nil {
			t.Fatalf("%s: %v (%s)", c.name, err, out)
		}
		primary, homes := LiveHomesOf(c.base)
		got, _ := json.Marshal([]any{primary, homes})
		want, _ := json.Marshal(py)
		if string(got) != string(want) {
			t.Errorf("%s: go %s, python %s", c.name, got, want)
		}
	}
}

func mkTmpFactory(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAnalyzerLiveHomesAddTheHooksRepoRoot(t *testing.T) {
	root := t.TempDir()
	hook := filepath.Join(root, "roles", "hooks", "worker-guard.py")
	SetPyHook(hook)
	t.Cleanup(func() { SetPyHook("") })
	found := false
	for _, h := range analyzerLiveHomes() {
		if h == pyRealpath(root) {
			found = true
		}
	}
	if !found {
		t.Errorf("analyzerLiveHomes() = %v, want it to hold %s (dirname^3 of the hook)", analyzerLiveHomes(), root)
	}
	SetPyHook("")
	for _, h := range analyzerLiveHomes() {
		if h == pyRealpath(root) {
			t.Error("repo root of an unnamed hook counted as live")
		}
	}
}
