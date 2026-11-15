package sandbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func tokenFile(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	must(t, os.WriteFile(p, []byte(body), 0o600))
	must(t, os.Chmod(p, mode))
	return p
}

// runHelper runs the real entry point (this test binary as the Go binary) with a scrubbed env.
func runHelper(t *testing.T, stdin string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return o.String(), e.String(), code
}

const goodReq = "protocol=https\nhost=github.com\npath=acme/app.git\n\n"

// TestGhCredScope: the credential helper answers only for the listed repos over https on github.com, the token is
// read at call time from $GH_TOKEN or a 0600 own-uid regular file (never a symlink), every failure is silent on
// stdout and names no token, the box env carries the token only for the role that has a credential, and the files
// PrepareDir writes hold no secret.
func TestGhCredScope(t *testing.T) {
	dir := t.TempDir()
	good := tokenFile(t, dir, "good", "ghs_SECRETTOKEN\n", 0o600)
	t.Run("helper scope", func(t *testing.T) {
		cases := []struct {
			name, slugs, req, action string
			env                      map[string]string
			code                     int
			wantOut                  string
		}{
			{"allowed repo with .git", "acme/app", goodReq, "get", map[string]string{"GH_TOKEN": "tok"}, 0, "username=x-access-token\npassword=tok\n"},
			{"allowed repo without .git", "acme/app", "protocol=https\nhost=github.com\npath=acme/app\n", "get", map[string]string{"GH_TOKEN": "tok"}, 0, "username=x-access-token\npassword=tok\n"},
			{"second slug", "other/x,acme/app", goodReq, "get", map[string]string{"GH_TOKEN": "tok"}, 0, "username=x-access-token\npassword=tok\n"},
			{"token from file", "acme/app", goodReq, "get", map[string]string{"FACTORY_GH_TOKEN_FILE": good}, 0, "username=x-access-token\npassword" + "=ghs_SECRETTOKEN\n"},
			{"other repo", "acme/app", "protocol=https\nhost=github.com\npath=acme/other.git\n", "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"prefix repo", "acme/app", "protocol=https\nhost=github.com\npath=acme/app-evil.git\n", "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"other owner", "acme/app", "protocol=https\nhost=github.com\npath=evil/app.git\n", "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"other host", "acme/app", "protocol=https\nhost=gitlab.com\npath=acme/app.git\n", "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"host lookalike", "acme/app", "protocol=https\nhost=github.com.evil.io\npath=acme/app.git\n", "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"plain http", "acme/app", "protocol=http\nhost=github.com\npath=acme/app.git\n", "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"no path", "acme/app", "protocol=https\nhost=github.com\n", "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"empty request", "acme/app", "", "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"bad slug list", "not a slug", goodReq, "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"empty slug list", "", goodReq, "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"one bad slug spoils the list", "acme/app,bad", goodReq, "get", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"no token at all", "acme/app", goodReq, "get", map[string]string{}, 1, ""},
			{"unknown action", "acme/app", goodReq, "list", map[string]string{"GH_TOKEN": "tok"}, 1, ""},
			{"store is a no-op", "acme/app", goodReq, "store", map[string]string{"GH_TOKEN": "tok"}, 0, ""},
			{"erase is a no-op", "acme/app", goodReq, "erase", map[string]string{}, 0, ""},
			{"whitespace token is refused, not replaced by the file", "acme/app", goodReq, "get", map[string]string{"GH_TOKEN": "a b", "FACTORY_GH_TOKEN_FILE": good}, 1, ""},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				var out, errb bytes.Buffer
				code := GitCredential(c.slugs, c.action, strings.NewReader(c.req), &out, &errb, c.env)
				if code != c.code || out.String() != c.wantOut {
					t.Errorf("code=%d out=%q err=%q; want code=%d out=%q", code, out.String(), errb.String(), c.code, c.wantOut)
				}
				if c.code != 0 && (out.Len() != 0 || strings.Contains(errb.String(), "password=") || strings.Contains(errb.String(), "SECRET") || strings.Contains(errb.String(), "=tok")) {
					t.Errorf("a failure leaked: stdout=%q stderr=%q", out.String(), errb.String())
				}
			})
		}
	})
	t.Run("token file rules", func(t *testing.T) {
		link := filepath.Join(dir, "link")
		must(t, os.Symlink(good, link))
		cases := []struct {
			name, path, want string
		}{
			{"0600 regular file", good, "ghs_SECRETTOKEN"},
			{"group readable", tokenFile(t, dir, "g", "tok", 0o640), ""},
			{"world readable", tokenFile(t, dir, "w", "tok", 0o644), ""},
			{"symlink", link, ""},
			{"empty file", tokenFile(t, dir, "e", "  \n", 0o600), ""},
			{"embedded space", tokenFile(t, dir, "s", "a b", 0o600), ""},
			{"embedded newline", tokenFile(t, dir, "n", "a\nb", 0o600), ""},
			{"control char", tokenFile(t, dir, "c", "a\x01b", 0o600), ""},
			{"missing", dir + "/missing", ""},
			{"directory", dir, ""},
			{"unset", "", ""},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if got := ReadTokenFile(c.path); got != c.want {
					t.Errorf("ReadTokenFile = %q, want %q", got, c.want)
				}
			})
		}
	})
	t.Run("real entry point: the helper answers on stdout only for the allowed repo", func(t *testing.T) {
		env := []string{"GH_TOKEN=tok"}
		out, _, code := runHelper(t, goodReq, env, "git-credential", "acme/app", "get")
		if code != 0 || out != "username=x-access-token\npassword=tok\n" {
			t.Errorf("allowed: code=%d out=%q", code, out)
		}
		out, errs, code := runHelper(t, "protocol=https\nhost=github.com\npath=evil/x.git\n", env, "git-credential", "acme/app", "get")
		if code != 1 || out != "" || strings.Contains(errs, "password=") || strings.Contains(errs, "=tok") {
			t.Errorf("refused: code=%d out=%q err=%q", code, out, errs)
		}
	})
	t.Run("real entry point: the gh shim does not run gh without a token", func(t *testing.T) {
		marker := filepath.Join(dir, "ran")
		fake := filepath.Join(dir, "fake-gh")
		must(t, os.WriteFile(fake, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755))
		_, errs, code := runHelper(t, "", []string{"PATH=/usr/bin:/bin"}, "gh-shim", fake, dir+"/gh", "pr", "list")
		if code != ShimExit || !strings.Contains(errs, GhNoTokenMsg) {
			t.Errorf("code=%d err=%q", code, errs)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("gh ran without a token")
		}
	})
	t.Run("real entry point: the gh shim runs gh with only the worker token", func(t *testing.T) {
		out := filepath.Join(dir, "env.txt")
		fake := filepath.Join(dir, "fake-gh2")
		must(t, os.WriteFile(fake, []byte("#!/bin/sh\nenv > "+out+"\n"), 0o755))
		env := []string{"PATH=/usr/bin:/bin", "GH_TOKEN=worker", "GITHUB_TOKEN=owner", "GH_ENTERPRISE_TOKEN=ent"}
		_, _, code := runHelper(t, "", env, "gh-shim", fake, dir+"/ghcfg", "pr", "list")
		if code != 0 {
			t.Fatalf("code = %d", code)
		}
		b, _ := os.ReadFile(out)
		got := string(b)
		for _, want := range []string{"GH_TOKEN=worker", "GH_CONFIG_DIR=" + dir + "/ghcfg", "GH_PROMPT_DISABLED=1"} {
			if !strings.Contains(got, want) {
				t.Errorf("gh env lacks %s: %s", want, got)
			}
		}
		for _, bad := range []string{"GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "owner", "ent"} {
			if strings.Contains(got, bad+"\n") || strings.Contains(got, bad+"=") {
				t.Errorf("gh env carries %s: %s", bad, got)
			}
		}
	})
	t.Run("GhShimEnv", func(t *testing.T) {
		if _, ok := GhShimEnv([]string{"PATH=/x"}, "/c"); ok {
			t.Error("a tokenless env was accepted")
		}
		env, ok := GhShimEnv([]string{"PATH=/x", "GH_TOKEN=a", "GH_CONFIG_DIR=/evil"}, "/c")
		if !ok || strings.Count(strings.Join(env, "\n"), "GH_TOKEN=") != 1 || !contains(env, "GH_CONFIG_DIR=/c") {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("roles without a gh credential get none", func(t *testing.T) {
		p := PathsFor("/h", "", "")
		cases := []struct{ role, want string }{
			{"coder", "/h/.config/factory/gh-worker-rw-token"}, {"reviewer", "/h/.config/factory/gh-worker-ro-token"},
			{"qa", "/h/.config/factory/gh-worker-ro-token"}, {"researcher", "/h/.config/factory/gh-worker-ro-token"},
			{"judge", ""}, {"pm", ""}, {"cleaner", ""}, {"boss", ""}, {"lead", ""}, {"evaluator", ""}, {"", ""}, {"CODER", ""},
		}
		for _, c := range cases {
			t.Run(c.role, func(t *testing.T) {
				if got := p.GhTokenFor(c.role); got != c.want {
					t.Errorf("GhTokenFor(%q) = %q, want %q", c.role, got, c.want)
				}
			})
		}
	})
	t.Run("the write token goes to the coder only", func(t *testing.T) {
		p := PathsFor("/h", "", "")
		for _, role := range []string{"reviewer", "qa", "researcher"} {
			if p.GhTokenFor(role) == p.GhRW {
				t.Errorf("%s was given the read-write gh token", role)
			}
		}
		if p.GhTokenFor("coder") != p.GhRW {
			t.Error("coder lost the read-write token")
		}
	})
	t.Run("PrepareDir writes paths and commands, never a token", func(t *testing.T) {
		home := Realpath(t.TempDir())
		must(t, os.MkdirAll(home+"/.config/factory", 0o700))
		tok := tokenFile(t, home+"/.config/factory", "gh-worker-rw-token", "ghs_MUSTNOTLEAK", 0o600)
		root := home + "/ghcred"
		agentDir, err := PrepareDir(PrepareDirOpts{Agent: "claude-coder-t-1", Role: "coder", TokenPath: tok, Slugs: []string{"acme/app"},
			RealGh: "/usr/bin/gh", Home: home, Root: root, Self: "/opt/factory", Identity: &[2]string{"Ann Dev", "ann-example.invalid"}})
		must(t, err)
		var all string
		for _, f := range []string{"gitconfig", "bin/gh"} {
			b, err := os.ReadFile(filepath.Join(agentDir, f))
			must(t, err)
			all += string(b)
			fi, _ := os.Stat(filepath.Join(agentDir, f))
			if fi.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s is group/world accessible: %v", f, fi.Mode().Perm())
			}
		}
		if strings.Contains(all, "MUSTNOTLEAK") {
			t.Errorf("a token reached the generated files: %s", all)
		}
		for _, want := range []string{`name = "Ann Dev"`, "useHttpPath = true", `[credential "https://github.com"]`, "/opt/factory git-credential acme/app",
			"exec /opt/factory gh-shim /usr/bin/gh " + agentDir + "/gh"} {
			if !strings.Contains(all, want) {
				t.Errorf("generated files lack %q:\n%s", want, all)
			}
		}
		if fi, err := os.Stat(agentDir + "/gh"); err != nil || !fi.IsDir() {
			t.Errorf("gh config dir missing: %v", err)
		}
		env := PaneEnv(agentDir, tok, "/usr/bin")
		for k, v := range env {
			if strings.Contains(v, "MUSTNOTLEAK") || strings.HasSuffix(k, "TOKEN") {
				t.Errorf("pane env %s=%s carries a token", k, v)
			}
		}
		if env["FACTORY_GH_TOKEN_FILE"] != tok || !strings.HasPrefix(env["PATH"], agentDir+"/bin:") || env["GIT_TERMINAL_PROMPT"] != "0" {
			t.Errorf("pane env = %v", env)
		}
	})
	t.Run("PrepareDir refusals", func(t *testing.T) {
		home := Realpath(t.TempDir())
		must(t, os.MkdirAll(home+"/.config/factory", 0o700))
		tok := tokenFile(t, home+"/.config/factory", "t", "x", 0o600)
		outside := tokenFile(t, home, "outside", "x", 0o600)
		link := home + "/.config/factory/link"
		must(t, os.Symlink(tok, link))
		base := PrepareDirOpts{Agent: "a1", Role: "coder", TokenPath: tok, Slugs: []string{"acme/app"}, RealGh: "/usr/bin/gh", Home: home,
			Root: home + "/r", Self: "/x", Identity: &[2]string{}}
		cases := []struct {
			name string
			mod  func(o *PrepareDirOpts)
			want string
		}{
			{"bad agent name", func(o *PrepareDirOpts) { o.Agent = "../evil" }, "bad agent name"},
			{"empty agent", func(o *PrepareDirOpts) { o.Agent = "" }, "bad agent name"},
			{"no slugs", func(o *PrepareDirOpts) { o.Slugs = nil }, "OWNER/NAME"},
			{"bad slug", func(o *PrepareDirOpts) { o.Slugs = []string{"acme"} }, "OWNER/NAME"},
			{"relative gh", func(o *PrepareDirOpts) { o.RealGh = "gh" }, "absolute"},
			{"token outside the config dir", func(o *PrepareDirOpts) { o.TokenPath = outside }, "directly in"},
			{"token symlink", func(o *PrepareDirOpts) { o.TokenPath = link }, "directly in"},
			{"token missing", func(o *PrepareDirOpts) { o.TokenPath = home + "/.config/factory/none" }, "directly in"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				o := base
				c.mod(&o)
				if _, err := PrepareDir(o); err == nil || !strings.Contains(err.Error(), c.want) {
					t.Errorf("err = %v, want %q", err, c.want)
				}
			})
		}
		t.Run("a symlinked agent dir is refused", func(t *testing.T) {
			must(t, os.MkdirAll(home+"/r", 0o700))
			must(t, os.Symlink(t.TempDir(), home+"/r/evil"))
			o := base
			o.Agent = "evil"
			if _, err := PrepareDir(o); err == nil {
				t.Error("followed a symlinked agent dir")
			}
		})
	})
	t.Run("slugs and names", func(t *testing.T) {
		for _, s := range []string{"a/b", "A-b_c.d/e.f", "acme/app.git"} {
			if !ValidSlug(s) {
				t.Errorf("%q should be a slug", s)
			}
		}
		for _, s := range []string{"", "a", "a/", "/b", "a/b/c", "a b/c", "a/b;x", "a/b\n", "$(x)/y"} {
			if ValidSlug(s) {
				t.Errorf("%q should not be a slug", s)
			}
		}
	})
}
