package sandbox

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func (f *fixture) gate(subuid bool) GateArgs {
	return GateArgs{Cmd: "make test", Tree: f.root + "/gate", FactoryMain: f.root + "/main", WtRoot: f.root + "/wt",
		Env: map[string]string{"PATH": "/usr/bin"}, Home: f.home, Runtime: f.run, Subuid: subuid, Unshare: "/usr/bin/unshare", Seams: f.seams(),
		Probe:     func(bool) (bool, string) { return true, "ok" },
		CommonDir: func(string) (string, error) { return f.root + "/outside.git", nil },
		SizeOf:    func(string) (int64, error) { return 1 << 20, nil }}
}

func cfgOf(t *testing.T, argv []string) *Cfg {
	t.Helper()
	for i, a := range argv {
		if (a == "--setup" || a == "--launch") && i+1 < len(argv) {
			var c Cfg
			must(t, jsonUnmarshal(argv[i+1], &c))
			return &c
		}
	}
	t.Fatalf("no cfg in %v", argv)
	return nil
}

func TestGatebox(t *testing.T) {
	f := newFixture(t)
	t.Run("the gate tree and the three scratch dirs are the only writable binds", func(t *testing.T) {
		argv, _, err := BuildGate(f.gate(false))
		must(t, err)
		c := cfgOf(t, argv)
		want := []string{f.root + "/gate", f.root + "/wt/reports", f.root + "/wt/factory-home", f.root + "/wt/tmp"}
		if got := sortedCopy(c.Rw); strings.Join(got, ",") != strings.Join(sortedCopy(want), ",") {
			t.Errorf("rw = %v, want %v", c.Rw, want)
		}
	})
	t.Run("FACTORY_MAIN, the git common dir and the worktree pointer are read-only", func(t *testing.T) {
		argv, _, _ := BuildGate(f.gate(false))
		c := cfgOf(t, argv)
		for _, p := range []string{f.root + "/main", f.root + "/outside.git", f.root + "/gate/.git"} {
			if !contains(c.Ro, p) {
				t.Errorf("%s is not read-only: ro = %v", p, c.Ro)
			}
			if contains(c.Rw, p) {
				t.Errorf("%s is writable", p)
			}
		}
	})
	t.Run("a common dir inside FACTORY_MAIN is not listed twice", func(t *testing.T) {
		a := f.gate(false)
		a.CommonDir = func(string) (string, error) { return f.root + "/main/.git", nil }
		argv, _, _ := BuildGate(a)
		if c := cfgOf(t, argv); contains(c.Ro, f.root+"/main/.git") {
			t.Errorf("ro = %v", c.Ro)
		}
	})
	t.Run("the state dirs of FACTORY_MAIN are masked", func(t *testing.T) {
		argv, _, _ := BuildGate(f.gate(false))
		c := cfgOf(t, argv)
		for _, d := range []string{"db", "logs", "worktrees"} {
			if !contains(c.Masks, f.root+"/main/"+d) {
				t.Errorf("%s not masked: %v", d, c.Masks)
			}
		}
		if contains(c.Masks, f.root+"/main/run") {
			t.Error("a dir that does not exist was masked")
		}
	})
	t.Run("home, runtime and every tmp dir are hidden; no network; own pid namespace", func(t *testing.T) {
		argv, _, _ := BuildGate(f.gate(false))
		c := cfgOf(t, argv)
		for _, d := range []string{f.home, f.run, "/tmp", "/dev/shm", "/var/tmp"} {
			if !contains(c.HideDirs, d) {
				t.Errorf("%s not hidden: %v", d, c.HideDirs)
			}
		}
		if c.Net || c.NoPid || c.ForwardSock != nil || !contains(argv, "-n") {
			t.Errorf("net=%v nopid=%v argv=%v", c.Net, c.NoPid, argv[:8])
		}
	})
	t.Run("the command runs as bash -c with the string kept whole", func(t *testing.T) {
		a := f.gate(false)
		a.Cmd = "echo 'a b'; echo $(id)"
		argv, _, _ := BuildGate(a)
		n := len(argv)
		if argv[n-3] != "bash" || argv[n-2] != "-c" || argv[n-1] != a.Cmd {
			t.Errorf("tail = %v", argv[n-3:])
		}
	})
	t.Run("env is an allow-list", func(t *testing.T) {
		a := f.gate(false)
		a.Env = map[string]string{"PATH": "/p", "LC_ALL": "C", "GH_TOKEN": "t", "SSH_AUTH_SOCK": "s", "FACTORY_AGENT": "a", "FACTORY_HOME": "/live",
			"GATE_SHARDS": "3", "OPENAI_API_KEY": "k", "HOME": "/real"}
		_, env, err := BuildGate(a)
		must(t, err)
		for _, gone := range []string{"GH_TOKEN", "SSH_AUTH_SOCK", "FACTORY_AGENT", "OPENAI_API_KEY"} {
			if _, has := env[gone]; has {
				t.Errorf("%s reached the gate", gone)
			}
		}
		if env["HOME"] != f.home || env["FACTORY_GATEBOX"] != "1" || env["TMPDIR"] != f.root+"/wt/tmp" || env["GATE_SHARDS"] != "3" || env["LC_ALL"] != "C" {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("defaults for FACTORY_MAIN and the report dir only when unset", func(t *testing.T) {
		_, env, _ := BuildGate(f.gate(false))
		if env["FACTORY_MAIN"] != f.root+"/main" || env["FACTORY_HOME"] != f.root+"/wt/factory-home" || env["GATE_REPORT_DIR"] != f.root+"/wt/reports" {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("subuid mode binds nothing writable and copies the tree into a tmpfs", func(t *testing.T) {
		argv, env, err := BuildGate(f.gate(true))
		must(t, err)
		c := cfgOf(t, argv)
		if len(c.Rw) != 0 || !c.Subuid || c.Copy == nil || c.Copy.Tree != f.root+"/gate" || !contains(c.Ro, f.root+"/gate") {
			t.Errorf("rw=%v subuid=%v copy=%+v ro=%v", c.Rw, c.Subuid, c.Copy, c.Ro)
		}
		if c.Copy.Size != "130m" {
			t.Errorf("copy size = %q (1 MiB tree: 2 MiB + 128m)", c.Copy.Size)
		}
		if env["TMPDIR"] != f.root+"/gate/.factory-box/tmp" || argv[1] != "--launch" {
			t.Errorf("tmpdir=%q argv=%v", env["TMPDIR"], argv[:3])
		}
	})
	t.Run("userns unavailable: refused, never run unsandboxed", func(t *testing.T) {
		a := f.gate(false)
		a.Probe = func(bool) (bool, string) { return false, "restricted" }
		argv, _, err := BuildGate(a)
		var ge *GateboxError
		if !errors.As(err, &ge) || argv != nil || !strings.Contains(err.Error(), "must not run unsandboxed") || !strings.Contains(err.Error(), "restricted") {
			t.Errorf("argv=%v err=%v", argv, err)
		}
	})
	t.Run("subuid unavailable: refused, never run as the owner uid", func(t *testing.T) {
		a := f.gate(true)
		a.Probe = func(sub bool) (bool, string) { return !sub, "no uidmap" }
		_, _, err := BuildGate(a)
		if err == nil || !strings.Contains(err.Error(), "must not run as the owner uid") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a tree that is not a directory is refused", func(t *testing.T) {
		a := f.gate(false)
		a.Tree = f.root + "/nope"
		if _, _, err := BuildGate(a); err == nil || !strings.Contains(err.Error(), "gate tree") {
			t.Errorf("err = %v", err)
		}
		a = f.gate(false)
		a.FactoryMain = f.root + "/nope"
		if _, _, err := BuildGate(a); err == nil || !strings.Contains(err.Error(), "FACTORY_MAIN") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a git failure is refused", func(t *testing.T) {
		a := f.gate(false)
		a.CommonDir = nil
		a.Tree = f.root + "/keep" // not a repo
		a.FactoryMain = f.root + "/main"
		if _, _, err := BuildGate(a); err == nil || !strings.Contains(err.Error(), "git-common-dir") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the scratch dirs are created", func(t *testing.T) {
		a := f.gate(false)
		a.WtRoot = f.root + "/fresh-wt"
		_, _, err := BuildGate(a)
		must(t, err)
		for _, d := range []string{"reports", "factory-home", "tmp"} {
			if fi, err := os.Stat(f.root + "/fresh-wt/" + d); err != nil || !fi.IsDir() {
				t.Errorf("%s not created: %v", d, err)
			}
		}
	})
	t.Run("a wt_root that cannot be created is refused", func(t *testing.T) {
		a := f.gate(false)
		a.WtRoot = f.root + "/gate/.git/inside-a-file"
		if _, _, err := BuildGate(a); err == nil || !strings.Contains(err.Error(), "cannot create") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestTestbox(t *testing.T) {
	f := newFixture(t)
	env := map[string]string{"PATH": "/p", "FACTORY_HOME": "/live", "GATE_SHARDS": "2", "GATE_REPORT_DIR": "/r", "LC_ALL": "C", "GH_TOKEN": "t"}
	t.Run("the argv list becomes one shell-quoted command", func(t *testing.T) {
		argv, _, err := BuildTest([]string{"go", "test", "-run", "A B", "x;y"}, f.root+"/gate", f.root+"/main", f.root+"/wt", env, f.gate(false))
		must(t, err)
		if got := argv[len(argv)-1]; got != "go test -run 'A B' 'x;y'" {
			t.Errorf("command = %q", got)
		}
	})
	t.Run("the live state dir and the gate knobs never reach a test run", func(t *testing.T) {
		_, out, err := BuildTest([]string{"true"}, f.root+"/gate", f.root+"/main", f.root+"/wt", env, f.gate(false))
		must(t, err)
		if out["FACTORY_HOME"] != f.root+"/wt/factory-home" || out["GATE_SHARDS"] != "" || out["GATE_REPORT_DIR"] != f.root+"/wt/reports" {
			t.Errorf("env = %v", out)
		}
		if _, has := out["GH_TOKEN"]; has {
			t.Error("GH_TOKEN reached the test run")
		}
	})
	t.Run("a build failure is a TestboxError with the testbox prefix", func(t *testing.T) {
		a := f.gate(false)
		a.Probe = func(bool) (bool, string) { return false, "restricted" }
		_, _, err := BuildTest([]string{"true"}, f.root+"/gate", f.root+"/main", f.root+"/wt", env, a)
		var te *TestboxError
		if !errors.As(err, &te) || !strings.HasPrefix(err.Error(), "testbox:") || strings.Contains(err.Error(), "gatebox:") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a scratch dir is fresh, disk-backed and under the tree", func(t *testing.T) {
		a, err := NewScratch(f.root + "/gate")
		must(t, err)
		b, _ := NewScratch(f.root + "/gate")
		if a == b || !strings.HasPrefix(a, f.root+"/gate/.factory-test-") {
			t.Errorf("scratch = %s, %s", a, b)
		}
		if _, err := NewScratch(f.root + "/nope"); err == nil || !strings.Contains(err.Error(), "the command was not run") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("joinQuoted", func(t *testing.T) {
		for in, want := range map[string][]string{"a": {"a"}, "'a b' c": {"a b", "c"}, "''": {""}, "'it'\"'\"'s'": {"it's"}} {
			if got := joinQuoted(want); got != in {
				t.Errorf("joinQuoted(%q) = %q, want %q", want, got, in)
			}
		}
	})
}

func TestSandboxEval(t *testing.T) {
	f := newFixture(t)
	trial := f.trials + "/tr0001"
	t.Run("TrialName", func(t *testing.T) {
		for in, want := range map[int]string{1: "tr0001", 42: "tr0042", 12345: "tr12345"} {
			if got := TrialName(in); got != want {
				t.Errorf("TrialName(%d) = %q", in, got)
			}
		}
	})
	t.Run("the eval cfg binds only the trial rw and forwards through the proxy", func(t *testing.T) {
		c, err := EvalNsCfg(trial, f.home, f.claude, f.root+"/vendor", "/usr/bin/unshare", f.seams())
		must(t, err)
		if len(c.Rw) != 1 || c.Rw[0] != trial || c.ForwardSock == nil || c.EnvSet["HTTPS_PROXY"] != ProxyURL {
			t.Errorf("rw=%v fwd=%v env=%v", c.Rw, c.ForwardSock, c.EnvSet)
		}
		for _, want := range []string{trial + "/bin", trial + "/scout", trial + "/role", f.root + "/vendor"} {
			if !contains(c.Ro, want) {
				t.Errorf("%s not read-only: %v", want, c.Ro)
			}
		}
		if !contains(c.Masks, f.trials) {
			t.Errorf("siblings are not masked: %v", c.Masks)
		}
		if *c.Cwd != trial+"/work" {
			t.Errorf("cwd = %q", *c.Cwd)
		}
	})
	t.Run("no vendored plugin means no extra mount", func(t *testing.T) {
		c, _ := EvalNsCfg(trial, f.home, f.claude, "", "/usr/bin/unshare", f.seams())
		if contains(c.Ro, f.root+"/vendor") {
			t.Errorf("ro = %v", c.Ro)
		}
	})
	t.Run("CheckToken", func(t *testing.T) {
		d := t.TempDir()
		ok := tokenFile(t, d, "ok", "x", 0o600)
		must(t, os.Symlink(ok, d+"/link"))
		cases := map[string]struct {
			path string
			ok   bool
		}{
			"0600 file": {ok, true}, "0644": {tokenFile(t, d, "w", "x", 0o644), false}, "0640": {tokenFile(t, d, "g", "x", 0o640), false},
			"0400": {tokenFile(t, d, "r", "x", 0o400), false}, "symlink": {d + "/link", false}, "missing": {d + "/none", false}, "directory": {d, false},
		}
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				err := CheckToken(c.path)
				if (err == nil) != c.ok || (err != nil && (!errors.Is(err, ErrSandboxRefused) || !strings.Contains(err.Error(), NoTokenMsg))) {
					t.Errorf("CheckToken = %v, want ok=%v", err, c.ok)
				}
			})
		}
	})
	t.Run("ClaudeJSON trusts the real path of the work dir", func(t *testing.T) {
		m := ClaudeJSON(trial + "/work")
		p := m["projects"].(map[string]any)[trial+"/work"].(map[string]any)
		if m["hasCompletedOnboarding"] != true || p["hasTrustDialogAccepted"] != true || p["hasCompletedProjectOnboarding"] != true {
			t.Errorf("seed = %v", m)
		}
		if _, has := claudeSeed["projects"].(map[string]any)[trial+"/work"]; has {
			t.Error("the shared seed was mutated")
		}
	})
	t.Run("preflight is the real view without a proxy", func(t *testing.T) {
		c, _ := EvalNsCfg(trial, f.home, f.claude, "", "/usr/bin/unshare", f.seams())
		argv, err := PreflightArgv(c)
		must(t, err)
		pc := cfgOf(t, argv)
		if pc.ForwardSock != nil || argv[len(argv)-1] != "--version" || c.ForwardSock == nil {
			t.Errorf("preflight fwd=%v orig fwd=%v argv tail=%v", pc.ForwardSock, c.ForwardSock, argv[len(argv)-2:])
		}
	})
	t.Run("EvalEnvKeys has no token or identity variable", func(t *testing.T) {
		for _, k := range EvalEnvKeys {
			if strings.Contains(k, "TOKEN") || strings.Contains(k, "KEY") {
				t.Errorf("%s in EvalEnvKeys", k)
			}
		}
	})
}

// --- real namespaces: skipped with the reason where user namespaces are unavailable (the Python tests' condition) ---

func requireUserns(t *testing.T) {
	t.Helper()
	if ok, why := UsernsAvailable("unshare"); !ok {
		t.Skipf("user namespaces unavailable: %s", why)
	}
	if _, err := exec.LookPath("setpriv"); err != nil {
		t.Skip("setpriv not installed")
	}
}

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, a := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t-e.invalid", "commit", "-q", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, a...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
}

// TestGateboxReal runs the gate profile in real namespaces: the secrets of a fake home are not visible, the main
// checkout is read-only, the gate tree is writable and there is no network.
func TestGateboxReal(t *testing.T) {
	requireUserns(t)
	f := newFixture(t)
	must(t, os.Remove(f.root+"/gate/.git")) // the fixture's pointer file: replace by a real repo
	gitRepo(t, f.root+"/gate")
	touch(t, f.home+"/.ssh/id_ed25519", 0o600)
	must(t, os.WriteFile(f.home+"/.ssh/id_ed25519", []byte("FAKE-SSH-SECRET"), 0o600))
	must(t, os.WriteFile(f.run+"/bus", []byte("FAKE-BUS"), 0o600))
	a := f.gate(false)
	a.Probe, a.CommonDir, a.SizeOf = nil, nil, nil
	a.Seams = Opts{Self: os.Args[0]}
	a.Unshare = ""
	a.Env = map[string]string{"PATH": os.Getenv("PATH"), "GH_TOKEN": "LEAKED-TOKEN"}
	script := `cat "$HOME/.ssh/id_ed25519" 2>&1; echo "rc-ssh=$?";
cat ` + f.run + `/bus 2>&1; echo "rc-bus=$?";
touch ` + f.root + `/main/owned 2>&1; echo "rc-main=$?";
touch ` + f.root + `/gate/written && echo "rc-tree=0";
echo "token=[$GH_TOKEN]"; echo "marker=$FACTORY_GATEBOX";
cat /proc/net/dev | tail -n +3 | cut -d: -f1 | tr -d ' ' | tr '\n' ','; echo;
id -u`
	a.Cmd = script
	res, err := RunGate(a, time.Minute)
	must(t, err)
	if res.TimedOut {
		t.Fatalf("timed out: %s", res.Output)
	}
	out := res.Output
	if strings.Contains(out, "FAKE-SSH-SECRET") || strings.Contains(out, "FAKE-BUS") || strings.Contains(out, "LEAKED-TOKEN") {
		t.Errorf("a secret is visible inside the box:\n%s", out)
	}
	for _, want := range []string{"rc-ssh=1", "rc-bus=1", "rc-tree=0", "token=[]", "marker=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "rc-main=0") {
		t.Errorf("FACTORY_MAIN is writable from the box:\n%s", out)
	}
	if _, err := os.Stat(f.root + "/main/owned"); err == nil {
		t.Error("the box created a file in FACTORY_MAIN")
	}
	if !strings.Contains(out, "lo,") || strings.Contains(out, "eth") || strings.Contains(out, "ens") {
		t.Errorf("the box has more than lo:\n%s", out)
	}
	if _, err := os.Stat(f.root + "/gate/written"); err != nil {
		t.Errorf("the gate tree write did not land: %v", err)
	}
}

func TestGateboxTimeoutKillsTree(t *testing.T) {
	requireUserns(t)
	f := newFixture(t)
	must(t, os.Remove(f.root+"/gate/.git"))
	gitRepo(t, f.root+"/gate")
	a := f.gate(false)
	a.Probe, a.CommonDir, a.SizeOf = nil, nil, nil
	a.Seams = Opts{Self: os.Args[0]}
	a.Unshare = ""
	a.Cmd = "sleep 60"
	start := time.Now()
	res, err := RunGate(a, 1500*time.Millisecond)
	must(t, err)
	if !res.TimedOut || time.Since(start) > 20*time.Second {
		t.Errorf("timed_out=%v after %v", res.TimedOut, time.Since(start))
	}
}

// TestContainedReal: run_contained's view in real namespaces: the home is an empty tmpfs, only the trial is writable.
func TestContainedReal(t *testing.T) {
	requireUserns(t)
	f := newFixture(t)
	must(t, os.WriteFile(f.home+"/secret", []byte("HOME-SECRET"), 0o600))
	o := f.opts(f.trials + "/tr0001")
	o.Self, o.Setpriv, o.Unshare = os.Args[0], "", "unshare"
	o.Mode, o.NoRunMask = "allow", true
	o.Rw = []string{f.trials + "/tr0001"}
	o.HideDirs = []string{f.home}
	o.Cwd = f.trials + "/tr0001"
	cfg, err := MakeCfg(o)
	must(t, err)
	res, err := RunContained(cfg, []string{"sh", "-c", "cat " + f.home + "/secret; echo rc=$?; touch " + f.trials + "/tr0001/ok && echo wrote; id -u"},
		f.trials+"/tr0001", EnvPassFrom(os.Environ(), f.trials+"/tr0001", nil), time.Minute)
	must(t, err)
	if strings.Contains(res.Output, "HOME-SECRET") || !strings.Contains(res.Output, "wrote") || !strings.Contains(res.Output, "rc=1") {
		t.Errorf("output:\n%s", res.Output)
	}
}

// TestSetupRefusals: stage 1 exits non-zero and never runs the command when the view cannot be built.
func TestSetupRefusals(t *testing.T) {
	requireUserns(t)
	f := newFixture(t)
	cases := []struct {
		name string
		mod  func(o *Opts)
		want string
	}{
		{"a missing rw source", func(o *Opts) { o.Rw = []string{f.root + "/does-not-exist"} }, "bind source missing"},
		{"a missing mask", func(o *Opts) { o.Masks = []string{f.root + "/does-not-exist"} }, "mask dir missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := f.opts(f.trials + "/tr0001")
			o.Self, o.Setpriv, o.Unshare = os.Args[0], "", "unshare"
			o.Mode, o.NoRunMask, o.HideDirs, o.Net = "allow", true, []string{f.home}, true
			c.mod(&o)
			cfg, err := MakeCfg(o)
			must(t, err)
			res, err := RunContained(cfg, []string{"sh", "-c", "echo RAN"}, "/", EnvPassFrom(os.Environ(), "/", nil), time.Minute)
			must(t, err)
			if res.ReturnCode == 0 || strings.Contains(res.Output, "RAN") || !strings.Contains(res.Output, c.want) {
				t.Errorf("rc=%d out=%s", res.ReturnCode, res.Output)
			}
		})
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
