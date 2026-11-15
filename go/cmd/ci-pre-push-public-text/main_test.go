package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // the driver of the config DB the tests create

	"factory/go/internal/guard"
)

const zeros = "0000000000000000000000000000000000000000"

// makeHome is a temp factory home: db/factory.db with the two config rows, a terms file (synthetic terms only) and
// ci/scrub_allowlist.txt (the Go tool looks for it under the home).
func makeHome(t *testing.T, publicRepos string, withTerms bool) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, "ci", "scrub_allowlist.txt"), "")
	write(t, filepath.Join(home, "terms.txt"), syntheticTerms)
	db, err := sql.Open("sqlite", filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if _, err := db.Exec("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{"public_text_repos": publicRepos}
	if withTerms {
		rows["scrub_terms_file"] = filepath.Join(home, "terms.txt")
	}
	for k, v := range rows {
		if _, err := db.Exec("INSERT INTO config VALUES (?, ?)", k, v); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// pushRepo has a base commit (the "origin/main" the hook merge-bases against) and a feature commit.
func pushRepo(t *testing.T, e []string, featureFile, featureMsg string) (repo, base, head string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, e, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "README.md"), "base\n")
	git(t, repo, e, "add", "-A")
	git(t, repo, e, "commit", "-q", "-m", "base")
	base = strings.TrimSpace(git(t, repo, e, "rev-parse", "HEAD"))
	git(t, repo, e, "update-ref", "refs/remotes/origin/main", base)
	git(t, repo, e, "checkout", "-q", "-b", "feature")
	write(t, filepath.Join(repo, "f.txt"), featureFile)
	git(t, repo, e, "add", "-A")
	git(t, repo, e, "commit", "-q", "-m", featureMsg)
	head = strings.TrimSpace(git(t, repo, e, "rev-parse", "HEAD"))
	return repo, base, head
}

func TestPrePushParity(t *testing.T) {
	py, bin := needPython(t, "ci/pre_push_public_text.py"), goBin(t, "ci-pre-push-public-text")
	e := env(t)
	public := "https://github.com/acme/widgets.git"
	clean := "just words\nversion 1.2.3\n"
	blocked := "owner Zorbulonic and host db01\x2ecorp\x2elan\n"
	crepo, _, chead := pushRepo(t, e, clean, "clean change")
	brepo, bbase, bhead := pushRepo(t, e, blocked, "change mentioning qux")
	refs := func(sha, remoteSha string) string {
		return "refs/heads/feature " + sha + " refs/heads/feature " + remoteSha + "\n"
	}

	cases := []struct {
		name     string
		repo     string
		repos    string
		terms    bool
		url      string
		stdin    string
		wantExit int
	}{
		{"clean push to a public repo", crepo, "acme/widgets", true, public, refs(chead, zeros), 0},
		{"blocked push to a public repo", brepo, "acme/widgets", true, public, refs(bhead, zeros), 1},
		{"blocked push, remote sha known", brepo, "acme/widgets", true, public, refs(bhead, bbase), 1},
		{"blocked push to another repo", brepo, "acme/widgets", true, "git\x40github.com:other/thing.git", refs(bhead, zeros), 0},
		{"no public repos configured", brepo, "", true, public, refs(bhead, zeros), 0},
		{"blocked ref name", brepo, "acme/widgets", true, public, "refs/heads/feature " + chead + " refs/heads/Zorbulonic-x " + zeros + "\n", 1},
		{"delete publishes nothing", brepo, "acme/widgets", true, public, "(delete) " + zeros + " refs/heads/old " + chead + "\n", 0},
		{"short and blank lines", brepo, "acme/widgets", true, public, "\n   \nonly three fields\n", 0},
		{"no terms file configured", crepo, "acme/widgets", false, public, refs(chead, zeros), 1},
		{"unparsable url with a public repo", crepo, "acme/widgets", true, "nonsense", refs(chead, zeros), 0},
	}
	for _, c := range cases {
		home := makeHome(t, c.repos, c.terms)
		args := []string{"--home", home, "origin", c.url}
		p := run(t, c.repo, e, c.stdin, append([]string{"python3", "-I", py}, args...)...)
		g := run(t, c.repo, e, c.stdin, append([]string{bin}, args...)...)
		if p.code != c.wantExit || g.code != c.wantExit {
			t.Errorf("%s: want exit %d, python %d (%s), go %d (%s)", c.name, c.wantExit, p.code, p.err, g.code, g.err)
		}
		if p.stdout != g.stdout || strings.Contains(g.err, "Zorbulonic") {
			t.Errorf("%s: stdout differs or leaks a term:\npython: %q\ngo: %q", c.name, p.stdout, g.stdout)
		}
		// the refusal line names the pattern ids, never the matched text; compare it up to the process-specific part
		if c.wantExit == 1 && c.terms && strings.Contains(c.name, "blocked") && p.err != g.err {
			t.Errorf("%s: refusal differs:\npython: %q\ngo: %q", c.name, p.err, g.err)
		}
	}
}

func TestPrePushParityErrors(t *testing.T) {
	py, bin := needPython(t, "ci/pre_push_public_text.py"), goBin(t, "ci-pre-push-public-text")
	e := env(t)
	repo, _, head := pushRepo(t, e, "x\n", "c")
	for _, args := range [][]string{{}, {"--home"}, {"--home", "h", "origin"}, {"origin", "url", "x", "y"}} {
		p := run(t, repo, e, "", append([]string{"python3", "-I", py}, args...)...)
		g := run(t, repo, e, "", append([]string{bin}, args...)...)
		if p.code != 1 || g.code != 1 || p.err != g.err {
			t.Errorf("usage %v: python %d %q, go %d %q", args, p.code, p.err, g.code, g.err)
		}
	}
	// a missing DB refuses the push (exit 1), as does a home without ci/ and a file that is no database
	missing := t.TempDir()
	for _, home := range []string{missing, func() string {
		h := t.TempDir()
		write(t, filepath.Join(h, "db", "factory.db"), "not a database")
		return h
	}()} {
		stdin := "refs/heads/f " + head + " refs/heads/f " + zeros + "\n"
		p := run(t, repo, e, stdin, "python3", "-I", py, "--home", home, "origin", "https://github.com/acme/widgets")
		g := run(t, repo, e, stdin, bin, "--home", home, "origin", "https://github.com/acme/widgets")
		if p.code != 1 || g.code != 1 || !strings.Contains(g.err, "factory config unreadable") {
			t.Errorf("home %s: python %d %q, go %d %q", home, p.code, p.err, g.code, g.err)
		}
	}
	// no origin/main and no remote sha: nothing to check the push against
	norepo := t.TempDir()
	git(t, norepo, e, "init", "-q", "-b", "main")
	write(t, filepath.Join(norepo, "a"), "a\n")
	git(t, norepo, e, "add", "-A")
	git(t, norepo, e, "commit", "-q", "-m", "c")
	sha := strings.TrimSpace(git(t, norepo, e, "rev-parse", "HEAD"))
	home := makeHome(t, "acme/widgets", true)
	stdin := "refs/heads/main " + sha + " refs/heads/main " + zeros + "\n"
	p := run(t, norepo, e, stdin, "python3", "-I", py, "--home", home, "origin", "https://github.com/acme/widgets")
	g := run(t, norepo, e, stdin, bin, "--home", home, "origin", "https://github.com/acme/widgets")
	if p.code != 1 || g.code != 1 || p.err != g.err || !strings.Contains(g.err, "no base to check the push against") {
		t.Errorf("no base: python %d %q, go %d %q", p.code, p.err, g.code, g.err)
	}
}

// slugs are remote URL shapes: https, scp-style, local paths, queries, trailing slashes, odd leftovers.
var slugs = []string{
	"https://github.com/Acme/Widgets.git", "https://github.com/acme/widgets", "git\x40github.com:acme/widgets.git",
	"ssh://git\x40github.com/acme/widgets.git/", "/srv/git/Acme/Widgets.GIT", "https://github.com/acme/widgets.git?x=1#frag",
	"nonsense", "", "a/b", "./b", "../b", "x/..", "https://host/", "https://host/only", " https://github.com/a/b \n",
	"https://github.com/a/b.git\n/", "host:path", "u\x40h:a/b.git", "a/\u0130b", "a/b.git.git", "///a///b///",
}

func TestPrePushParityRepoSlug(t *testing.T) {
	py := needPython(t, "ci/pre_push_public_text.py")
	// the Python original loads ci/public_text_guard.py by path; repo_slug itself needs only the module namespace
	code := "import importlib.util,json,sys\n" +
		"spec=importlib.util.spec_from_file_location('m',sys.argv[1]);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)\n" +
		"print(json.dumps([m.repo_slug(u) for u in json.load(sys.stdin)]))\n"
	in := "["
	for i, s := range slugs {
		if i > 0 {
			in += ","
		}
		in += jsonString(s)
	}
	in += "]"
	cmd := exec.Command("python3", "-I", "-c", code, py)
	cmd.Stdin = strings.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("python repo_slug: %v", err)
	}
	var want []string
	for _, s := range slugs {
		g, ok := guard.CIRepoSlug(s)
		if !ok {
			g = "null"
		} else {
			g = jsonString(g)
		}
		want = append(want, g)
	}
	if got, exp := strings.TrimSpace(string(out)), "["+strings.Join(want, ", ")+"]"; got != exp {
		t.Errorf("repo_slug differs:\npython: %s\ngo:     %s", got, exp)
	}
}

// jsonString is json.dumps of a string with ensure_ascii (the form Python prints).
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r < 0x20 || r > 0x7e:
			b.WriteString(`\u`)
			const hex = "0123456789abcdef"
			b.WriteByte(hex[r>>12&15])
			b.WriteByte(hex[r>>8&15])
			b.WriteByte(hex[r>>4&15])
			b.WriteByte(hex[r&15])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
