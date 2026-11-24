package sandbox2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factory/go/internal/sandbox"
)

// Port of tests/test_ghcred.py (31 cases: HelperTests 12, OwnerCredentialTests 2, ShimTests 5, PrepareDirTests 8,
// IncludeIfIdentityTests 4). The helper and shim are the Go ones in internal/sandbox, run through this test binary
// (init in ghcred_helpers_test.go), the way the shipped binary runs them. Everything is in temp dirs with a fake HOME.
// not ported: test_token_foreign_uid runs only as root (it needs a file owned by another uid; unprivileged, no such
//   file can be made) and skips otherwise.
// The Python "the helper path names ghcred.py" assertions become "names git-credential" (the Go helper is a
// subcommand of the binary, not a script).

func TestGhcredFillAllowedRepoReturnsToken(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	for _, path := range []string{ghSlug + ".git", ghSlug} {
		r := b.fill(d, path)
		if r.Code != 0 {
			t.Fatalf("%s: exit %d: %s", path, r.Code, r.Err)
		}
		lines := strings.Split(r.Out, "\n")
		has := func(w string) bool {
			for _, l := range lines {
				if l == w {
					return true
				}
			}
			return false
		}
		if !has("username=x-access-token") || !has("password="+ghToken) {
			t.Fatalf("%s: stdout %q lacks the username or password line", path, r.Out)
		}
	}
}

func TestGhcredFillOtherRepoRefused(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	for _, path := range []string{"other/repo", "other/repo.git", ghSlug + "-evil", "x/" + ghSlug} {
		r := b.fill(d, path)
		if r.Code == 0 {
			t.Fatalf("%s: exit 0, want a refusal", path)
		}
		ghNotContains(t, path+" stdout", r.Out, "password=")
		ghNotContains(t, path+" output", r.Out+r.Err, ghToken)
	}
}

func TestGhcredFillOtherHostOrNoPathRefused(t *testing.T) {
	b := ghNew(t)
	b.mustPrepare(sandbox.PrepareDirOpts{})
	for _, inp := range []string{
		"protocol=https\nhost=evil.example\npath=volekerb/hivemill\n\n",
		"protocol=http\nhost=github.com\npath=volekerb/hivemill\n\n",
		"protocol=https\nhost=github.com\n\n",
	} {
		r := b.helper(inp, b.tokenEnv(), "get")
		if r.Code == 0 {
			t.Fatalf("%q: exit 0, want a refusal", inp)
		}
		ghNotContains(t, "stdout", r.Out, "password=")
	}
}

// assertUnavailable: git credential fill fails with no password, and the helper itself exits 1 with empty stdout and
// one stderr line saying the token is unavailable, never naming it.
func (b *ghBase) assertUnavailable(d string) {
	b.t.Helper()
	r := b.fill(d, ghSlug)
	if r.Code == 0 {
		b.t.Fatalf("fill exit 0, want a failure")
	}
	ghNotContains(b.t, "fill stdout", r.Out, "password=")
	h := b.helper(b.credReq(ghSlug), b.tokenEnv(), "get")
	ghEq(b.t, "helper exit", h.Code, 1)
	ghEq(b.t, "helper stdout", h.Out, "")
	ghContains(b.t, "helper stderr", h.Err, "worker gh token unavailable")
	ghEq(b.t, "helper stderr lines", len(strings.Split(strings.TrimSpace(h.Err), "\n")), 1)
	ghNotContains(b.t, "stderr", h.Err+r.Err, ghToken)
}

func TestGhcredTokenMissing(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	ghMust(t, "remove token", os.Remove(b.token))
	b.assertUnavailable(d)
}

func TestGhcredTokenMode0644(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	b.writeToken(ghToken, 0o644)
	b.assertUnavailable(d)
}

func TestGhcredTokenSymlink(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	real := filepath.Join(b.tmp, "elsewhere")
	ghWrite(t, real, ghToken, 0o600)
	ghMust(t, "remove token", os.Remove(b.token))
	ghMust(t, "symlink", os.Symlink(real, b.token))
	b.assertUnavailable(d)
}

func TestGhcredTokenEmpty(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	b.writeToken("  \n", 0o600)
	b.assertUnavailable(d)
}

