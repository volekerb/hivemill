package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestSizeOK(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"8m", true}, {"512k", true}, {"1g", true}, {"16g", true}, {"16384m", true}, {"1", true}, {"100%", true}, {"1%", true},
		{"16G", true}, {"8M", true}, {"1K", true},
		{"0", false}, {"0m", false}, {"0g", false}, {"0%", false}, {"101%", false}, {"17g", false}, {"16385m", false},
		{"17179869184g", false}, {"18446744073709551616", false}, {"18446744073709551616k", false}, {"-1m", false}, {"1t", false},
		{"1p", false}, {"1e", false}, {"", false}, {"m", false}, {"8 m", false}, {" 8m", false}, {"8m ", false}, {"8mb", false},
		{"1.5g", false}, {"0x10", false}, {"1_0m", false}, {"٣m", false}, {"16777216k", true}, {"16777217k", false},
		{"17179869184", true}, {"17179869185", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := SizeOK(c.in); got != c.ok {
				t.Errorf("SizeOK(%q) = %v, want %v", c.in, got, c.ok)
			}
		})
	}
}

func TestRunKeepRO(t *testing.T) {
	cases := []struct {
		resolv string
		want   []string
	}{
		{"/run/systemd/resolve/stub-resolv.conf", []string{"/run/systemd/resolve"}},
		{"/run/resolvconf/resolv.conf", []string{"/run/resolvconf"}},
		{"/run/NetworkManager/no-stub-resolv.conf", []string{"/run/NetworkManager"}},
		{"/run/a/b/c/resolv.conf", []string{"/run/a/b/c"}},
		{"/run/resolv.conf", nil},           // parent is /run itself: never re-exposed
		{"/run/user/1000/resolv.conf", nil}, // the runtime dir: never
		{"/run/user/resolv.conf", nil},      // /run/user itself
		{"/run/user/1000/x/resolv.conf", nil},
		{"/var/run-like/resolv.conf", nil}, // not under /run
		{"/runner/x/resolv.conf", nil},     // prefix trap: /runner is not /run
	}
	for _, c := range cases {
		t.Run(c.resolv, func(t *testing.T) {
			if got := RunKeepRO(c.resolv); !reflect.DeepEqual(got, c.want) {
				t.Errorf("RunKeepRO(%q) = %v, want %v", c.resolv, got, c.want)
			}
		})
	}
}

