package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestRegistryPortedCommands: done, prcheck, pr and src are registered (matched by `go test -run TestRegistry`).
func TestRegistryPortedCommands(t *testing.T) {
	for _, n := range []string{"done", "prcheck", "pr", "src"} {
		if _, ok := registry[key{n, ""}]; !ok {
			t.Errorf("%s is not registered", n)
			continue
		}
		t.Logf("registered: %s", n)
	}
}

// prStub is a gh on PATH that records its argv (one line) and stdin, prints GH_REPO and GH_HOST from its environment and
// exits with the code in exit.
type prStub struct{ dir string }

func newPrStub(t *testing.T) prStub {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"$*\" >> " + dir + "/argv\ncat > " + dir + "/stdin\n" +
		"echo \"GH_REPO=${GH_REPO:-unset} GH_HOST=${GH_HOST:-unset}\"\n" +
		"[ -f " + dir + "/exit ] && { echo boom >&2; exit \"$(cat " + dir + "/exit)\"; }\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("GH_REPO", "evil/redirect")
	t.Setenv("GH_HOST", "evil.example")
	return prStub{dir}
}

func (s prStub) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}

func newPrCoder(t *testing.T) (*cliEnv, string) {
	t.Helper()
	e := newCliEnvLive(t, false)
	wt := t.TempDir()
	gitIn(t, wt, "init", "-q", "-b", "factory/t-0001")
	e.sql("UPDATE tasks SET worktree=?, branch='factory/t-0001' WHERE id='T-0001'", wt)
	e.cwd = wt
	return e, wt
}

func TestPrCreateAndReady(t *testing.T) {
	stub := newPrStub(t)
	e, _ := newPrCoder(t)
	out, errs, code := e.run("claude-coder", "## Summary\nbody\n\n", "pr", "create", "--title", "T-0001: x", "--body", "-")
	if code != 0 || out != "GH_REPO=unset GH_HOST=unset\n" || errs != "" {
		t.Fatalf("create: exit %d out %q err %q", code, out, errs)
	}
	if got := strings.TrimSpace(stub.read(t, "argv")); got != "pr create --repo acme/app --base main --head factory/t-0001 --title T-0001: x --body-file -" {
		t.Errorf("gh argv %q", got)
	}
	if got := stub.read(t, "stdin"); got != "## Summary\nbody" {
		t.Errorf("gh stdin %q (the body is read stripped, as read_text does)", got)
	}
	if d := e.str("SELECT detail FROM events WHERE action='pr-create'"); d != "acme/app factory/t-0001" {
		t.Errorf("pr-create event %q", d)
	}
	out, _, code = e.run("claude-coder", "", "pr", "ready")
	lines := strings.Split(strings.TrimSpace(stub.read(t, "argv")), "\n")
	if code != 0 || out != "GH_REPO=unset GH_HOST=unset\n" || lines[len(lines)-1] != "pr ready factory/t-0001 --repo acme/app" {
		t.Errorf("ready: exit %d out %q argv %q", code, out, lines)
	}
	if d := e.str("SELECT detail FROM events WHERE action='pr-ready'"); d != "acme/app factory/t-0001" {
		t.Errorf("pr-ready event %q", d)
	}
	// a gh failure passes its output and exit code through and records nothing
	os.WriteFile(filepath.Join(stub.dir, "exit"), []byte("4"), 0o644)
	before := e.str("SELECT count(*) FROM events WHERE action LIKE 'pr-%'")
	out, errs, code = e.run("claude-coder", "", "pr", "ready")
	if code != 4 || errs != "boom\n" || e.str("SELECT count(*) FROM events WHERE action LIKE 'pr-%'") != before {
		t.Errorf("gh failure: exit %d out %q err %q", code, out, errs)
	}
}

