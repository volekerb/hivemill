package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture is the on-disk layout the golden generator (see the PR body) builds for the Python modules: the same names,
// under a dir that is not below /tmp or /dev/shm (the worker profile hides those).
type fixture struct {
	root, home, run, fh, trials, claude, agent string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := "/var/tmp"
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		t.Skip("/var/tmp is not usable: the fixture must not sit below /tmp or /dev/shm")
	}
	root, err := os.MkdirTemp(base, "zz-test-sandbox-")
	if err != nil {
		t.Skipf("cannot create a fixture under /var/tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	root = Realpath(root)
	f := &fixture{root: root, home: root + "/hm", run: root + "/run", fh: root + "/fh", trials: root + "/trials", agent: root + "/agents/w1"}
	for _, d := range []string{f.run, f.fh + "/worktrees/wt1", f.fh + "/db", f.trials + "/tr0001/work", f.trials + "/tr0002", root + "/vendor",
		root + "/keep", root + "/main/db", root + "/main/logs", root + "/main/worktrees", root + "/gate", root + "/wt", f.agent,
		f.home + "/.local/go/bin", f.home + "/.cache/go/path/pkg/mod", f.home + "/.local/bin", f.home + "/.config/factory"} {
		must(t, os.MkdirAll(d, 0o755))
	}
	touch(t, f.home+"/.local/go/bin/go", 0o755)
	touch(t, f.home+"/.local/share/claude/versions/1/claude", 0o755)
	f.claude = f.home + "/.local/bin/claude"
	must(t, os.Symlink(f.home+"/.local/share/claude/versions/1/claude", f.claude))
	must(t, os.Symlink(f.fh+"/bin-factory", f.home+"/.local/bin/factory"))
	touch(t, f.home+"/.config/factory/scrub-terms.txt", 0o644)
	touch(t, root+"/gate/.git", 0o644)
	touch(t, root+"/resolv.conf", 0o644)
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte("x"), mode))
	must(t, os.Chmod(path, mode))
}

// seams are the injected host facts of the golden: uid, tool paths, the runtime dir and a resolv.conf outside /run.
func (f *fixture) seams() Opts {
	return Opts{UID: 1000, GID: 1000, Unshare: "/usr/bin/unshare", Setpriv: "/usr/bin/setpriv", Self: "@SELF@",
		Runtime: f.run, ResolvConf: f.root + "/resolv.conf"}
}

// opts is NewOpts with the seams applied.
func (f *fixture) opts(trial string) Opts {
	o := NewOpts(trial)
	s := f.seams()
	o.UID, o.GID, o.Unshare, o.Setpriv, o.Self, o.Runtime, o.ResolvConf = s.UID, s.GID, s.Unshare, s.Setpriv, s.Self, s.Runtime, s.ResolvConf
	o.Home = f.home
	return o
}

func (f *fixture) norm(v any) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, f.root, "@ROOT@")
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = f.norm(x[i])
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[strings.ReplaceAll(k, f.root, "@ROOT@")] = f.norm(e)
		}
		return out
	}
	return v
}

// generic round-trips through JSON so numbers and nulls compare like the golden's.
func generic(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	var out any
	must(t, json.Unmarshal(b, &out))
	return out
}

// argvOf is the stage-1 argv with the cfg JSON argument parsed, root-normalised.
func (f *fixture) argvOf(t *testing.T, cfg *Cfg, args []string) any {
	t.Helper()
	argv, err := ComposeStage1(cfg, args)
	must(t, err)
	return f.parsedArgv(t, argv)
}

func (f *fixture) parsedArgv(t *testing.T, argv []string) any {
	t.Helper()
	out := make([]any, len(argv))
	for i, a := range argv {
		out[i] = a
		if i > 0 && (argv[i-1] == "--setup" || argv[i-1] == "--launch") {
			var j any
			must(t, json.Unmarshal([]byte(a), &j))
			out[i] = j
		}
	}
	return f.norm(out)
}

type goldenCase struct {
	Argv any               `json:"argv"`
	Env  map[string]string `json:"env"`
	Text string            `json:"text"`
}