// TestGhcredTokenForeignUid: a token file owned by another uid is refused, with one stderr line and nothing on stdout.
func TestGhcredTokenForeignUid(t *testing.T) {
	b := ghNew(t)
	b.mustPrepare(sandbox.PrepareDirOpts{})
	if err := os.Chown(b.token, os.Getuid()+1, -1); err != nil {
		t.Skipf("not ported: needs root to give the token file a foreign uid (%v)", err)
	}
	var out, errb strings.Builder
	code := sandbox.GitCredential(ghSlug, "get", strings.NewReader(b.credReq(ghSlug)), &out, &errb, map[string]string{"FACTORY_GH_TOKEN_FILE": b.token})
	ghEq(t, "exit", code, 1)
	ghEq(t, "stdout", out.String(), "")
	ghContains(t, "stderr", errb.String(), "worker gh token unavailable")
	ghNotContains(t, "stderr", errb.String(), ghToken)
}

func TestGhcredTokenWithEmbeddedNewline(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	b.writeToken("abc\nhost=evil", 0o600)
	b.assertUnavailable(d)
}

func TestGhcredTokenFifoDoesNotHang(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	ghMust(t, "remove token", os.Remove(b.token))
	ghMust(t, "mkfifo", ghMkfifo(b.token, 0o600))
	b.assertUnavailable(d)
}

func TestGhcredGhTokenEnvUsed(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	ghMust(t, "remove token", os.Remove(b.token))
	r := b.fill(d, ghSlug, "GH_TOKEN=from-env-token")
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Err)
	}
	ghContains(t, "stdout", "\n"+r.Out, "\n"+ghPwKey+"from-env-token\n")
}

func TestGhcredStoreAndEraseAreNoops(t *testing.T) {
	b := ghNew(t)
	for _, action := range []string{"store", "erase"} {
		r := b.helper("protocol=https\nhost=github.com\npath=x/y\n"+ghPwKey+"zzz\n\n", []string{}, action)
		if r.Code != 0 || r.Out != "" {
			t.Fatalf("%s: exit %d stdout %q, want 0 and empty", action, r.Code, r.Out)
		}
	}
}

func TestGhcredOwnerHelperIsNeverConsulted(t *testing.T) {
	b := ghNew(t)
	owner := filepath.Join(b.tmp, "owner-helper")
	ghWrite(t, owner, "#!/bin/sh\necho username=owner\necho "+ghPwKey+"OWNER\n", 0o755)
	f, err := os.OpenFile(filepath.Join(b.home, ".gitconfig"), os.O_APPEND|os.O_WRONLY, 0)
	ghMust(t, "open gitconfig", err)
	_, err = f.WriteString("[credential \"https://github.com\"]\n\thelper = \"!" + owner + "\"\n")
	ghMust(t, "append gitconfig", err)
	ghMust(t, "close gitconfig", f.Close())
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	env := b.env(d)
	r := ghGitOut(t, b.tmp, env, true, "config", "--get-all", "credential.https://github.com.helper")
	ghEq(t, "helper lines", len(strings.Split(strings.TrimSpace(r.Out), "\n")), 1)
	ghContains(t, "helper", r.Out, "git-credential")
	ghNotContains(t, "helper", r.Out, owner)
	ghNotContains(t, "fill stdout", b.fill(d, ghSlug).Out, "OWNER")
	ghMust(t, "remove token", os.Remove(b.token)) // fail closed: the owner helper does not take over
	f2 := b.fill(d, ghSlug)
	if f2.Code == 0 {
		t.Fatalf("fill exit 0 without a token, want a failure")
	}
	ghNotContains(t, "fill stdout", f2.Out, "OWNER")
}

func TestGhcredIdentityAndHttpPathInGitconfig(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	env := b.env(d)
	get := func(k string) string {
		return strings.TrimSpace(ghGitOut(t, b.tmp, env, false, "config", "--get", k).Out)
	}
	ghEq(t, "user.name", get("user.name"), "Test Owner")
	ghEq(t, "user.email", get("user.email"), "owner@example.invalid")
	ghEq(t, "credential.useHttpPath", get("credential.useHttpPath"), "true")
}

// ---- gh shim ----

func (b *ghBase) stubGh() {
	b.t.Helper()
	ghWrite(b.t, b.real, "#!/bin/sh\n"+
		"echo \"$GH_TOKEN|$GH_CONFIG_DIR|$GH_PROMPT_DISABLED|$*\" > "+b.tmp+"/gh-call\n"+
		"touch "+b.tmp+"/gh-ran\n", 0o755)
}