func TestPrCreateTitleRule(t *testing.T) {
	stub := newPrStub(t)
	e, _ := newPrCoder(t)
	for _, tc := range []struct{ arg, db, want string }{
		{"T-0001", "[SC4] status line; never idle", "T-0001: status line; never idle"},
		{"missing status", "other", "T-0001: missing status"},
		{"t-0001 - coder text", "other", "T-0001: coder text"},
		{"T-0001: T-0001", "[SC1, SC2] two tags", "T-0001: two tags"},
	} {
		e.sql("UPDATE tasks SET title=? WHERE id='T-0001'", tc.db)
		_, errs, code := e.run("claude-coder", "b\n", "pr", "create", "--title", tc.arg, "--body", "-")
		lines := strings.Split(strings.TrimSpace(stub.read(t, "argv")), "\n")
		want := "pr create --repo acme/app --base main --head factory/t-0001 --title " + tc.want + " --body-file -"
		if code != 0 || lines[len(lines)-1] != want {
			t.Errorf("%q: exit %d err %q argv %q, want %q", tc.arg, code, errs, lines[len(lines)-1], want)
		}
	}
	e.sql("UPDATE tasks SET title='[SC4]' WHERE id='T-0001'")
	before := stub.read(t, "argv")
	_, errs, code := e.run("claude-coder", "b\n", "pr", "create", "--title", "T-0001", "--body", "-")
	if code != 2 || !strings.Contains(errs, "title needs a description") || stub.read(t, "argv") != before {
		t.Errorf("empty description: exit %d err %q, gh must not run", code, errs)
	}
}

func TestPrRefusals(t *testing.T) {
	stub := newPrStub(t)
	e, wt := newPrCoder(t)
	for _, tc := range []struct {
		name, agent, stdin string
		args               []string
		code               int
		err                string
	}{
		{"non-coder", "claude-pm", "", []string{"pr", "ready"}, 3, "factory: role 'pm' (claude-pm) may not do this: `factory pr` is for coders; ask your lead\n"},
		{"owner", "owner", "", []string{"pr", "ready"}, 3, "factory: role 'owner' (owner) may not do this: `factory pr` is for coders; ask your lead\n"},
		{"body not stdin", "claude-coder", "x", []string{"pr", "create", "--title", "t", "--body", "text"}, 2, "factory: `factory pr create` takes the body on stdin only: --body - with a heredoc\n"},
		{"no body", "claude-coder", "", []string{"pr", "create", "--title", "t"}, 2, "factory: `factory pr create` takes the body on stdin only: --body - with a heredoc\n"},
		{"blank title", "claude-coder", "x", []string{"pr", "create", "--title", " ", "--body", "-"}, 2, "factory: `factory pr create` needs a non-empty --title and a body on stdin\n"},
		{"empty body", "claude-coder", "  \n", []string{"pr", "create", "--title", "t", "--body", "-"}, 2, "factory: `factory pr create` needs a non-empty --title and a body on stdin\n"},
		{"ready with args", "claude-coder", "", []string{"pr", "ready", "--title", "t"}, 2, "factory: `factory pr ready` takes no arguments: it targets the PR of your own branch\n"},
	} {
		out, errs, code := e.run(tc.agent, tc.stdin, tc.args...)
		if code != tc.code || errs != tc.err || out != "" || e.execs != 0 {
			t.Errorf("%s: exit %d out %q err %q execs %d", tc.name, code, out, errs, e.execs)
		}
	}
	e.sql("UPDATE agents SET task_id=NULL WHERE name='claude-pm'")
	e.sql("UPDATE agents SET role='coder' WHERE name='claude-pm'")
	if _, errs, code := e.run("claude-pm", "", "pr", "ready"); code != 3 || errs != "factory: claude-pm has no task: `factory pr` opens the PR of your own task; ask your lead\n" {
		t.Errorf("no task: exit %d err %q", code, errs)
	}
	gitIn(t, wt, "checkout", "-q", "-b", "other-branch")
	if _, errs, code := e.run("claude-coder", "", "pr", "ready"); code != 3 || errs != "factory: current branch is 'other-branch', not 'factory/t-0001' or the task's recorded branch "+
		"(factory/t-0001-vN, set in the task): run `factory pr` from your own task worktree\n" {
		t.Errorf("wrong branch: exit %d err %q", code, errs)
	}
	// a rebuilt clean branch is accepted only when it is the task's recorded branch
	gitIn(t, wt, "checkout", "-q", "-b", "factory/t-0001-v2")
	if _, _, code := e.run("claude-coder", "", "pr", "ready"); code != 3 {
		t.Errorf("an unrecorded -v2 branch: exit %d, want 3", code)
	}
	e.sql("UPDATE tasks SET branch='factory/t-0001-v2' WHERE id='T-0001'")
	if _, errs, code := e.run("claude-coder", "", "pr", "ready"); code != 0 {
		t.Errorf("the recorded -v2 branch: exit %d err %q", code, errs)
	}
	e.sql("UPDATE projects SET gh_repo='not a slug' WHERE id='P-1'")
	if _, errs, code := e.run("claude-coder", "", "pr", "ready"); code != 3 || errs != "factory: T-0001: its project has no valid gh_repo ('not a slug'); ask your lead to set it with `factory project update`\n" {
		t.Errorf("bad gh_repo: exit %d err %q", code, errs)
	}
	if n := strings.Count(stub.read(t, "argv"), "\n"); n != 1 {
		t.Errorf("gh ran %d times, only the accepted -v2 branch may run it", n)
	}
}