func TestExpandEntry(t *testing.T) {
	cases := []struct{ in, want string }{
		{"~/.ssh", "/h/.ssh"}, {"~", "/h"}, {"$XDG_RUNTIME_DIR", "/r"}, {"$XDG_RUNTIME_DIR/bus", "/r/bus"},
		{"/abs/~/x", "/abs/~/x"}, {"rel/~", "rel/~"}, {"~/a~b", "/h/a~b"}, {"", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := ExpandEntry(c.in, "/h", "/r"); got != c.want {
				t.Errorf("ExpandEntry(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestMakeCfgRefusals(t *testing.T) {
	f := newFixture(t)
	link := f.root + "/linkdir"
	must(t, os.Symlink(f.root+"/keep", link))
	cases := []struct {
		name string
		mod  func(o *Opts)
		want string
	}{
		{"bad size", func(o *Opts) { o.Mode = "allow"; o.TmpfsSize = "0" }, "tmpfs size must be a positive number"},
		{"unlimited size via overflow", func(o *Opts) { o.Mode = "allow"; o.TmpfsSize = "17179869184g" }, "tmpfs size must be a positive number"},
		{"bad per-tree size", func(o *Opts) { o.Mode = "allow"; o.TmpfsSizes = map[string]string{"/x": "1t"} }, "tmpfs size must be a positive number"},
		{"unknown mode", func(o *Opts) { o.Mode = "bogus" }, "mode must be 'deny' or 'allow'"},
		{"pid off with forward", func(o *Opts) { o.NoPid, o.Forward = true, true }, "pid=False cannot be combined with forward=True"},
		{"vendor in allow", func(o *Opts) { o.Mode = "allow"; o.VendorDir = "/v" }, "deny-mode args"},
		{"keep in allow", func(o *Opts) { o.Mode = "allow"; o.Keep = []string{"/k"} }, "deny-mode args"},
		{"rw in deny", func(o *Opts) { o.Rw = []string{"/x"} }, "need mode='allow'"},
		{"ro in deny", func(o *Opts) { o.Ro = []string{"/x"} }, "need mode='allow'"},
		{"masks in deny", func(o *Opts) { o.Masks = []string{"/x"} }, "need mode='allow'"},
		{"tmpfs size in deny", func(o *Opts) { o.TmpfsSize = "16m" }, "tmpfs_size and tmpfs_sizes need mode='allow'"},
		{"tmpfs sizes in deny", func(o *Opts) { o.TmpfsSizes = map[string]string{"/x": "1m"} }, "tmpfs_size and tmpfs_sizes need mode='allow'"},
		{"subuid in deny", func(o *Opts) { o.Subuid = true }, "subuid needs mode='allow'"},
		{"copy without subuid", func(o *Opts) { o.Mode = "allow"; o.Copy = &CopySpec{Tree: "/t", Size: "1m"} }, "copy needs subuid=True"},
		{"bad copy size", func(o *Opts) { o.Mode = "allow"; o.Subuid = true; o.Copy = &CopySpec{Tree: "/t", Size: "0"} }, "copy size must be like 512m"},
		{"rw symlink leaf", func(o *Opts) { o.Mode = "allow"; o.Rw = []string{link} }, "rw/ro path is a symlink"},
		{"ro symlink leaf", func(o *Opts) { o.Mode = "allow"; o.Ro = []string{link} }, "rw/ro path is a symlink"},
		{"rw symlink parent", func(o *Opts) { o.Mode = "allow"; o.Rw = []string{link + "/sub"} }, "has a symlink parent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := f.opts(f.trials + "/tr0001")
			c.mod(&o)
			cfg, err := MakeCfg(o)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("MakeCfg error = %v (cfg %v), want one containing %q", err, cfg, c.want)
			}
		})
	}
}

func TestMakeCfgShape(t *testing.T) {
	f := newFixture(t)
	trial := f.trials + "/tr0001"
	t.Run("deny mode scrubs the net env unless forwarding", func(t *testing.T) {
		cfg, err := MakeCfg(f.opts(trial))
		must(t, err)
		if !contains(cfg.Scrub, "ANTHROPIC_API_KEY") || !contains(cfg.Scrub, "SSH_AUTH_SOCK") || !contains(cfg.Scrub, "https_proxy") {
			t.Errorf("scrub = %v", cfg.Scrub)
		}
		o := f.opts(trial)
		o.Forward = true
		fw, err := MakeCfg(o)
		must(t, err)
		if contains(fw.Scrub, "ANTHROPIC_API_KEY") || !contains(fw.Scrub, "SSH_AUTH_SOCK") {
			t.Errorf("forward scrub = %v", fw.Scrub)
		}
		if fw.EnvSet["HTTPS_PROXY"] != ProxyURL || fw.ForwardSock == nil || *fw.ForwardSock != trial+"/proxy.sock" {
			t.Errorf("forward env_set=%v sock=%v", fw.EnvSet, fw.ForwardSock)
		}
	})
	t.Run("keep_env exempts a variable from the scrub", func(t *testing.T) {
		o := f.opts(trial)
		o.KeepEnv = []string{"ANTHROPIC_API_KEY", "SSH_AUTH_SOCK"}
		cfg, err := MakeCfg(o)
		must(t, err)
		if contains(cfg.Scrub, "ANTHROPIC_API_KEY") || contains(cfg.Scrub, "SSH_AUTH_SOCK") {
			t.Errorf("scrub = %v", cfg.Scrub)
		}
	})
	t.Run("deny hide dirs resolve ~ and the runtime dir from the passwd home", func(t *testing.T) {
		cfg, err := MakeCfg(f.opts(trial))
		must(t, err)
		want := []string{f.home + "/.claude", f.home + "/.config", f.home + "/.ssh", f.home + "/.gnupg", f.home + "/factory", f.run}
		if !reflect.DeepEqual(cfg.HideDirs, want) {
			t.Errorf("hide_dirs = %v, want %v", cfg.HideDirs, want)
		}
		if !reflect.DeepEqual(cfg.HideFiles, []string{f.home + "/.git-credentials", f.home + "/.netrc"}) {
			t.Errorf("hide_files = %v", cfg.HideFiles)
		}
	})
	t.Run("deny mode keeps the trial and the claude link and its target", func(t *testing.T) {
		o := f.opts(trial)
		o.RealClaude = f.claude
		cfg, err := MakeCfg(o)
		must(t, err)
		want := []string{trial, f.claude, f.home + "/.local/share/claude/versions/1/claude"}
		if !reflect.DeepEqual(cfg.Keep, want) {
			t.Errorf("keep = %v, want %v", cfg.Keep, want)
		}
	})
	t.Run("allow mode binds nothing implicitly and masks /run", func(t *testing.T) {
		o := f.opts(trial)
		o.Mode = "allow"
		cfg, err := MakeCfg(o)
		must(t, err)
		if len(cfg.Rw) != 0 || len(cfg.Keep) != 0 || !contains(cfg.HideDirs, RunMask) {
			t.Errorf("rw=%v keep=%v hide=%v", cfg.Rw, cfg.Keep, cfg.HideDirs)
		}
	})
	t.Run("allow mode no_run_mask leaves /run alone", func(t *testing.T) {
		o := f.opts(trial)
		o.Mode, o.NoRunMask = "allow", true
		cfg, err := MakeCfg(o)
		must(t, err)
		if contains(cfg.HideDirs, RunMask) {
			t.Errorf("hide = %v", cfg.HideDirs)
		}
	})
	t.Run("allow mode re-exposes only a resolv.conf dir under /run read-only", func(t *testing.T) {
		d := t.TempDir()
		// a resolv.conf that really resolves into /run is machine specific; use the pure function plus a dir check
		o := f.opts(trial)
		o.Mode, o.ResolvConf = "allow", d+"/resolv.conf"
		cfg, err := MakeCfg(o)
		must(t, err)
		for _, r := range cfg.Ro {
			if strings.HasPrefix(r, "/run") {
				t.Errorf("ro = %v: a non-/run resolv.conf must add nothing", cfg.Ro)
			}
		}
	})
	t.Run("defaults for the trials root", func(t *testing.T) {
		cfg, err := MakeCfg(f.opts(trial))
		must(t, err)
		if cfg.TrialsRoot != f.trials {
			t.Errorf("trials_root = %q", cfg.TrialsRoot)
		}
	})
	t.Run("cwd is made absolute", func(t *testing.T) {
		o := f.opts(trial)
		o.Cwd = "rel/dir"
		cfg, err := MakeCfg(o)
		must(t, err)
		if !filepath.IsAbs(*cfg.Cwd) {
			t.Errorf("cwd = %q", *cfg.Cwd)
		}
	})
}

func TestComposeStage(t *testing.T) {
	f := newFixture(t)
	trial := f.trials + "/tr0001"
	claude := f.claude
	mk := func(mod func(o *Opts)) *Cfg {
		o := f.opts(trial)
		mod(&o)
		c, err := MakeCfg(o)
		must(t, err)
		return c
	}
	t.Run("stage 2 drops caps after the uid map", func(t *testing.T) {
		c := mk(func(o *Opts) { o.RealClaude = claude })
		got := ComposeStage2(c, []string{"-p", "x"})
		want := append(append([]string{"/usr/bin/unshare", "--map-user=1000", "--map-group=1000", "--keep-caps", "/usr/bin/setpriv"}, DropCaps...), claude, "-p", "x")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v\nwant %v", got, want)
		}
	})
	t.Run("stage 2 without claude runs the caller argv", func(t *testing.T) {
		got := ComposeStage2(mk(func(o *Opts) {}), []string{"id"})
		if got[len(got)-1] != "id" || contains(got, claude) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("stage 2 subuid uses setpriv ids and clears groups", func(t *testing.T) {
		c := mk(func(o *Opts) { o.Mode, o.Subuid = "allow", true })
		got := ComposeStage2(c, []string{"id"})
		if !reflect.DeepEqual(got[:4], []string{"/usr/bin/setpriv", "--reuid=1", "--regid=1", "--clear-groups"}) || !contains(got, "--no-new-privs") {
			t.Errorf("got %v", got)
		}
	})
	t.Run("every cap-drop flag is in stage 2", func(t *testing.T) {
		got := ComposeStage2(mk(func(o *Opts) {}), []string{"id"})
		for _, flag := range []string{"--no-new-privs", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all"} {
			if !contains(got, flag) {
				t.Errorf("missing %s in %v", flag, got)
			}
		}
	})
	t.Run("stage 1 net namespace unless net", func(t *testing.T) {
		a, _ := ComposeStage1(mk(func(o *Opts) {}), []string{"id"})
		b, _ := ComposeStage1(mk(func(o *Opts) { o.Net = true }), []string{"id"})
		if !contains(a[:6], "-n") || contains(b[:6], "-n") {
			t.Errorf("net: %v / %v", a[:6], b[:6])
		}
	})
	t.Run("stage 1 pid namespace unless pid off", func(t *testing.T) {
		a, _ := ComposeStage1(mk(func(o *Opts) {}), []string{"id"})
		b, _ := ComposeStage1(mk(func(o *Opts) { o.NoPid = true }), []string{"id"})
		if !contains(a, "--kill-child") || contains(b, "--kill-child") || contains(b, "--pid") {
			t.Errorf("pid: %v / %v", a, b)
		}
	})
	t.Run("stage 1 private propagation and the caller args after --", func(t *testing.T) {
		a, _ := ComposeStage1(mk(func(o *Opts) {}), []string{"a", "--b", "c d"})
		i := len(a) - 4
		if a[i] != "--" || a[i+1] != "a" || a[i+3] != "c d" || !contains(a, "private") {
			t.Errorf("argv = %v", a)
		}
	})
	t.Run("stage 1 subuid launches through --launch", func(t *testing.T) {
		a, _ := ComposeStage1(mk(func(o *Opts) { o.Mode, o.Subuid = "allow", true }), []string{"id"})
		if a[0] != "@SELF@" || a[1] != "--launch" || a[3] != "--" {
			t.Errorf("argv = %v", a)
		}
	})
	t.Run("stage 1 is argv only: no shell word", func(t *testing.T) {
		a, _ := ComposeStage1(mk(func(o *Opts) {}), []string{"id"})
		for _, w := range a[:2] {
			if w == "sh" || w == "bash" || w == "-c" {
				t.Errorf("shell in %v", a)
			}
		}
	})
}

func TestCfgJSONRoundTrip(t *testing.T) {
	f := newFixture(t)
	trial := f.trials + "/tr0001"
	cases := map[string]func(o *Opts){
		"deny": func(o *Opts) { o.RealClaude = f.claude; o.Forward = true },
		"allow": func(o *Opts) {
			o.Mode = "allow"
			o.Rw = []string{trial}
			o.Masks = []string{f.trials}
			o.TmpfsSizes = map[string]string{f.home: "64m"}
		},
		"nopid": func(o *Opts) { o.Mode, o.NoPid, o.Net = "allow", true, true },
		"subuid": func(o *Opts) {
			o.Mode, o.Subuid = "allow", true
			o.Copy = &CopySpec{Tree: f.root + "/gate", Size: "1m"}
		},
		"tmpsize": func(o *Opts) { o.Mode, o.TmpfsSize = "allow", "32m" },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			o := f.opts(trial)
			mod(&o)
			c, err := MakeCfg(o)
			must(t, err)
			b, err := json.Marshal(c)
			must(t, err)
			var back Cfg
			must(t, json.Unmarshal(b, &back))
			b2, _ := json.Marshal(&back)
			if string(b) != string(b2) {
				t.Errorf("round trip differs:\n%s\n%s", b, b2)
			}
			var raw map[string]any
			must(t, json.Unmarshal(b, &raw))
			if _, has := raw["pid"]; has != c.NoPid {
				t.Errorf("pid key present=%v want %v", has, c.NoPid)
			}
			if _, has := raw["mode"]; has != (c.Mode == "allow") {
				t.Errorf("mode key present=%v", has)
			}
		})
	}
}

func TestSubidRange(t *testing.T) {
	d := t.TempDir()
	cases := []struct {
		name, body string
		uid        int
		start, cnt int
	}{
		{"by name", "u:100000:65536\n", 1000, 100000, 65536},
		{"by number", "1000:200000:10\n", 1000, 200000, 10},
		{"largest wins", "u:1:5\nu:9:50\nu:3:7\n", 1000, 9, 50},
		{"other user ignored", "x:1:100\n", 1000, 0, 0},
		{"short line", "u:1\n", 1000, 0, 0},
		{"non-numeric", "u:a:b\n", 1000, 0, 0},
		{"negative is not digits", "u:-1:5\n", 1000, 0, 0},
		{"extra field", "u:1:5:6\n", 1000, 0, 0},
		{"empty file", "", 1000, 0, 0},
		{"blank lines", "\n\nu:7:8\n\n", 1000, 7, 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(d, strings.ReplaceAll(c.name, " ", "_"))
			must(t, os.WriteFile(p, []byte(c.body), 0o644))
			s, n := SubidRange(p, "u", c.uid)
			if s != c.start || n != c.cnt {
				t.Errorf("SubidRange = (%d, %d), want (%d, %d)", s, n, c.start, c.cnt)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		if s, n := SubidRange(d+"/nope", "u", 1); s != 0 || n != 0 {
			t.Errorf("got (%d, %d)", s, n)
		}
	})
}

func TestReadToken(t *testing.T) {
	d := t.TempDir()
	w := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(d, name)
		must(t, os.WriteFile(p, []byte(body), 0o600))
		must(t, os.Chmod(p, mode))
		return p
	}
	cases := []struct {
		name string
		path string
		want string // "" = refused
	}{
		{"ok", w("ok", "sk-ant-tok\n", 0o600), "sk-ant-tok"},
		{"trims", w("trim", "  tok \n", 0o600), "tok"},
		{"group readable", w("g", "tok", 0o640), ""},
		{"world readable", w("w", "tok", 0o644), ""},
		{"too tight is fine only at 0600", w("r", "tok", 0o400), ""},
		{"empty", w("e", "", 0o600), ""},
		{"missing", d + "/missing", ""},
		{"directory", d, ""},
	}
	link := filepath.Join(d, "link")
	must(t, os.Symlink(cases[0].path, link))
	cases = append(cases, struct {
		name, path, want string
	}{"symlink", link, ""})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadToken(c.path)
			if c.want == "" {
				if err == nil || !strings.Contains(err.Error(), NoTokenMsg) {
					t.Fatalf("err = %v, want a refusal", err)
				}
				if got != "" {
					t.Errorf("a refused token returned %q", got)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("ReadToken = %q, %v; want %q", got, err, c.want)
			}
		})
	}
	t.Run("the error never contains the token", func(t *testing.T) {
		p := w("leak", "SECRETVALUE", 0o644)
		_, err := ReadToken(p)
		if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRedact(t *testing.T) {
	cases := []struct {
		name, in, tok string
		shape         bool
		want          string
	}{
		{"exact token", "a TOKEN b", "TOKEN", false, "a [redacted-token] b"},
		{"every occurrence", "TOKENTOKEN", "TOKEN", false, "[redacted-token][redacted-token]"},
		{"shape only with shape", "x sk-ant-abc_1-2 y", "", true, "x [redacted-token] y"},
		{"shape off leaves it", "x sk-ant-abc y", "", false, "x sk-ant-abc y"},
		{"nothing to do", "plain", "", true, "plain"},
		{"token and shape", "TOKEN sk-ant-zzz", "TOKEN", true, "[redacted-token] [redacted-token]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Redact(c.in, c.tok, c.shape); got != c.want {
				t.Errorf("Redact = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStartLogAppend(t *testing.T) {
	read := func(dir string) string {
		b, _ := os.ReadFile(dir + "/log/start.log")
		return string(b)
	}
	t.Run("appends and redacts the exact token", func(t *testing.T) {
		d := t.TempDir()
		if !StartLogAppend(d, "hello TOK\n", "TOK", 100) {
			t.Fatal("append refused")
		}
		StartLogAppend(d, "more\n", "TOK", 100)
		if got := read(d); got != "hello [redacted-token]\nmore\n" {
			t.Errorf("log = %q", got)
		}
	})
	t.Run("never grows past the limit and says so", func(t *testing.T) {
		d := t.TempDir()
		if StartLogAppend(d, strings.Repeat("x", 30), "", 20) {
			t.Error("an over-limit append reported success")
		}
		if got := read(d); len(got) != 20 {
			t.Errorf("log length = %d", len(got))
		}
		if StartLogAppend(d, "y", "", 20) || len(read(d)) != 20 {
			t.Error("a full log grew")
		}
	})
	t.Run("a symlinked log dir is not followed", func(t *testing.T) {
		d, out := t.TempDir(), t.TempDir()
		must(t, os.Symlink(out, d+"/log"))
		if StartLogAppend(d, "x", "", 100) {
			t.Error("followed a symlinked log dir")
		}
		if _, err := os.Stat(out + "/start.log"); err == nil {
			t.Error("wrote through the symlink")
		}
	})
	t.Run("a symlinked start.log is not followed", func(t *testing.T) {
		d, out := t.TempDir(), t.TempDir()
		must(t, os.Mkdir(d+"/log", 0o700))
		must(t, os.Symlink(out+"/target", d+"/log/start.log"))
		if StartLogAppend(d, "x", "", 100) {
			t.Error("followed a symlinked start.log")
		}
		if _, err := os.Stat(out + "/target"); err == nil {
			t.Error("wrote through the symlink")
		}
	})
	t.Run("a symlinked trial dir is not followed", func(t *testing.T) {
		real, d := t.TempDir(), t.TempDir()
		must(t, os.Remove(d))
		must(t, os.Symlink(real, d))
		if StartLogAppend(d, "x", "", 100) {
			t.Error("followed a symlinked trial dir")
		}
	})
	t.Run("a missing trial dir is a quiet false", func(t *testing.T) {
		if StartLogAppend("/nonexistent-trial", "x", "", 100) {
			t.Error("reported success")
		}
	})
	t.Run("the log is created 0600", func(t *testing.T) {
		d := t.TempDir()
		StartLogAppend(d, "x", "", 100)
		fi, err := os.Stat(d + "/log/start.log")
		must(t, err)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", fi.Mode().Perm())
		}
	})
}

func TestRealpathAndUnder(t *testing.T) {
	d := Realpath(t.TempDir())
	must(t, os.Mkdir(d+"/real", 0o755))
	must(t, os.Symlink(d+"/real", d+"/link"))
	cases := []struct{ name, got, want string }{
		{"resolves a link", Realpath(d + "/link"), d + "/real"},
		{"keeps a missing leaf", Realpath(d + "/link/nope"), d + "/real/nope"},
		{"missing middle", Realpath(d + "/a/b/c"), d + "/a/b/c"},
		{"dot dot", Realpath(d + "/real/../real"), d + "/real"},
		{"root", Realpath("/"), "/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("got %q want %q", c.got, c.want)
			}
		})
	}
	for _, c := range []struct {
		p, b string
		want bool
	}{{"/a/b", "/a", true}, {"/a", "/a", true}, {"/ab", "/a", false}, {"/a", "/a/b", false}, {"/a/b", "/a/", true}, {"/", "/", true}, {"/x", "/", true}} {
		t.Run("under "+c.p+" "+c.b, func(t *testing.T) {
			if under(c.p, c.b) != c.want {
				t.Errorf("under(%q, %q) != %v", c.p, c.b, c.want)
			}
		})
	}
}

func TestSymlinkParent(t *testing.T) {
	d := Realpath(t.TempDir())
	must(t, os.MkdirAll(d+"/a/b", 0o755))
	must(t, os.Symlink(d+"/a", d+"/l"))
	cases := []struct{ path, want string }{
		{d + "/a/b", ""}, {d + "/l/b", d + "/l"}, {d + "/l", ""}, {d + "/a/b/c/d", ""}, {"/", ""},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := symlinkParent(c.path); got != c.want {
				t.Errorf("symlinkParent(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
	_ = syscall.Getpid
}
