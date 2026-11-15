package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAgentName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"claude-coder-t-1480", true}, {"a", true}, {"A1_b-c", true}, {strings.Repeat("a", 64), true}, {strings.Repeat("a", 65), false},
		{"", false}, {"-a", false}, {"_a", false}, {"a b", false}, {"a/b", false}, {"../x", false}, {"a.b", false}, {"a\n", false}, {"é", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := CheckAgentName(c.name); (err == nil) != c.ok {
				t.Errorf("CheckAgentName(%q) = %v, want ok=%v", c.name, err, c.ok)
			}
		})
	}
}

func TestRefuseCwd(t *testing.T) {
	f := newFixture(t)
	hide := HideTrees(f.home, f.run, nil)
	must(t, os.MkdirAll(f.root+"/research/task1", 0o755))
	cases := []struct {
		name, cwd, research, want string // want "" = accepted
	}{
		{"a worktree", f.fh + "/worktrees/wt1", "", ""},
		{"a worktree subdir", f.fh + "/worktrees/wt1/sub", "", ""},
		{"the home", f.home, "", "equal to or above"},
		{"above the home", f.root, "", "equal to or above"},
		{"the factory home", f.fh, "", "equal to or above"},
		{"factory home bin", f.fh + "/bin", "", "not under its worktrees/"},
		{"factory home db", f.fh + "/db", "", "not under its worktrees/"},
		{"factory home roles", f.fh + "/roles", "", "not under its worktrees/"},
		{"worktrees itself", f.fh + "/worktrees", "", "not under its worktrees/"},
		{"the agent dir", f.agent, "", "equal to or above"},
		{"inside the agent dir (neither hidden nor protected: the same as Python)", f.agent + "/claude", "", ""},
		{"inside the hidden home", f.home + "/project", "", "inside the hidden tree"},
		{"inside the runtime dir", f.run + "/x", "", "inside the hidden tree"},
		{"/tmp", "/tmp", "", "equal to or above"},
		{"under /tmp", "/tmp/x", "", "inside the hidden tree"},
		{"/dev/shm", "/dev/shm", "", "equal to or above"},
		{"researcher dir strictly below its root", f.root + "/research/task1", f.root + "/research", ""},
		{"the research root itself", f.root + "/research", f.root + "/research", ""},
		{"outside every guarded tree", f.root + "/keep", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := RefuseCwd(c.cwd, f.home, f.fh, f.agent, hide, c.research)
			if c.want == "" {
				if err != nil && c.name != "the research root itself" {
					t.Errorf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestGoPathsAndEnv(t *testing.T) {
	f := newFixture(t)
	t.Run("with a toolchain and a module cache", func(t *testing.T) {
		g := GoPaths(f.home, f.agent, "", "")
		if g == nil || g.Root != f.home+"/.local/go" || g.Modcache != f.home+"/.cache/go/path/pkg/mod" || g.Dir != f.agent+"/go" {
			t.Fatalf("go = %+v", g)
		}
		env := GoEnvFor(g)
		for k, v := range map[string]string{"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOFLAGS": "-mod=readonly", "GOENV": "off",
			"GOWORK": "off", "GOMODCACHE": g.Modcache, "GOCACHE": g.Dir + "/cache", "GOTMPDIR": g.Dir + "/tmp", "GOPATH": g.Dir + "/path", "GOROOT": g.Root} {
			if env[k] != v {
				t.Errorf("%s = %q, want %q", k, env[k], v)
			}
		}
	})
	t.Run("no toolchain means no Go", func(t *testing.T) {
		if g := GoPaths(f.home, f.agent, f.root+"/no-go", ""); g != nil {
			t.Errorf("go = %+v", g)
		}
	})
	t.Run("a go binary that is not executable is not a toolchain", func(t *testing.T) {
		d := t.TempDir()
		touch(t, d+"/bin/go", 0o644)
		if GoPaths(f.home, f.agent, d, "") != nil {
			t.Error("accepted a non-executable go")
		}
	})
	t.Run("the first existing module cache wins, an explicit one overrides", func(t *testing.T) {
		g := GoPaths(f.home, f.agent, "", f.root+"/keep")
		if g.Modcache != f.root+"/keep" {
			t.Errorf("modcache = %q", g.Modcache)
		}
		must(t, os.RemoveAll(f.home+"/.cache"))
		must(t, os.MkdirAll(f.home+"/.local/lib/factory/go/mod", 0o755))
		if g := GoPaths(f.home, f.agent, "", ""); g.Modcache != f.home+"/.local/lib/factory/go/mod" {
			t.Errorf("private cache not found: %+v", g)
		}
		must(t, os.RemoveAll(f.home+"/.local/lib"))
		if g := GoPaths(f.home, f.agent, "", ""); g.Modcache != "" || GoEnvFor(g)["GOMODCACHE"] != "" {
			t.Errorf("no cache should leave GOMODCACHE unset: %+v", g)
		}
	})
	t.Run("a symlinked toolchain is resolved", func(t *testing.T) {
		real := t.TempDir()
		touch(t, real+"/bin/go", 0o755)
		link := f.root + "/golink"
		must(t, os.Symlink(real, link))
		if g := GoPaths(f.home, f.agent, link, ""); g == nil || g.Root != Realpath(real) {
			t.Errorf("go = %+v", g)
		}
	})
}

func TestBuildEnv(t *testing.T) {
	caller := map[string]string{"PATH": "/p", "LANG": "C", "TERM": "x", "LC_ALL": "C", "LC_TIME": "t", "FACTORY_AGENT": "a", "FACTORY_ROLE": "coder",
		"FACTORY_HOME": "/fh", "HERDR_PANE_ID": "p1", "CLAUDE_CODE_ENTRYPOINT": "cli", "SSH_AUTH_SOCK": "/s", "DBUS_SESSION_BUS_ADDRESS": "d",
		"TMPDIR": "/tmp", "HTTPS_PROXY": "p", "ANTHROPIC_API_KEY": "k", "OPENAI_API_KEY": "k", "GH_TOKEN": "caller", "HOME": "/real", "AWS_SECRET": "s"}
	env := BuildEnv(caller, "/h", "/cfg", "/gc", "OAUTH", "GH", nil)
	for _, gone := range []string{"SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS", "TMPDIR", "HTTPS_PROXY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AWS_SECRET"} {
		t.Run("dropped "+gone, func(t *testing.T) {
			if _, has := env[gone]; has {
				t.Errorf("%s passed through", gone)
			}
		})
	}
	for k, v := range map[string]string{"HOME": "/h", "CLAUDE_CONFIG_DIR": "/cfg", "GIT_CONFIG_GLOBAL": "/gc", "DISABLE_AUTOUPDATER": "1",
		"CLAUDE_CODE_OAUTH_TOKEN": "OAUTH", "GH_TOKEN": "GH", "PATH": "/p", "LC_ALL": "C", "LC_TIME": "t", "FACTORY_ROLE": "coder", "HERDR_PANE_ID": "p1",
		"CLAUDE_CODE_ENTRYPOINT": "cli"} {
		t.Run("set "+k, func(t *testing.T) {
			if env[k] != v {
				t.Errorf("%s = %q, want %q", k, env[k], v)
			}
		})
	}
	t.Run("go env is set by HQ and its bin goes last on PATH", func(t *testing.T) {
		e := BuildEnv(caller, "/h", "/cfg", "/gc", "O", "G", map[string]string{"GOROOT": "/go", "GOPROXY": "off"})
		if e["PATH"] != "/p:/go/bin" || e["GOPROXY"] != "off" {
			t.Errorf("env = %v", e)
		}
	})
	t.Run("go env without a caller PATH", func(t *testing.T) {
		e := BuildEnv(map[string]string{}, "/h", "/cfg", "/gc", "O", "G", map[string]string{"GOROOT": "/go"})
		if e["PATH"] != "/go/bin:/usr/bin:/bin" {
			t.Errorf("PATH = %q", e["PATH"])
		}
	})
	t.Run("a caller GOPROXY cannot override HQ's", func(t *testing.T) {
		c := map[string]string{"GOPROXY": "https://evil", "GOFLAGS": "-x"}
		e := BuildEnv(c, "/h", "/cfg", "/gc", "O", "G", map[string]string{"GOROOT": "/go", "GOPROXY": "off"})
		if e["GOPROXY"] != "off" || e["GOFLAGS"] != "" {
			t.Errorf("env = %v", e)
		}
	})
	t.Run("without go env the caller's Go variables are dropped", func(t *testing.T) {
		e := BuildEnv(map[string]string{"GOPROXY": "x"}, "/h", "/cfg", "/gc", "O", "G", nil)
		if _, has := e["GOPROXY"]; has {
			t.Error("GOPROXY passed through")
		}
	})
}

func TestWorkerCfg(t *testing.T) {
	f := newFixture(t)
	args := func() WorkerCfgArgs {
		return WorkerCfgArgs{AgentDir: f.agent, Cwd: f.fh + "/worktrees/wt1", FactoryHome: f.fh, DBDir: f.fh + "/db", RealClaude: f.claude,
			RwCwd: true, Home: f.home, Runtime: f.run, Seams: f.seams()}
	}
	t.Run("network on, no pid namespace, nothing forwarded", func(t *testing.T) {
		c, err := WorkerCfg(args())
		must(t, err)
		if !c.Net || !c.NoPid || c.ForwardSock != nil || c.Mode != "allow" {
			t.Errorf("net=%v nopid=%v fwd=%v mode=%q", c.Net, c.NoPid, c.ForwardSock, c.Mode)
		}
	})
	t.Run("the oauth and gh tokens are exempt from the scrub, everything else net is scrubbed", func(t *testing.T) {
		c, _ := WorkerCfg(args())
		if contains(c.Scrub, "CLAUDE_CODE_OAUTH_TOKEN") || contains(c.Scrub, "GH_TOKEN") {
			t.Errorf("scrub = %v", c.Scrub)
		}
		for _, want := range []string{"SSH_AUTH_SOCK", "ANTHROPIC_API_KEY", "HTTPS_PROXY"} {
			if !contains(c.Scrub, want) {
				t.Errorf("%s not scrubbed: %v", want, c.Scrub)
			}
		}
	})
	t.Run("the factory CLI link and the scrub terms file are bound read-only", func(t *testing.T) {
		c, _ := WorkerCfg(args())
		if !contains(c.Ro, f.home+"/.local/bin/factory") || !contains(c.Ro, f.home+"/.config/factory/scrub-terms.txt") {
			t.Errorf("ro = %v", c.Ro)
		}
		for _, p := range c.Ro {
			if p == f.home+"/.config/factory" || p == f.home+"/.config" {
				t.Errorf("the token dir %s is bound: %v", p, c.Ro)
			}
		}
	})
	t.Run("a symlinked scrub terms file is refused", func(t *testing.T) {
		real := f.home + "/real-terms"
		touch(t, real, 0o644)
		must(t, os.Remove(f.home+"/.config/factory/scrub-terms.txt"))
		must(t, os.Symlink(real, f.home+"/.config/factory/scrub-terms.txt"))
		t.Cleanup(func() {
			os.Remove(f.home + "/.config/factory/scrub-terms.txt")
			touch(t, f.home+"/.config/factory/scrub-terms.txt", 0o644)
		})
		if _, err := WorkerCfg(args()); err == nil || !strings.Contains(err.Error(), "is a symlink") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a refused cwd fails before any cfg", func(t *testing.T) {
		a := args()
		a.Cwd = f.home
		if _, err := WorkerCfg(a); err == nil {
			t.Error("home accepted as cwd")
		}
	})
	t.Run("the claude install dir is bound read-only when it lives under the home share", func(t *testing.T) {
		c, _ := WorkerCfg(args())
		if !contains(c.Ro, f.home+"/.local/share/claude/versions/1") {
			t.Errorf("ro = %v", c.Ro)
		}
	})
	t.Run("a worker without a worktrees dir has no mask", func(t *testing.T) {
		must(t, os.Remove(f.fh+"/worktrees/wt1"))
		a := args()
		a.Cwd = f.root + "/keep"
		c, err := WorkerCfg(a)
		must(t, err)
		if len(c.Masks) != 1 {
			t.Errorf("masks = %v", c.Masks)
		}
		must(t, os.RemoveAll(f.fh+"/worktrees"))
		if c, _ = WorkerCfg(a); len(c.Masks) != 0 {
			t.Errorf("masks = %v", c.Masks)
		}
	})
}

func TestSeedClaudeJSON(t *testing.T) {
	read := func(dir string) map[string]any {
		b, err := os.ReadFile(dir + "/.claude.json")
		must(t, err)
		var m map[string]any
		must(t, json.Unmarshal(b, &m))
		return m
	}
	proj := func(m map[string]any, k string) map[string]any {
		return m["projects"].(map[string]any)[k].(map[string]any)
	}
	t.Run("a fresh seed trusts the cwd and skips onboarding", func(t *testing.T) {
		d := t.TempDir()
		must(t, SeedClaudeJSON(d, "/w"))
		m := read(d)
		if m["hasCompletedOnboarding"] != true || m["theme"] != "dark" || proj(m, "/w")["hasTrustDialogAccepted"] != true || proj(m, "/w")["hasCompletedProjectOnboarding"] != true {
			t.Errorf("seed = %v", m)
		}
		if fi, _ := os.Stat(d + "/.claude.json"); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	})
	t.Run("a second call for the same cwd is a no-op", func(t *testing.T) {
		d := t.TempDir()
		must(t, SeedClaudeJSON(d, "/w"))
		fi1, _ := os.Stat(d + "/.claude.json")
		must(t, SeedClaudeJSON(d, "/w"))
		fi2, _ := os.Stat(d + "/.claude.json")
		if !os.SameFile(fi1, fi2) {
			t.Error("the file was rewritten")
		}
	})
	t.Run("another cwd is added and the first kept", func(t *testing.T) {
		d := t.TempDir()
		must(t, SeedClaudeJSON(d, "/w"))
		must(t, SeedClaudeJSON(d, "/x"))
		m := read(d)
		if proj(m, "/w") == nil || proj(m, "/x")["hasTrustDialogAccepted"] != true {
			t.Errorf("projects = %v", m["projects"])
		}
	})
	t.Run("an entry without trust keeps its keys and gains the flags (T-1291)", func(t *testing.T) {
		d := t.TempDir()
		must(t, os.WriteFile(d+"/.claude.json", []byte(`{"projects":{"/w":{"allowedTools":["x"]},"/o":{"k":1}},"keep":true}`), 0o600))
		must(t, SeedClaudeJSON(d, "/w"))
		m := read(d)
		if proj(m, "/w")["allowedTools"] == nil || proj(m, "/w")["hasTrustDialogAccepted"] != true || proj(m, "/o")["k"] != float64(1) || m["keep"] != true {
			t.Errorf("m = %v", m)
		}
	})
	junk := map[string]string{"junk": "not json", "array": "[]", "projects not an object": `{"projects": 5}`, "empty": ""}
	for name, body := range junk {
		t.Run("a "+name+" file is replaced by a fresh seed", func(t *testing.T) {
			d := t.TempDir()
			must(t, os.WriteFile(d+"/.claude.json", []byte(body), 0o600))
			must(t, SeedClaudeJSON(d, "/w"))
			if proj(read(d), "/w")["hasTrustDialogAccepted"] != true {
				t.Error("not reseeded")
			}
		})
	}
	t.Run("a symlink is replaced, never followed", func(t *testing.T) {
		d, out := t.TempDir(), t.TempDir()
		must(t, os.WriteFile(out+"/victim", []byte("PRECIOUS"), 0o600))
		must(t, os.Symlink(out+"/victim", d+"/.claude.json"))
		must(t, SeedClaudeJSON(d, "/w"))
		if b, _ := os.ReadFile(out + "/victim"); string(b) != "PRECIOUS" {
			t.Errorf("the link target was written: %q", b)
		}
		if fi, _ := os.Lstat(d + "/.claude.json"); fi.Mode()&os.ModeSymlink != 0 {
			t.Error("the symlink is still there")
		}
	})
	t.Run("a directory in the way is refused", func(t *testing.T) {
		d := t.TempDir()
		must(t, os.Mkdir(d+"/.claude.json", 0o700))
		if err := SeedClaudeJSON(d, "/w"); err == nil || !strings.Contains(err.Error(), "remove whatever is there") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("an oversized file is refused and left alone", func(t *testing.T) {
		d := t.TempDir()
		big := strings.Repeat("x", ClaudeJSONMax+10)
		must(t, os.WriteFile(d+"/.claude.json", []byte(big), 0o600))
		if err := SeedClaudeJSON(d, "/w"); err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Fatalf("err = %v", err)
		}
		if fi, _ := os.Stat(d + "/.claude.json"); fi.Size() != int64(len(big)) {
			t.Error("the oversized file was modified")
		}
	})
}

func TestClonePathRefusal(t *testing.T) {
	d := Realpath(t.TempDir())
	must(t, os.MkdirAll(d+"/worktrees/c1", 0o755))
	must(t, os.Symlink(d+"/worktrees/c1", d+"/worktrees/link"))
	must(t, os.MkdirAll(d+"/elsewhere", 0o755))
	must(t, os.Symlink(d+"/worktrees", d+"/wlink"))
	cases := []struct{ name, clone, wt, want string }{
		{"a clone under worktrees", d + "/worktrees/c1", d + "/worktrees", ""},
		{"a not yet existing clone", d + "/worktrees/new", d + "/worktrees", ""},
		{"worktrees itself", d + "/worktrees", d + "/worktrees", "not under worktrees/"},
		{"a sibling", d + "/elsewhere", d + "/worktrees", "not under worktrees/"},
		{"a prefix sibling", d + "/worktrees-x/c", d + "/worktrees", "not under worktrees/"},
		{"a symlink component", d + "/worktrees/link", d + "/worktrees", "symlink component"},
		{"worktrees behind a symlink", d + "/wlink/c1", d + "/wlink", "symlink"},
		{"dot dot out", d + "/worktrees/../elsewhere", d + "/worktrees", "not under worktrees/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClonePathRefusal(c.clone, c.wt)
			if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Errorf("refusal = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSetCloneTrust(t *testing.T) {
	mk := func(t *testing.T, body string) (home, wt string) {
		home = Realpath(t.TempDir())
		wt = home + "/worktrees"
		must(t, os.MkdirAll(wt+"/c1", 0o755))
		if body != "" {
			must(t, os.WriteFile(home+"/.claude.json", []byte(body), 0o600))
		}
		return
	}
	projects := func(home string) map[string]any {
		b, _ := os.ReadFile(home + "/.claude.json")
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		p, _ := m["projects"].(map[string]any)
		return p
	}
	t.Run("add registers one entry and touches nothing else", func(t *testing.T) {
		home, wt := mk(t, `{"projects":{"/other":{"a":1}},"userID":"u"}`)
		res, why := SetCloneTrust(home, wt, wt+"/c1", true)
		if res != "added" || why != "" {
			t.Fatalf("res=%q why=%q", res, why)
		}
		p := projects(home)
		if p[wt+"/c1"].(map[string]any)["hasTrustDialogAccepted"] != true || p["/other"].(map[string]any)["a"] != float64(1) {
			t.Errorf("projects = %v", p)
		}
		b, _ := os.ReadFile(home + "/.claude.json")
		if !strings.Contains(string(b), `"userID"`) {
			t.Error("another key was dropped")
		}
	})
	t.Run("adding twice is unchanged", func(t *testing.T) {
		home, wt := mk(t, `{"projects":{}}`)
		SetCloneTrust(home, wt, wt+"/c1", true)
		if res, _ := SetCloneTrust(home, wt, wt+"/c1", true); res != "unchanged" {
			t.Errorf("res = %q", res)
		}
	})
	t.Run("remove drops only that entry", func(t *testing.T) {
		home, wt := mk(t, `{"projects":{"/other":{}}}`)
		SetCloneTrust(home, wt, wt+"/c1", true)
		if res, _ := SetCloneTrust(home, wt, wt+"/c1", false); res != "removed" {
			t.Errorf("res = %q", res)
		}
		if p := projects(home); len(p) != 1 || p["/other"] == nil {
			t.Errorf("projects = %v", p)
		}
	})
	t.Run("removing an absent entry is unchanged", func(t *testing.T) {
		home, wt := mk(t, `{"projects":{}}`)
		if res, _ := SetCloneTrust(home, wt, wt+"/c1", false); res != "unchanged" {
			t.Errorf("res = %q", res)
		}
	})
	refusals := []struct {
		name, body string
		mod        func(home, wt string) string
		want       string
	}{
		{"a clone outside worktrees", `{"projects":{}}`, func(h, w string) string { return h }, "not under worktrees/"},
		{"worktrees itself", `{"projects":{}}`, func(h, w string) string { return w }, "not under worktrees/"},
		{"a missing owner file", "", func(h, w string) string { return w + "/c1" }, "is missing"},
		{"invalid JSON", "nope", func(h, w string) string { return w + "/c1" }, "not valid JSON"},
		{"an array", "[]", func(h, w string) string { return w + "/c1" }, "not a JSON object"},
		{"projects of the wrong type", `{"projects":[]}`, func(h, w string) string { return w + "/c1" }, "not a JSON object"},
	}
	for _, c := range refusals {
		t.Run("refused: "+c.name, func(t *testing.T) {
			home, wt := mk(t, c.body)
			res, why := SetCloneTrust(home, wt, c.mod(home, wt), true)
			if res != "refused" || !strings.Contains(why, c.want) {
				t.Errorf("res=%q why=%q, want refused containing %q", res, why, c.want)
			}
		})
	}
	t.Run("a symlinked owner file is refused", func(t *testing.T) {
		home, wt := mk(t, "")
		real := t.TempDir() + "/real.json"
		must(t, os.WriteFile(real, []byte(`{"projects":{}}`), 0o600))
		must(t, os.Symlink(real, home+"/.claude.json"))
		res, why := SetCloneTrust(home, wt, wt+"/c1", true)
		if res != "refused" || !strings.Contains(why, "symlink") {
			t.Errorf("res=%q why=%q", res, why)
		}
	})
}

func TestRenderGitconfig(t *testing.T) {
	cases := []struct {
		name, helper, user, email string
		want                      []string
		err                       bool
	}{
		{"identity and helper", "!/x git-credential a/b", "N", "n@e", []string{"[user]", `name = "N"`, `email = "n@e"`, "useHttpPath = true", `helper = "!/x git-credential a/b"`}, false},
		{"no identity has no user section", "!/x", "", "", []string{"[credential]"}, false},
		{"quotes and backslashes are escaped", "!/x", `a"b\c`, "", []string{`name = "a\"b\\c"`}, false},
		{"newline in name is refused", "!/x", "a\nb", "", nil, true},
		{"CR in email is refused", "!/x", "", "a\rb", nil, true},
		{"NUL in helper is refused", "!/x\x00", "", "", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := RenderGitconfig(c.helper, c.user, c.email)
			if c.err {
				if err == nil {
					t.Errorf("accepted: %q", got)
				}
				return
			}
			must(t, err)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in %q", w, got)
				}
			}
			if c.user == "" && c.email == "" && strings.Contains(got, "[user]") {
				t.Errorf("user section without identity: %q", got)
			}
		})
	}
	t.Run("WorkerGhHelper refuses bad slugs", func(t *testing.T) {
		for _, s := range [][]string{nil, {}, {"bad"}, {"a/b", "x y/z"}} {
			if _, err := WorkerGhHelper("/x", s); err == nil {
				t.Errorf("accepted %v", s)
			}
		}
		h, err := WorkerGhHelper("/opt/f", []string{"a/b", "c/d"})
		if err != nil || h != "!/opt/f git-credential a/b,c/d" {
			t.Errorf("helper = %q, %v", h, err)
		}
	})
}

func TestPrepareAgent(t *testing.T) {
	f := newFixture(t)
	args := func() PrepareAgentArgs {
		return PrepareAgentArgs{Agent: "claude-coder-t-1", Role: "coder", Cwd: f.fh + "/worktrees/wt1", FactoryHome: f.fh, DBDir: f.fh + "/db",
			RealClaude: f.claude, RwCwd: true, Home: f.home, Runtime: f.run, WorkersRoot: f.root + "/workers", Identity: &[2]string{"N", "n@e"},
			Self: "/opt/factory", GhSlugs: []string{"acme/app"}, Seams: f.seams()}
	}
	t.Run("writes the agent dir, a 0755 shim, the gitconfig and the seed", func(t *testing.T) {
		dir, err := PrepareAgent(args())
		must(t, err)
		if dir != f.root+"/workers/claude-coder-t-1" {
			t.Errorf("dir = %s", dir)
		}
		shim, err := os.ReadFile(dir + "/bin/claude")
		must(t, err)
		if fi, _ := os.Stat(dir + "/bin/claude"); fi.Mode().Perm() != 0o755 || !strings.HasPrefix(string(shim), "#!/bin/sh\nexec /opt/factory worker-shim ") {
			t.Errorf("shim mode=%v text=%.80q", fi.Mode().Perm(), shim)
		}
		var sc ShimCfg
		js := strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(string(shim), "worker-shim ", 2)[1], "'"), "' \"$@\"\n")
		must(t, json.Unmarshal([]byte(js), &sc))
		if sc.Role != "coder" || sc.GhToken != f.home+"/.config/factory/gh-worker-rw-token" || sc.OAuthToken != f.home+"/.config/factory/worker-oauth-token" {
			t.Errorf("shim cfg = %+v", sc)
		}
		if sc.GoEnv["GOPROXY"] != "off" {
			t.Errorf("go env = %v", sc.GoEnv)
		}
		gc, _ := os.ReadFile(dir + "/gitconfig")
		if !strings.Contains(string(gc), "/opt/factory git-credential acme/app") {
			t.Errorf("gitconfig = %s", gc)
		}
		if _, err := os.Stat(dir + "/claude/.claude.json"); err != nil {
			t.Errorf("no seed: %v", err)
		}
	})
	t.Run("the reviewer gets the read-only token", func(t *testing.T) {
		a := args()
		a.Agent, a.Role = "rev1", "reviewer"
		dir, err := PrepareAgent(a)
		must(t, err)
		shim, _ := os.ReadFile(dir + "/bin/claude")
		if !strings.Contains(string(shim), "gh-worker-ro-token") || strings.Contains(string(shim), "gh-worker-rw-token") {
			t.Errorf("shim = %s", shim)
		}
	})
	t.Run("an explicit gh token path wins", func(t *testing.T) {
		a := args()
		a.Agent, a.GhTokenPath = "custom", f.root+"/custom-token"
		dir, _ := PrepareAgent(a)
		shim, _ := os.ReadFile(dir + "/bin/claude")
		if !strings.Contains(string(shim), f.root+"/custom-token") {
			t.Errorf("shim = %s", shim)
		}
	})
	refusals := []struct {
		name string
		mod  func(a *PrepareAgentArgs)
		want string
	}{
		{"a non-sandboxable role", func(a *PrepareAgentArgs) { a.Role = "judge" }, "not sandboxable"},
		{"pm is not sandboxable", func(a *PrepareAgentArgs) { a.Role = "pm" }, "not sandboxable"},
		{"a bad agent name", func(a *PrepareAgentArgs) { a.Agent = "../x" }, "not a slug"},
		{"a claude path not named claude", func(a *PrepareAgentArgs) { a.RealClaude = f.root + "/keep/bash" }, "basename is claude"},
		{"a cwd at the home", func(a *PrepareAgentArgs) { a.Cwd = f.home }, "equal to or above"},
		{"a cwd in factory home outside worktrees", func(a *PrepareAgentArgs) { a.Cwd = f.fh + "/bin" }, "not under its worktrees/"},
		{"no gh slugs", func(a *PrepareAgentArgs) { a.GhSlugs = nil }, "OWNER/NAME"},
		{"a newline in the identity", func(a *PrepareAgentArgs) { a.Identity = &[2]string{"a\nb", ""} }, "newline"},
	}
	for _, c := range refusals {
		t.Run("refused: "+c.name, func(t *testing.T) {
			a := args()
			a.Agent = "ref-" + strings.ReplaceAll(strings.ToLower(c.name[:3]), " ", "")
			c.mod(&a)
			_, err := PrepareAgent(a)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
	t.Run("a bad cwd creates nothing", func(t *testing.T) {
		a := args()
		a.Agent, a.Cwd, a.WorkersRoot = "nothing", f.home, f.root+"/fresh-workers"
		if _, err := PrepareAgent(a); err == nil {
			t.Fatal("accepted")
		}
		if _, err := os.Stat(f.root + "/fresh-workers"); err == nil {
			t.Error("the workers root was created before the cwd was checked")
		}
	})
	t.Run("a symlinked agent dir is refused", func(t *testing.T) {
		a := args()
		a.Agent = "linked"
		must(t, os.MkdirAll(f.root+"/workers", 0o700))
		must(t, os.Symlink(t.TempDir(), f.root+"/workers/linked"))
		if _, err := PrepareAgent(a); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("CheckCwd has no side effects and mirrors the rule", func(t *testing.T) {
		if err := CheckCwd("a1", f.home, f.fh, f.home, "", f.root+"/never", f.run, nil, ""); err == nil {
			t.Error("home accepted")
		}
		if err := CheckCwd("a1", f.fh+"/worktrees/wt1", f.fh, f.home, "", f.root+"/never", f.run, nil, ""); err != nil {
			t.Errorf("worktree refused: %v", err)
		}
		if _, err := os.Stat(f.root + "/never"); err == nil {
			t.Error("CheckCwd created the workers root")
		}
		if err := CheckCwd("bad name", f.fh+"/worktrees/wt1", f.fh, f.home, "", "", f.run, nil, ""); err == nil {
			t.Error("bad name accepted")
		}
	})
	t.Run("the shim path is under the agent dir and nothing is world writable", func(t *testing.T) {
		a := args()
		a.Agent = "perm"
		dir, err := PrepareAgent(a)
		must(t, err)
		_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
			if err == nil && fi.Mode().Perm()&0o002 != 0 {
				t.Errorf("%s is world writable", p)
			}
			return nil
		})
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Errorf("agent dir mode = %v", fi.Mode().Perm())
		}
	})
	t.Run("shim cfg round trips", func(t *testing.T) {
		c, err := WorkerCfg(WorkerCfgArgs{AgentDir: f.agent, Cwd: f.fh + "/worktrees/wt1", FactoryHome: f.fh, DBDir: f.fh + "/db", RealClaude: f.claude,
			Home: f.home, Runtime: f.run, Seams: f.seams()})
		must(t, err)
		sc := &ShimCfg{NS: c, Home: f.home, Role: "qa"}
		b, _ := json.Marshal(sc)
		var back ShimCfg
		must(t, json.Unmarshal(b, &back))
		if a, b := mustJSON(back.NS), mustJSON(c); a != b {
			t.Error("cfg changed in the shim round trip")
		}
	})
}
