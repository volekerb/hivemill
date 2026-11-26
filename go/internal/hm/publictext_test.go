package hm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test values are built from parts, so that this file holds no owner term and no key shape.
var (
	ptTerm = "zebra" + "corp"
	ptKey  = "AKIA" + strings.Repeat("Q7", 8)
)

// publicTextCase is a runner with owner terms set and a clone whose one commit past origin/main adds file with content.
func publicTextCase(t *testing.T, publicRepos, file, content string) (*Runner, string) {
	t.Helper()
	r, s := testRunner(t, "")
	home, wt := r.Env.Home, t.TempDir()
	terms := filepath.Join(home, "terms.txt")
	write := func(path, text string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(terms, ptTerm+"\n")
	write(filepath.Join(home, "ci", "scrub_allowlist.txt"), HomeAllowlist)
	for k, v := range map[string]string{"scrub_terms_file": terms, "public_text_repos": publicRepos} {
		if err := s.SetFactoryCfg(k, v); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SCRUB_TERMS_FILE", "")
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", wt, "-c", "user.name=t", "-c", "user.email=t@users.noreply.github.com",
			"-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "base")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	write(filepath.Join(wt, file), content+"\n")
	git("add", "-A")
	git("commit", "-q", "-m", "change")
	return r, wt
}

func TestPublicTextSkipsOwnerTermsForPrivateRepo(t *testing.T) {
	// the term in a path, in a file and in the PR text, plus an email and a home path: none of it is a secret
	content := ptTerm + " mail dev@" + ptTerm + ".io in /ho" + "me/" + ptTerm + "/x"
	for _, public := range []string{"", "acme/other"} {
		r, wt := publicTextCase(t, public, ptTerm+"/notes.txt", content)
		pr := &PR{Title: ptTerm, Body: content}
		if ok, note := r.publicText("acme/app", wt, "main", pr); !ok {
			t.Errorf("public_text_repos %q: %s", public, note)
		}
		// no terms configured at all is no reason to block a private repo
		if err := r.Store.(*SQLite).SetFactoryCfg("scrub_terms_file", ""); err != nil {
			t.Fatal(err)
		}
		if ok, note := r.publicText("acme/app", wt, "main", pr); !ok {
			t.Errorf("no terms: %s", note)
		}
	}
}

func TestPublicTextSecretsStillCheckedForPrivateRepo(t *testing.T) {
	r, wt := publicTextCase(t, "acme/other", "cfg.txt", "id "+ptKey)
	// the environment's terms are not loaded either
	t.Setenv("SCRUB_TERMS_FILE", r.Store.FactoryCfg("scrub_terms_file"))
	ok, note := r.publicText("acme/app", wt, "main", nil)
	if ok || !strings.Contains(note, "(secrets) on the commits") || !strings.Contains(note, "aws-access-key-id") ||
		strings.Contains(note, ptKey) || strings.Contains(note, "owner term") {
		t.Errorf("key in a commit: %v %q", ok, note)
	}
	r, wt = publicTextCase(t, "", "cfg.txt", "clean")
	ok, note = r.publicText("acme/app", wt, "main", &PR{Title: "x", Body: "id " + ptKey})
	if ok || !strings.Contains(note, "(secrets) on the PR title/body") || strings.Contains(note, ptKey) {
		t.Errorf("key in the PR body: %v %q", ok, note)
	}
}

// A list that cannot be read must not pass as "not listed".
func TestPublicTextUnreadableRepoListRefuses(t *testing.T) {
	r, wt := publicTextCase(t, "acme/app", "notes.txt", "about "+ptTerm)
	if err := r.Store.(*SQLite).Close(); err != nil {
		t.Fatal(err)
	}
	if ok, note := r.publicText("acme/app", wt, "main", nil); ok || !strings.Contains(note, "cannot read public_text_repos") {
		t.Errorf("unreadable list: %v %q", ok, note)
	}
}

// A stored list that hivemill config would refuse must not pass as "not listed" either.
func TestPublicTextBadRepoListRefuses(t *testing.T) {
	r, wt := publicTextCase(t, "acme/app acme/api", "notes.txt", "about "+ptTerm)
	if ok, note := r.publicText("acme/app", wt, "main", nil); ok || !strings.Contains(note, "is not OWNER/NAME") {
		t.Errorf("bad list: %v %q", ok, note)
	}
}

func TestPublicTextOwnerTermsForPublicRepo(t *testing.T) {
	r, wt := publicTextCase(t, "acme/other, Acme/App", "notes.txt", "about "+ptTerm)
	ok, note := r.publicText("acme/APP", wt, "main", nil)
	if ok || !strings.Contains(note, "(owner terms) on the commits") || strings.Contains(note, ptTerm) {
		t.Errorf("term in a commit: %v %q", ok, note)
	}
	r, wt = publicTextCase(t, "acme/app", "cfg.txt", "id "+ptKey)
	if ok, note = r.publicText("acme/app", wt, "main", nil); ok || !strings.Contains(note, "(secrets) on the commits") {
		t.Errorf("key in a commit: %v %q", ok, note)
	}
	r, wt = publicTextCase(t, "acme/app", "cfg.txt", "clean")
	ok, note = r.publicText("acme/app", wt, "main", &PR{Title: ptTerm, Body: "id " + ptKey})
	if ok || !strings.Contains(note, "(owner terms and secrets) on the PR title/body") || strings.Contains(note, ptTerm) {
		t.Errorf("both in the PR text: %v %q", ok, note)
	}
	if ok, note = r.publicText("acme/app", wt, "main", &PR{Title: "x", Body: "clean"}); !ok {
		t.Errorf("clean: %s", note)
	}
	// no terms: nothing passes
	if err := r.Store.(*SQLite).SetFactoryCfg("scrub_terms_file", ""); err != nil {
		t.Fatal(err)
	}
	if ok, note = r.publicText("acme/app", wt, "main", nil); ok || !strings.Contains(note, "no owner terms") {
		t.Errorf("no terms: %v %q", ok, note)
	}
}
