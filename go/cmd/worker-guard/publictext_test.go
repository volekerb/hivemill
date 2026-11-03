package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// T-0887: with the public-text rule on (public_text_repos / frozen_repos in the export) the binary decides git and gh
// writes itself. The binary runs with a --py-hook whose hook file fails when started, next to copies of the real ci/
// files: a delegation would exit 2 with "failing closed", so an answer equal to the Python hook's proves no Python ran.

func gitIn(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), append([]string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com"}, env...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// failingHookLayout is <root>/roles/hooks/worker-guard.py (exit 1 if started) and <root>/ci with the real checker files.
func failingHookLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"roles/hooks", "ci"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hook := filepath.Join(root, "roles", "hooks", "worker-guard.py")
	if err := os.WriteFile(hook, []byte("import sys\nsys.exit(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"public_text_guard.py", "scrub_scan.py", "scrub_allowlist.txt"} {
		b, err := os.ReadFile(filepath.Join("../../../ci", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "ci", f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return hook
}

func publicTextRepo(t *testing.T, origin string) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, nil, "init", "-q", "-b", "main")
	gitIn(t, repo, nil, "remote", "add", "origin", origin)
	gitIn(t, repo, nil, "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, repo, nil, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, repo, nil, "branch", "clean")
	gitIn(t, repo, nil, "checkout", "-q", "-b", "bad")
	gitIn(t, repo, []string{"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@" + "realmail.test"}, "commit", "-q", "--allow-empty", "-m", "add rule")
	gitIn(t, repo, nil, "checkout", "-q", "clean")
	return repo
}

func TestPublicTextOnDecidesWithoutPython(t *testing.T) {
	hook := absHook(t)
	stub := failingHookLayout(t)
	repo := publicTextRepo(t, "https://github.com/o/pub.git")
	frozen := publicTextRepo(t, "git@"+"github.com:o/old.git")
	home := seedDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)",
		"INSERT INTO config VALUES ('public_text_repos', 'o/pub')", "INSERT INTO config VALUES ('frozen_repos', 'o/old')")
	env := withTerms(t, "FACTORY_HOME="+home)
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("about the secret plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, cmd, cwd string
		code           int
		want           string
	}{
		{"clean commit", "git commit -m 'add the rule'", repo, 0, ""},
		{"commit with an owner term", "git commit -m 'about the secret plan'", repo, 2, "public text: 1 hits: owner-term"},
		{"gh pr create clean", "gh pr create --title t --body 'fine'", repo, 0, ""},
		{"gh pr create with a term", "gh pr create --title t --body 'the secret'", repo, 2, "public text: 1 hits: owner-term"},
		{"gh pr create body file with a term", "gh pr create --title t --body-file " + body, repo, 2, "public text: 1 hits: owner-term"},
		{"gh pr create dynamic text", "gh pr create --title t --body \"$X\"", repo, 2, "write the text to a file"},
		{"gh api patch with a term", "gh api -X PATCH repos/o/pub/pulls/1 -f body=secret", repo, 2, "public text: 1 hits: owner-term"},
		{"push clean", "git push origin HEAD:factory/t-1", repo, 0, ""},
		{"push with a real author email", "git push origin bad:factory/t-1", repo, 2, "public text: 1 hits: email"},
		{"push of a branch named with an address", "git push origin HEAD:factory/" + strings.Join([]string{"10", "0", "0", "5"}, "."), repo, 2, "public text: 2 hits: ipv4, rfc1918"},
		{"frozen push", "git push origin HEAD", frozen, 2, "is frozen, G-021"},
		{"frozen gh write", "gh pr create -R o/old --title t --body b", repo, 2, "is frozen, G-021"},
		{"not a write", "git status", repo, 0, ""},
	} {
		payload := []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(c.cmd) + `},"cwd":` + jsonString(c.cwd) + `}`)
		code, stderr := call(t, payload, env, "--role", "judge", "--py-hook", stub)
		if code != c.code || strings.Contains(stderr, "failing closed") || strings.Contains(stderr, "Python hook") || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d %q, want %d containing %q and no delegation", c.name, code, stderr, c.code, c.want)
		}
		pcode, pstderr := callPython(t, hook, payload, env, "--role", "judge")
		if pcode != code || pstderr != stderr {
			t.Errorf("%s: python exit %d %q, go exit %d %q", c.name, pcode, pstderr, code, stderr)
		}
	}
}

func execCombined(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// A rule that cannot decide fails closed, with the Python hook's words: no owner terms, a checker that is missing,
// and (Go only) a push whose allowlist cannot be located because the shim did not pass --py-hook.
func TestPublicTextFailsClosed(t *testing.T) {
	hook := absHook(t)
	stub := failingHookLayout(t)
	repo := publicTextRepo(t, "https://github.com/o/pub.git")
	home := seedDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)", "INSERT INTO config VALUES ('public_text_repos', 'o/pub')")
	terms := withTerms(t, "FACTORY_HOME="+home)
	noTerms := []string{"FACTORY_HOME=" + home}
	payload := func(cmd string) []byte {
		return []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(cmd) + `},"cwd":` + jsonString(repo) + `}`)
	}
	// no owner terms loaded: exit 2 with the same message as the Python hook (and a terms file that cannot be read)
	for _, env := range [][]string{noTerms, append(append([]string{}, noTerms...), "SCRUB_"+"TERMS_FILE="+filepath.Join(t.TempDir(), "missing.txt"))} {
		code, stderr := call(t, payload("git commit -m ok"), env, "--role", "judge", "--py-hook", stub)
		pcode, pstderr := callPython(t, hook, payload("git commit -m ok"), env, "--role", "judge")
		if code != 2 || code != pcode || stderr != pstderr || !strings.Contains(stderr, "terms") {
			t.Errorf("no terms: go exit %d %q, python exit %d %q", code, stderr, pcode, pstderr)
		}
	}
	// a hook checkout without ci/: the checker cannot be loaded
	bare := filepath.Join(t.TempDir(), "roles", "hooks")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, hook, filepath.Join(bare, "worker-guard.py"))
	code, stderr := call(t, payload("git commit -m ok"), terms, "--role", "judge", "--py-hook", filepath.Join(bare, "worker-guard.py"))
	pcode, pstderr := callPython(t, filepath.Join(bare, "worker-guard.py"), payload("git commit -m ok"), terms, "--role", "judge")
	if code != 2 || code != pcode || stderr != pstderr || !strings.Contains(stderr, "cannot be loaded") {
		t.Errorf("no checker: go exit %d %q, python exit %d %q", code, stderr, pcode, pstderr)
	}
	// text checks need no hook path; a push needs the allowlist beside it
	if code, stderr := call(t, payload("git commit -m ok"), terms, "--role", "judge"); code != 0 {
		t.Errorf("commit without --py-hook: exit %d %q, want 0", code, stderr)
	}
	if code, stderr := call(t, payload("git push origin HEAD:factory/t-1"), terms, "--role", "judge"); code != 2 || !strings.Contains(stderr, "--py-hook") {
		t.Errorf("push without --py-hook: exit %d %q, want 2 naming --py-hook", code, stderr)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