func TestPrPublicRepoHandsOver(t *testing.T) {
	stub := newPrStub(t)
	e, _ := newPrCoder(t)
	e.sql("INSERT OR REPLACE INTO config(key,value) VALUES('public_text_repos','Acme/App')")
	out, errs, code := e.run("claude-coder", "body", "pr", "create", "--title", "t", "--body", "-")
	if e.execs != 1 || out != "" || errs != "" || code != 0 || stub.read(t, "argv") != "" {
		t.Errorf("public repo: execs %d exit %d out %q err %q gh %q", e.execs, code, out, errs, stub.read(t, "argv"))
	}
	if _, _, code := e.run("claude-coder", "", "pr", "ready"); e.execs != 0 || code != 0 {
		t.Errorf("ready on a public repo has no text to check: execs %d exit %d", e.execs, code)
	}
}

func TestParsePr(t *testing.T) {
	good := map[string]prCmd{
		"create --title T --body -": {action: "create", title: strp("T"), body: strp("-")},
		"create --body=- --title=T": {action: "create", title: strp("T"), body: strp("-")},
		"ready":                     {action: "ready"},
	}
	for in, want := range good {
		got, ok := parsePr(strings.Fields(in))
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("parsePr(%q) = %v %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "merge", "create --title", "create --title --body", "create --tit T", "create --title T --title U", "--title T create", "create extra"} {
		if _, ok := parsePr(strings.Fields(in)); ok {
			t.Errorf("parsePr(%q) should hand over", in)
		}
	}
}

func TestRepoSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Acme/App": "acme/app", "https://github.com/Acme/App.git": "acme/app", "git@example.com:Acme/App.git?x=1": "acme/app",
		"/srv/git/acme/app.git/": "acme/app", "app": "", "": "", " a/b ": "a/b", "a/..": "", "https://h/x/y/z": "y/z", "a/b.GIT": "a/b",
	} {
		got, ok := repoSlug(in)
		if !ok || got != want {
			t.Errorf("repoSlug(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"a\nb/c", "é/x", "a/b\x00"} {
		if _, ok := repoSlug(in); ok {
			t.Errorf("repoSlug(%q) must hand over", in)
		}
	}
}

func FuzzParsePr(f *testing.F) {
	for _, s := range []string{"create", "ready", "--title", "--body", "-", "=", "--title=x", "\xff"} {
		f.Add(s, "t", "-")
	}
	f.Fuzz(func(t *testing.T, a, b, c string) {
		parsePr([]string{a, b, c})
		parsePr([]string{"create", a, b, c})
		repoSlug(a)
	})
}

// TestGhTimeouts: every gh call has Python's 120 s limit (shortened here); a hanging gh ends the command with a timeout
// error instead of hanging it.
func TestGhTimeouts(t *testing.T) {
	old := ghTimeout
	ghTimeout = 300 * time.Millisecond
	t.Cleanup(func() { ghTimeout = old })

	t.Run("pr ready", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexec sleep 5\n"), 0o755)
		t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
		e, _ := newPrCoder(t)
		start := time.Now()
		_, errs, code := e.run("claude-coder", "", "pr", "ready")
		if code != 1 || !strings.HasPrefix(errs, "factory: gh pr ready failed to run: Command '[") || !strings.Contains(errs, "timed out after") {
			t.Errorf("exit %d err %q", code, errs)
		}
		if time.Since(start) > 4*time.Second {
			t.Errorf("the command waited for the hanging gh")
		}
		if n := e.str("SELECT count(*) FROM events WHERE action='pr-ready'"); n != "0" {
			t.Errorf("a timed-out gh recorded %s pr-ready events", n)
		}
	})
	t.Run("prcheck CI state", func(t *testing.T) {
		r := newPrRepo(t, true)
		script := "#!/bin/sh\nlast=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
			"if [ \"$last\" = body ]; then cat " + r.stub + "/body.json; else exec sleep 5; fi\n"
		os.WriteFile(filepath.Join(r.stub, "gh"), []byte(script), 0o755)
		out, _, code := r.prcheck(t)
		if code != 1 || !strings.Contains(out, "CI is not green on the head yet (cannot read PR checks: Command '[") || !strings.Contains(out, "timed out after") {
			t.Errorf("exit %d out %q", code, out)
		}
	})
}