// runShim runs `gh pr view 7` through the shim of agent dir d; the shim's own PATH is d/bin first.
func (b *ghBase) runShim(d string, extra ...string) ghRes {
	b.t.Helper()
	env := append(b.env(d, extra...), "PATH="+filepath.Join(d, "bin")+":/usr/bin:/bin")
	return ghRun(b.t, b.tmp, env, "", filepath.Join(d, "bin", "gh"), "pr", "view", "7")
}

func TestGhcredShimSetsTokenAndConfigDir(t *testing.T) {
	b := ghNew(t)
	b.stubGh()
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	r := b.runShim(d)
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Err)
	}
	parts := strings.Split(strings.TrimSpace(ghRead(t, filepath.Join(b.tmp, "gh-call"))), "|")
	ghEq(t, "fields", len(parts), 4)
	ghEq(t, "GH_TOKEN", parts[0], ghToken)
	ghEq(t, "GH_CONFIG_DIR", parts[1], filepath.Join(d, "gh"))
	ghEq(t, "GH_PROMPT_DISABLED", parts[2], "1")
	ghEq(t, "args", parts[3], "pr view 7")
}

func TestGhcredShimReadsTokenAtCallTime(t *testing.T) {
	b := ghNew(t)
	b.stubGh()
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	b.writeToken("rotated-token", 0o600)
	b.runShim(d)
	if got := ghRead(t, filepath.Join(b.tmp, "gh-call")); !strings.HasPrefix(got, "rotated-token|") {
		t.Fatalf("gh-call = %q, want it to start with rotated-token|", got)
	}
}

func TestGhcredShimTokenMissingExits4WithoutRunningGh(t *testing.T) {
	b := ghNew(t)
	b.stubGh()
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	ghMust(t, "remove token", os.Remove(b.token))
	r := b.runShim(d)
	ghEq(t, "exit", r.Code, 4)
	ghContains(t, "stderr", r.Err, "worker gh token unavailable")
	if _, err := os.Stat(filepath.Join(b.tmp, "gh-ran")); err == nil {
		t.Fatal("the real gh ran")
	}
}

func TestGhcredShimMissingRealGhExits4(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	r := b.runShim(d)
	ghEq(t, "exit", r.Code, 4)
	ghContains(t, "stderr", r.Err, "cannot run the real gh")
}