func loadGolden(t *testing.T) map[string]goldenCase {
	t.Helper()
	b, err := os.ReadFile("testdata/argv_golden.json")
	must(t, err)
	var g map[string]goldenCase
	must(t, json.Unmarshal(b, &g))
	return g
}

func (f *fixture) normEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		out[k] = strings.ReplaceAll(v, f.root, "@ROOT@")
	}
	return out
}

// TestSandboxArgvGolden: for nsbox, workerbox, gatebox, testbox and the eval profile the built argv, cfg and env equal
// what the Python modules built for the same fixture (testdata/argv_golden.json, generator in the PR body).
func TestSandboxArgvGolden(t *testing.T) {
	f := newFixture(t)
	golden := loadGolden(t)
	trial := f.trials + "/tr0001"
	genv := map[string]string{"PATH": "/usr/bin", "LANG": "C", "LC_ALL": "C", "GH_TOKEN": "t", "FACTORY_AGENT": "a", "FACTORY_HOME": "/live", "GATE_SHARDS": "2"}
	gate := func(subuid bool) GateArgs {
		return GateArgs{Cmd: "make test 'x y'", Tree: f.root + "/gate", FactoryMain: f.root + "/main", WtRoot: f.root + "/wt", Env: genv,
			Home: f.home, Runtime: f.run, Subuid: subuid, Unshare: "/usr/bin/unshare", Seams: f.seams(),
			Probe:     func(bool) (bool, string) { return true, "ok" },
			CommonDir: func(string) (string, error) { return f.root + "/outside.git", nil },
			SizeOf:    func(string) (int64, error) { return 1 << 20, nil }}
	}
	worker := func(rwCwd bool) (*Cfg, map[string]string) {
		go1 := GoPaths(f.home, f.agent, "", "")
		cfg, err := WorkerCfg(WorkerCfgArgs{AgentDir: f.agent, Cwd: f.fh + "/worktrees/wt1", FactoryHome: f.fh, DBDir: f.fh + "/db",
			RealClaude: f.claude, RwCwd: rwCwd, Home: f.home, Runtime: f.run, Go: go1, Seams: f.seams()})
		must(t, err)
		caller := map[string]string{"PATH": "/usr/bin:/bin", "LANG": "C", "LC_ALL": "C", "SSH_AUTH_SOCK": "/s", "FACTORY_AGENT": "a",
			"FACTORY_ROLE": "coder", "FACTORY_HOME": "/fh", "HERDR_PANE_ID": "p1", "OPENAI_API_KEY": "k", "TMPDIR": "/tmp", "HTTPS_PROXY": "x"}
		return cfg, BuildEnv(caller, f.home, f.agent+"/claude", f.agent+"/gitconfig", "OAUTH", "GHTOK", GoEnvFor(go1))
	}
	cases := map[string]func() (any, map[string]string){
		"nsbox_deny": func() (any, map[string]string) {
			o := f.opts(trial)
			o.RealClaude, o.TrialsRoot, o.VendorDir, o.Keep, o.Forward, o.Cwd = f.claude, f.trials, f.root+"/vendor", []string{f.root + "/keep"}, true, trial+"/work"
			cfg, err := MakeCfg(o)
			must(t, err)
			return f.argvOf(t, cfg, []string{"--print", "hi"}), nil
		},
		"nsbox_deny_net_noforward": func() (any, map[string]string) {
			o := f.opts(trial)
			o.Net, o.HideDirs = true, []string{"~/.ssh", "$XDG_RUNTIME_DIR"}
			cfg, err := MakeCfg(o)
			must(t, err)
			return f.argvOf(t, cfg, []string{"id"}), nil
		},
		"nsbox_allow": func() (any, map[string]string) {
			o := f.opts(trial)
			o.RealClaude, o.TrialsRoot, o.Mode, o.Forward, o.NoRunMask = f.claude, f.trials, "allow", true, true
			o.Rw, o.Ro, o.Masks, o.Cwd = []string{trial}, []string{f.root + "/vendor"}, []string{f.trials}, trial+"/work"
			o.HideDirs, o.HideFiles, o.TmpfsSize, o.TmpfsSizes = []string{f.home, f.run}, nil, "16m", map[string]string{f.home: "64m"}
			cfg, err := MakeCfg(o)
			must(t, err)
			return f.argvOf(t, cfg, []string{"--version"}), nil
		},
		"nsbox_allow_nopid_net": func() (any, map[string]string) {
			o := f.opts(trial)
			o.Mode, o.Net, o.NoPid, o.NoRunMask, o.KeepEnv = "allow", true, true, true, []string{"GH_TOKEN"}
			o.Rw, o.HideDirs = []string{f.root + "/wt"}, []string{f.home}
			cfg, err := MakeCfg(o)
			must(t, err)
			return f.argvOf(t, cfg, []string{"true"}), nil
		},
		"nsbox_allow_subuid_copy": func() (any, map[string]string) {
			o := f.opts(trial)
			o.Mode, o.Subuid, o.NoRunMask = "allow", true, true
			o.Copy = &CopySpec{Tree: f.root + "/gate", Size: "128m", Mkdirs: []string{f.root + "/gate/tmp"}}
			o.Ro, o.HideDirs = []string{f.root + "/gate"}, []string{f.home}
			cfg, err := MakeCfg(o)
			must(t, err)
			return f.argvOf(t, cfg, []string{"bash", "-c", "true"}), nil
		},
		"workerbox_rw_cwd": func() (any, map[string]string) {
			cfg, env := worker(true)
			return f.argvOf(t, cfg, []string{"--model", "m"}), f.normEnv(env)
		},
		"workerbox_ro_cwd": func() (any, map[string]string) {
			cfg, env := worker(false)
			return f.argvOf(t, cfg, []string{"--model", "m"}), f.normEnv(env)
		},
		"gatebox_plain": func() (any, map[string]string) {
			argv, env, err := BuildGate(gate(false))
			must(t, err)
			return f.parsedArgv(t, argv), f.normEnv(env)
		},
		"gatebox_subuid": func() (any, map[string]string) {
			argv, env, err := BuildGate(gate(true))
			must(t, err)
			return f.parsedArgv(t, argv), f.normEnv(env)
		},
		"testbox": func() (any, map[string]string) {
			argv, env, err := BuildTest([]string{"pytest", "-k", "a b"}, f.root+"/gate", f.root+"/main", f.root+"/wt", genv, gate(false))
			must(t, err)
			return f.parsedArgv(t, argv), f.normEnv(env)
		},
		"sandbox_eval": func() (any, map[string]string) {
			cfg, err := EvalNsCfg(trial, f.home, f.claude, f.root+"/vendor", "/usr/bin/unshare", f.seams())
			must(t, err)
			return f.argvOf(t, cfg, []string{"--version"}), nil
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			want, ok := golden[name]
			if !ok {
				t.Fatalf("no golden for %s: regenerate testdata/argv_golden.json", name)
			}
			argv, env := build()
			if !reflect.DeepEqual(generic(t, argv), generic(t, want.Argv)) {
				t.Errorf("argv differs from the Python golden\n got: %s\nwant: %s", mustJSON(argv), mustJSON(want.Argv))
			}
			if want.Env != nil && !reflect.DeepEqual(env, want.Env) {
				t.Errorf("env differs from the Python golden\n got: %v\nwant: %v", env, want.Env)
			}
		})
	}
	t.Run("workerbox_gitconfig", func(t *testing.T) {
		got, err := RenderGitconfig("!/bin/helper git-credential a/b", `N "x"`, "n-e.x")
		must(t, err)
		if got != golden["workerbox_gitconfig"].Text {
			t.Errorf("gitconfig differs\n got: %q\nwant: %q", got, golden["workerbox_gitconfig"].Text)
		}
	})
	t.Run("every golden case is exercised", func(t *testing.T) {
		for name := range golden {
			if _, ok := cases[name]; !ok && name != "workerbox_gitconfig" {
				t.Errorf("golden case %s has no Go case", name)
			}
		}
	})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