func TestGhcredShimUsesFixedRealGhNotPath(t *testing.T) {
	b := ghNew(t)
	b.stubGh()
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	evil := filepath.Join(b.tmp, "evil")
	ghWrite(t, filepath.Join(evil, "gh"), "#!/bin/sh\ntouch "+b.tmp+"/evil-ran\n", 0o755)
	env := append(b.env(d), "PATH="+d+"/bin:"+evil+":/usr/bin:/bin")
	ghRun(t, b.tmp, env, "", filepath.Join(d, "bin", "gh"), "x")
	if _, err := os.Stat(filepath.Join(b.tmp, "gh-ran")); err != nil {
		t.Fatalf("the fixed real gh did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.tmp, "evil-ran")); err == nil {
		t.Fatal("the gh found on PATH ran")
	}
}

// ---- PrepareDir ----

func TestGhcredNoSecretWrittenAndModes(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	for _, f := range ghTree(t, d) {
		ghNotContains(t, f, ghRead(t, f), ghToken)
	}
	mode := func(p string) os.FileMode {
		fi, err := os.Stat(p)
		ghMust(t, "stat "+p, err)
		return fi.Mode().Perm()
	}
	ghEq(t, "agent dir mode", mode(d), os.FileMode(0o700))
	ghEq(t, "bin/gh mode", mode(filepath.Join(d, "bin", "gh")), os.FileMode(0o700))
	fi, err := os.Stat(filepath.Join(d, "gh"))
	ghMust(t, "stat gh dir", err)
	if !fi.IsDir() {
		t.Fatal("gh is not a directory")
	}
	ents, err := os.ReadDir(filepath.Join(d, "gh"))
	ghMust(t, "ReadDir", err)
	ghEq(t, "gh dir entries", len(ents), 0)
	ghNotContains(t, "PaneEnv", strings.Join(mapVals(sandbox.PaneEnv(d, b.token, "")), "\n"), ghToken)
}

func mapVals(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestGhcredPaneEnvKeys(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	e := sandbox.PaneEnv(d, b.token, ghPath())
	ghEq(t, "GIT_CONFIG_GLOBAL", e["GIT_CONFIG_GLOBAL"], d+"/gitconfig")
	ghEq(t, "GH_CONFIG_DIR", e["GH_CONFIG_DIR"], d+"/gh")
	ghEq(t, "FACTORY_GH_TOKEN_FILE", e["FACTORY_GH_TOKEN_FILE"], b.token)
	ghEq(t, "GIT_CONFIG_NOSYSTEM", e["GIT_CONFIG_NOSYSTEM"], "1")
	ghEq(t, "GIT_TERMINAL_PROMPT", e["GIT_TERMINAL_PROMPT"], "0")
	if !strings.HasPrefix(e["PATH"], d+"/bin:") {
		t.Fatalf("PATH = %q, want the prefix %s/bin:", e["PATH"], d)
	}
}

func TestGhcredPrepareTwiceReplacesFiles(t *testing.T) {
	b := ghNew(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{})
	ghEq(t, "second prepare", b.mustPrepare(sandbox.PrepareDirOpts{}), d)
	ghEq(t, "fill exit", b.fill(d, ghSlug).Code, 0)
}

func TestGhcredSymlinkedAgentDirRefused(t *testing.T) {
	b := ghNew(t)
	ghMust(t, "mkdir root", os.MkdirAll(b.root, 0o755))
	target := filepath.Join(b.tmp, "target")
	ghMust(t, "mkdir target", os.MkdirAll(target, 0o755))
	ghMust(t, "symlink", os.Symlink(target, filepath.Join(b.root, "coder-t-1")))
	if _, err := b.prepare(sandbox.PrepareDirOpts{}); err == nil {
		t.Fatal("PrepareDir through a symlinked agent dir succeeded")
	}
	ents, err := os.ReadDir(target)
	ghMust(t, "ReadDir", err)
	ghEq(t, "target entries", len(ents), 0)
}

func TestGhcredTokenPathOutsideConfigFactoryRefused(t *testing.T) {
	b := ghNew(t)
	hosts := filepath.Join(b.home, ".config", "gh", "hosts.yml")
	ghWrite(t, hosts, "github.com:\n  oauth_token: OWNER\n", 0o644)
	for _, bad := range []string{hosts, filepath.Join(b.tmp, "tok"), filepath.Join(b.home, ".config", "factory", "sub", "t")} {
		ghWrite(t, bad, "x", 0o644)
		if _, err := b.prepare(sandbox.PrepareDirOpts{Agent: "a", TokenPath: bad}); err == nil {
			t.Fatalf("%s: PrepareDir accepted a token path outside ~/.config/factory", bad)
		}
	}
}

func TestGhcredTokenSymlinkIntoGhConfigRefused(t *testing.T) {
	b := ghNew(t)
	hosts := filepath.Join(b.home, ".config", "gh", "hosts.yml")
	ghWrite(t, hosts, "x", 0o644)
	link := filepath.Join(b.home, ".config", "factory", "link")
	ghMust(t, "symlink", os.Symlink(hosts, link))
	if _, err := b.prepare(sandbox.PrepareDirOpts{Agent: "a", TokenPath: link, RealGh: "/usr/bin/gh"}); err == nil {
		t.Fatal("a token symlink into the owner gh config was accepted")
	}
}

func TestGhcredTokenSymlinkInsideConfigDirRefused(t *testing.T) {
	b := ghNew(t)
	link := filepath.Join(b.home, ".config", "factory", "alias")
	ghMust(t, "symlink", os.Symlink(b.token, link))
	if _, err := b.prepare(sandbox.PrepareDirOpts{Agent: "a", TokenPath: link, RealGh: "/usr/bin/gh"}); err == nil {
		t.Fatal("a symlink to the token inside the config dir was accepted")
	}
}

func TestGhcredBadInputs(t *testing.T) {
	b := ghNew(t)
	for _, slugs := range [][]string{{}, {"no-slash"}, {"a/b;rm"}} {
		if _, err := b.prepare(sandbox.PrepareDirOpts{Slugs: append([]string{}, slugs...)}); err == nil {
			t.Fatalf("slugs %q accepted", slugs)
		}
	}
	if _, err := b.prepare(sandbox.PrepareDirOpts{Agent: "../x", RealGh: "/usr/bin/gh"}); err == nil {
		t.Fatal("agent ../x accepted")
	}
	if _, err := b.prepare(sandbox.PrepareDirOpts{Agent: "a", RealGh: "gh"}); err == nil {
		t.Fatal("a relative real gh accepted")
	}
}

// ---- includeIf identity ----

const (
	ghPrivate = "a@example.invalid"
	ghNoreply = "zz-test@example.invalid"
)

// ghIncl is IncludeIfIdentityTests.setUp: the owner gitconfig includes a noreply identity for repos under tmp/factory/.
func ghIncl(t *testing.T) (*ghBase, string) {
	t.Helper()
	b := ghNew(t)
	inc := filepath.Join(b.tmp, "hivemill.gitconfig")
	ghWrite(t, inc, "[user]\n\temail = "+ghNoreply+"\n", 0o644)
	ghWrite(t, filepath.Join(b.home, ".gitconfig"),
		"[user]\n\tname = Owner\n\temail = "+ghPrivate+"\n[includeIf \"gitdir:"+b.tmp+"/factory/\"]\n\tpath = "+inc+"\n", 0o644)
	repo := filepath.Join(b.tmp, "factory", "worktrees", "G-1-T-1")
	ghMust(t, "mkdir repo", os.MkdirAll(repo, 0o755))
	ghGitOut(t, "", []string{"PATH=" + ghPath(), "HOME=" + b.home}, true, "init", "-q", repo)
	return b, repo
}

func TestGhcredCwdInIncludedTreeGetsNoreply(t *testing.T) {
	b, repo := ghIncl(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{Cwd: repo})
	text := ghRead(t, filepath.Join(d, "gitconfig"))
	ghContains(t, "gitconfig", text, ghNoreply)
	ghNotContains(t, "gitconfig", text, ghPrivate)
}

func TestGhcredNoOrBadCwdFallsBack(t *testing.T) {
	b, _ := ghIncl(t)
	for _, cwd := range []string{"", "/", filepath.Join(b.tmp, "missing")} {
		d := b.mustPrepare(sandbox.PrepareDirOpts{Cwd: cwd})
		text := ghRead(t, filepath.Join(d, "gitconfig"))
		ghContains(t, "gitconfig for cwd "+cwd, text, ghPrivate)
		ghNotContains(t, "gitconfig for cwd "+cwd, text, ghNoreply)
	}
}

func TestGhcredWrittenConfigHasNoIncludeOrForeignHelper(t *testing.T) {
	b, repo := ghIncl(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{Cwd: repo})
	out := ghGitOut(t, "", []string{"PATH=" + ghPath(), "HOME=" + b.tmp}, true, "config", "--file", filepath.Join(d, "gitconfig"), "--list").Out
	var helpers []string
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		k, _, _ := strings.Cut(ln, "=")
		if strings.HasPrefix(k, "include") {
			t.Fatalf("written config has an include key: %s", ln)
		}
		if strings.Contains(ln, ".helper=") {
			helpers = append(helpers, ln)
		}
	}
	ghEq(t, "helper lines", len(helpers), 1)
	ghContains(t, "helper", helpers[0], "git-credential")
}

func TestGhcredCommitUsesNoreplyIdentity(t *testing.T) {
	b, repo := ghIncl(t)
	d := b.mustPrepare(sandbox.PrepareDirOpts{Cwd: repo})
	env := []string{"PATH=" + ghPath(), "HOME=" + filepath.Join(b.tmp, "nohome"),
		"GIT_CONFIG_GLOBAL=" + filepath.Join(d, "gitconfig"), "GIT_CONFIG_NOSYSTEM=1"}
	ghGitOut(t, repo, env, true, "commit", "-q", "--allow-empty", "-m", "x")
	r := ghGitOut(t, repo, env, true, "log", "-1", "--format=%ae %ce")
	got := strings.Fields(r.Out)
	if len(got) != 2 || got[0] != ghNoreply || got[1] != ghNoreply {
		t.Fatalf("author/committer = %q, want both %s", got, ghNoreply)
	}
}
