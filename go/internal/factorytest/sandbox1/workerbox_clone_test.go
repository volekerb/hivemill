package sandbox1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/sandbox"
)

// Port of tests/test_workerbox_clone.py (CloneProfileTests 13 cases + FactoryDoneTests 1 case = 14; see
// ported_sandbox1.txt). The cwd is a standalone `git clone --shared` of a fake <factory> (alternates, origin = a local
// bare repo over file://). The Python probe script becomes a one-line sh "claude" that execs its arguments, so each
// check runs one command through the real shim -> stage 1 chain and reads its exit code and output; errno names
// (ENOENT, EROFS) become the strerror text. The shim, the git credential helper and stage 1 are the test binary
// itself (sandbox.Main, see main_test.go), copied under /var/tmp because /tmp is a hide tree inside the box.
// adapted: `python3 -c print(1)` of the qa case is `sh -c 'echo 1'` (no python needed).
// FactoryDoneTests drives bin/factory (Python) inside the box, so it calls NeedPython.

const (
	tbTask   = "G-001-T-0001"
	tbBranch = "factory/T-0001"
	tbOAuth  = "oauth-A1b2A1b2A1b2A1b2A1b2A1b2A1b2A1b2"
	tbGhRO   = "ghro-C3d4C3d4C3d4C3d4C3d4C3d4C3d4C3d4"
	tbGhRW   = "ghrw-E5f6E5f6E5f6E5f6E5f6E5f6E5f6E5f6"
	tbSlug   = "volekerb/hivemill"
)

var tbIdent = &[2]string{"Test Owner", "owner@example.invalid"}

// tbCopySelf puts a copy (or hard link) of the running test binary at dst, mode 0755.
func tbCopySelf(t testing.TB, dst string) {
	t.Helper()
	if err := os.Link(os.Args[0], dst); err == nil {
		return
	}
	in, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy the test binary: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("copy the test binary: %v", err)
	}
}

// tbWorld is a fake home with tokens and a pass-through "claude", around a given fake <factory>.
type tbWorld struct {
	t               *testing.T
	home, fh, run   string
	claude, selfBin string
}

func tbNewWorld(t *testing.T, base, home, fh string) *tbWorld {
	t.Helper()
	w := &tbWorld{t: t, home: home, fh: fh, run: filepath.Join(base, "run"), selfBin: filepath.Join(base, "factory-self")}
	if err := os.MkdirAll(w.run, 0o755); err != nil {
		t.Fatal(err)
	}
	tbCopySelf(t, w.selfBin)
	real := home + "/.local/share/claude/versions/1"
	tbWrite(t, real, "#!/bin/sh\nexec \"$@\"\n", 0o755)
	w.claude = home + "/.local/bin/claude"
	if err := os.MkdirAll(filepath.Dir(w.claude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, w.claude); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"worker-oauth-token": tbOAuth, "gh-worker-ro-token": tbGhRO, "gh-worker-rw-token": tbGhRW} {
		tbWrite(t, home+"/.config/factory/"+name, tok+"\n", 0o600)
	}
	return w
}

func (w *tbWorld) prepare(agent, role, cwd string, rwCwd bool) (string, error) {
	return sandbox.PrepareAgent(sandbox.PrepareAgentArgs{Agent: agent, Role: role, Cwd: cwd, FactoryHome: w.fh, DBDir: w.fh + "/db",
		RealClaude: w.claude, RwCwd: rwCwd, Home: w.home, Runtime: w.run, Identity: tbIdent, GhSlugs: []string{tbSlug}, Self: w.selfBin})
}

func (w *tbWorld) mustPrepare(agent, role, cwd string, rwCwd bool) string {
	w.t.Helper()
	d, err := w.prepare(agent, role, cwd, rwCwd)
	if err != nil {
		w.t.Fatalf("prepare %s: %v", agent, err)
	}
	return d
}

func (w *tbWorld) callerEnv(agent, role string) []string {
	return []string{"PATH=" + w.home + "/.local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TERM=xterm", "LC_ALL=C.UTF-8",
		"FACTORY_AGENT=" + agent, "FACTORY_ROLE=" + role, "FACTORY_HOME=" + w.fh}
}

// shim runs the generated bin/claude with args (the command the probe claude execs); the shim itself must succeed
// at starting (the command's own exit code is the result).
func (w *tbWorld) shim(agentDir, agent, role, cwd string, args ...string) (int, string, string) {
	w.t.Helper()
	cmd := exec.Command(agentDir+"/bin/claude", args...)
	cmd.Dir = cwd
	cmd.Env = w.callerEnv(agent, role)
	return tbRun(w.t, cmd)
}

type tbClones struct {
	*tbWorld
	base, bare, url, own, other, qa, d, factoryOnly string
}

func (c *tbClones) makeClone(name, branch string) string {
	c.t.Helper()
	wt := c.fh + "/worktrees/" + name
	tbGit(c.t, c.base, "-c", "protocol.file.allow=always", "clone", "-q", "--shared", "--no-checkout", c.fh, wt)
	tbGit(c.t, wt, "remote", "set-url", "origin", c.url)
	tbGit(c.t, wt, "fetch", "-q", "origin")
	tbGit(c.t, wt, "checkout", "-q", "-b", branch, "origin/main")
	return wt
}

// pushToOrigin is a commit on top of origin's main made by someone else, pushed to ref of the bare repo.
func (c *tbClones) pushToOrigin(name, text, ref string) string {
	c.t.Helper()
	seed := c.base + "/seed-" + name
	tbGit(c.t, c.base, "-c", "protocol.file.allow=always", "clone", "-q", c.url, seed)
	tbWrite(c.t, seed+"/"+name, text, 0o644)
	tbGit(c.t, seed, "add", name)
	tbGit(c.t, seed, "commit", "-q", "-m", name)
	tbGit(c.t, seed, "push", "-q", "origin", "HEAD:"+ref)
	return tbGit(c.t, seed, "rev-parse", "HEAD")
}

func tbNewClones(t *testing.T) *tbClones {
	t.Helper()
	if fi, err := os.Stat("/var/tmp"); err != nil || !fi.IsDir() {
		t.Skip("/var/tmp is not usable: the fixture must not sit below /tmp or /dev/shm")
	}
	base, err := os.MkdirTemp("/var/tmp", "zz-test-wbclone-")
	if err != nil {
		t.Skipf("cannot create a fixture under /var/tmp: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Errorf("remove %s: %v", base, err)
		}
	})
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	fh := base + "/home/factory"
	c := &tbClones{base: base, bare: base + "/origin.git"}
	c.url = "file://" + c.bare
	tbWrite(t, fh+"/tests/stubs/gh", "#!/bin/sh\nif [ -n \"$GH_TOKEN\" ]; then echo 'GH_TOKEN set'; else echo 'GH_TOKEN unset'; fi\n", 0o755)
	tbWrite(t, fh+"/README", "factory\n", 0o644)
	if err := os.Mkdir(fh+"/db", 0o755); err != nil {
		t.Fatal(err)
	}
	tbGit(t, fh, "init", "-q", "-b", "main")
	tbGit(t, fh, "add", "README", "tests/stubs/gh")
	tbGit(t, fh, "commit", "-q", "-m", "base")
	tbGit(t, base, "init", "-q", "--bare", "-b", "main", c.bare)
	tbGit(t, fh, "remote", "add", "origin", c.url)
	tbGit(t, fh, "push", "-q", "-u", "origin", "main")
	c.tbWorld = &tbWorld{t: t, fh: fh}
	c.own = c.makeClone(tbTask, tbBranch)
	c.other = c.makeClone("G-001-T-0002", "factory/T-0002")
	c.qa = c.makeClone(tbTask+"-qa", tbBranch+"-qa") // T-1544: qa's own clone, sibling of the coder clone
	// an object only the shared <factory>/.git has: the clones reach it through alternates alone
	tbWrite(t, fh+"/only-in-factory", "alternates\n", 0o644)
	tbGit(t, fh, "add", "only-in-factory")
	tbGit(t, fh, "commit", "-q", "-m", "factory-only")
	c.factoryOnly = tbGit(t, fh, "rev-parse", "HEAD")
	c.tbWorld = tbNewWorld(t, base, base+"/home", fh)
	c.d = c.mustPrepare("coder-t-1", "coder", c.own, true)
	return c
}

// sh runs one command inside as the coder in its clone.
func (c *tbClones) sh(args ...string) (int, string, string) {
	c.t.Helper()
	return c.shim(c.d, "coder-t-1", "coder", c.own, args...)
}

func (c *tbClones) ok(args ...string) string {
	c.t.Helper()
	rc, out, err := c.sh(args...)
	if rc != 0 {
		c.t.Fatalf("%v: rc=%d\n%s\n%s", args, rc, out, err)
	}
	return out
}

func (c *tbClones) outside(args ...string) string { c.t.Helper(); return tbGit(c.t, c.own, args...) }

// tbENOENT is whether an sh/ls message names a missing path (dash says "nonexistent" for a missing directory).
func tbENOENT(se string) bool {
	return strings.Contains(se, "No such file") || strings.Contains(se, "nonexistent")
}

func tbSha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// write tries `echo x > path` inside; it returns the exit code and stderr.
func (c *tbClones) writeAs(d, agent, role, cwd, path string) (int, string) {
	c.t.Helper()
	rc, _, se := c.shim(d, agent, role, cwd, "sh", "-c", `echo x > "$1"`, "sh", path)
	return rc, se
}

func tbNeedClones(t *testing.T) *tbClones {
	t.Helper()
	tbNeedUserns(t)
	return tbNewClones(t)
}

func TestWorkerboxCloneGitStatusLogDiffAndCommitInside(t *testing.T) {
	c := tbNeedClones(t)
	tbEq(t, "status", c.ok("git", "status", "--porcelain"), "")
	tbContains(t, "log", c.ok("git", "log", "--oneline"), "base")
	tbWrite(t, c.own+"/README", "changed\n", 0o644)
	tbContains(t, "diff", c.ok("git", "diff", "--stat"), "README")
	c.ok("git", "commit", "-q", "-am", "change")
	tbEq(t, "log -1", c.ok("git", "log", "-1", "--format=%s"), "change\n")
	tbEq(t, "outside log", c.outside("log", "-1", "--format=%s"), "change") // the commit is in the clone on the host
	tbEq(t, "author", c.outside("log", "-1", "--format=%an <%ae>"), "Test Owner <owner@example.invalid>")
}

func TestWorkerboxCloneFetchAndMergeOriginMainAgainstALocalBareOrigin(t *testing.T) {
	c := tbNeedClones(t)
	c.ok("git", "commit", "-q", "--allow-empty", "-m", "local work")
	n := c.pushToOrigin("upstream", "from origin\n", "refs/heads/main")
	c.ok("git", "fetch", "origin")
	tbEq(t, "origin/main", c.outside("rev-parse", "origin/main"), n)
	c.ok("git", "merge", "--no-edit", "origin/main")
	tbEq(t, "upstream", tbRead(t, c.own+"/upstream"), "from origin\n")
	rc, _, _ := c.sh("git", "merge-base", "--is-ancestor", "origin/main", "HEAD")
	tbEq(t, "merge-base rc", rc, 0)
}

func TestWorkerboxClonePushTaskBranchLandsInTheBareRepo(t *testing.T) {
	c := tbNeedClones(t)
	c.ok("git", "commit", "-q", "--allow-empty", "-m", "work")
	c.ok("git", "push", "-u", "origin", "HEAD")
	head := c.outside("rev-parse", "HEAD")
	tbEq(t, "bare branch", tbGit(t, c.bare, "rev-parse", "refs/heads/"+tbBranch), head)
	tbEq(t, "origin branch", c.outside("rev-parse", "origin/"+tbBranch), head)
}

func TestWorkerboxCloneCredentialHelperReturnsXAccessTokenAndTheGhToken(t *testing.T) {
	c := tbNeedClones(t)
	helper, err := sandbox.WorkerGhHelper(c.selfBin, []string{tbSlug})
	if err != nil {
		t.Fatal(err)
	}
	tbEq(t, "helper", strings.TrimSpace(c.ok("git", "config", "--global", "--get", "credential.https://github.com.helper")), helper)
	out := c.ok("sh", "-c", `printf 'protocol=https\nhost=github.com\npath=%s\n\n' "$1" | GIT_TERMINAL_PROMPT=0 git credential fill; printf 'sha=%s\n' "$(printf %s "$GH_TOKEN" | sha256sum | cut -d' ' -f1)"`, "sh", tbSlug)
	kv := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			kv[k] = v
		}
	}
	tbEq(t, "username", kv["username"], "x-access-token")
	tbEq(t, "password is the coder rw token", kv["password"], tbGhRW)
	tbEq(t, "GH_TOKEN sha", kv["sha"], tbSha(tbGhRW))
}

func TestWorkerboxCloneCredentialHelperGivesNoPasswordForAnotherRepo(t *testing.T) {
	c := tbNeedClones(t)
	for _, path := range []string{"other/repo", "volekerb/hivemill-evil", "other/repo.git"} {
		rc, out, _ := c.sh("sh", "-c", `printf 'protocol=https\nhost=github.com\npath=%s\n\n' "$1" | GIT_TERMINAL_PROMPT=0 git credential fill`, "sh", path)
		if rc == 0 || strings.Contains(out, "password=") {
			t.Fatalf("%s: rc=%d out=%q, want a failure and no password", path, rc, out)
		}
	}
}

func TestWorkerboxCloneObjectsOnlyInTheFactoryGitDirAreReadableThroughAlternates(t *testing.T) {
	c := tbNeedClones(t)
	sha := c.factoryOnly
	if tbExists(c.own + "/.git/objects/" + sha[:2] + "/" + sha[2:]) {
		t.Fatal("the object is in the clone's own store")
	}
	rc, _, _ := c.sh("git", "cat-file", "-e", sha)
	tbEq(t, "cat-file -e rc", rc, 0)
	_, out, _ := c.sh("git", "cat-file", "-p", sha+":only-in-factory")
	tbEq(t, "content", out, "alternates\n")
}

func TestWorkerboxCloneQaFetchesAPrRefChecksItOutDetachedAndRunsACommandInItsOwnClone(t *testing.T) {
	c := tbNeedClones(t)
	pr := c.pushToOrigin("pr-change", "from the PR\n", "refs/pull/1/head")
	d := c.mustPrepare("qa-t-1", "qa", c.qa, true)
	run := func(args ...string) (int, string) {
		rc, out, se := c.shim(d, "qa-t-1", "qa", c.qa, args...)
		if rc != 0 {
			t.Fatalf("%v: rc=%d\n%s\n%s", args, rc, out, se)
		}
		return rc, out
	}
	run("git", "fetch", "origin", "refs/pull/1/head")
	run("git", "checkout", "-q", "--detach", "FETCH_HEAD")
	_, out := run("sh", "-c", "echo 1")
	tbEq(t, "command output", out, "1\n")
	tbEq(t, "qa HEAD", tbGit(t, c.qa, "rev-parse", "HEAD"), pr)
	cmd := exec.Command("git", "symbolic-ref", "-q", "HEAD")
	cmd.Dir = c.qa
	if code, _, _ := tbRun(t, cmd); code != 1 { // detached
		t.Fatalf("symbolic-ref exit %d, want 1 (detached)", code)
	}
	if c.outside("rev-parse", "HEAD") == pr {
		t.Fatal("the coder clone was touched")
	}
}

func TestWorkerboxCloneQaCannotWriteTheCoderCloneButWritesItsOwn(t *testing.T) {
	// T-1544: the read-only-token class has no access to the clone a read-write role works in.
	c := tbNeedClones(t)
	d := c.mustPrepare("qa-t-2", "qa", c.qa, true)
	if err := os.MkdirAll(c.own+"/.git/hooks", 0o755); err != nil {
		t.Fatal(err)
	}
	cfgBefore := tbRead(t, c.own+"/.git/config")
	for _, p := range []string{c.own + "/.git/hooks/pre-commit", c.own + "/.git/config", c.own + "/newfile"} {
		rc, se := c.writeAs(d, "qa-t-2", "qa", c.qa, p)
		if rc == 0 || !tbENOENT(se) { // worktrees/ is masked: the coder clone is not there
			t.Fatalf("write %s: rc=%d stderr=%q, want ENOENT", p, rc, se)
		}
	}
	rc, _, se := c.shim(d, "qa-t-2", "qa", c.qa, "ls", c.own)
	if rc == 0 || !tbENOENT(se) {
		t.Fatalf("listdir: rc=%d stderr=%q, want ENOENT", rc, se)
	}
	for _, p := range []string{c.qa + "/.git/hooks/x", c.qa + "/newfile"} {
		if rc, se := c.writeAs(d, "qa-t-2", "qa", c.qa, p); rc != 0 {
			t.Fatalf("write %s: rc=%d %s", p, rc, se)
		}
	}
	if tbExists(c.own+"/.git/hooks/pre-commit") || tbExists(c.own+"/newfile") {
		t.Fatal("a write reached the coder clone")
	}
	tbEq(t, "coder .git/config", tbRead(t, c.own+"/.git/config"), cfgBefore)
	if !tbExists(c.qa+"/.git/hooks/x") || !tbExists(c.qa+"/newfile") {
		t.Fatal("qa's own writes did not land")
	}
}

func TestWorkerboxCloneReviewerReadsTheCoderCloneAndCannotWriteIt(t *testing.T) {
	c := tbNeedClones(t)
	d := c.mustPrepare("reviewer-t-2", "reviewer", c.own, false)
	rc, out, se := c.shim(d, "reviewer-t-2", "reviewer", c.own, "ls", c.own)
	if rc != 0 || !strings.Contains(out, "README") {
		t.Fatalf("listdir: rc=%d out=%q err=%q", rc, out, se)
	}
	for _, p := range []string{c.own + "/.git/hooks/x", c.own + "/newfile"} {
		rc, se := c.writeAs(d, "reviewer-t-2", "reviewer", c.own, p)
		if rc == 0 || !strings.Contains(se, "Read-only file system") {
			t.Fatalf("write %s: rc=%d stderr=%q, want EROFS", p, rc, se)
		}
	}
	if tbExists(c.own + "/.git/hooks/x") {
		t.Fatal("a write reached the clone")
	}
}

func TestWorkerboxCloneGhStubInsideReportsGhTokenSet(t *testing.T) {
	c := tbNeedClones(t)
	stub := c.fh + "/tests/stubs/gh"
	tbEq(t, "inside", c.ok(stub), "GH_TOKEN set\n")
	cmd := exec.Command(stub)
	cmd.Env = c.callerEnv("coder-t-1", "coder")
	_, out, _ := tbRun(t, cmd)
	tbEq(t, "outside", out, "GH_TOKEN unset\n") // only the shim sets it
}

func TestWorkerboxCloneSharedGitDirIsReadOnlyInside(t *testing.T) {
	c := tbNeedClones(t)
	g := c.fh + "/.git"
	for _, p := range []string{g + "/config", g + "/hooks/x", g + "/info/attributes", g + "/refs/heads/planted", c.fh + "/README"} {
		rc, se := c.writeAs(c.d, "coder-t-1", "coder", c.own, p)
		if rc == 0 || !strings.Contains(se, "Read-only file system") {
			t.Fatalf("write %s: rc=%d stderr=%q, want EROFS", p, rc, se)
		}
	}
	if tbExists(g+"/refs/heads/planted") || tbExists(g+"/hooks/x") {
		t.Fatal("a write reached the shared git dir")
	}
}

func TestWorkerboxCloneAnotherTaskDirIsNotListed(t *testing.T) {
	c := tbNeedClones(t)
	rc, out, se := c.sh("ls", c.fh+"/worktrees")
	if rc != 0 || strings.Fields(out)[0] != tbTask || len(strings.Fields(out)) != 1 {
		t.Fatalf("worktrees listing: rc=%d out=%q err=%q, want only %s", rc, out, se, tbTask)
	}
	rc, _, se = c.sh("ls", c.other)
	if rc == 0 || !tbENOENT(se) {
		t.Fatalf("other task dir: rc=%d stderr=%q, want ENOENT", rc, se)
	}
}

// tbShimNS is the "ns" config baked into <agent dir>/bin/claude.
func tbShimNS(t *testing.T, agentDir string) *sandbox.Cfg {
	t.Helper()
	text := tbRead(t, agentDir+"/bin/claude")
	rest := strings.SplitN(text, "worker-shim ", 2)
	if len(rest) != 2 {
		t.Fatalf("no worker-shim in %q", text)
	}
	js := strings.TrimSuffix(strings.TrimPrefix(rest[1], "'"), "' \"$@\"\n")
	var sc sandbox.ShimCfg
	if err := json.Unmarshal([]byte(js), &sc); err != nil {
		t.Fatalf("shim cfg: %v", err)
	}
	return sc.NS
}

func TestWorkerboxCloneTheCloneIsTheOnlyRwPathBeyondDbAndClaudeDir(t *testing.T) {
	// The gate is not weakened: coder/qa get exactly their clone writable, reviewer none. No namespace needed.
	c := tbNewClones(t)
	for _, tc := range []struct {
		role string
		rw   bool
	}{{"coder", true}, {"qa", true}, {"reviewer", false}} {
		ns := tbShimNS(t, c.mustPrepare(tc.role+"-t-9", tc.role, c.own, tc.rw))
		want := []string{c.fh + "/db", c.home + "/.local/state/factory/workers/" + tc.role + "-t-9/claude"}
		if tc.rw {
			want = append(want, c.own)
		}
		if !reflect.DeepEqual(ns.Rw, want) {
			t.Fatalf("%s: rw = %v, want %v", tc.role, ns.Rw, want)
		}
		inRo := func(p string) bool {
			for _, r := range ns.Ro {
				if r == p {
					return true
				}
			}
			return false
		}
		if !inRo(c.fh) {
			t.Fatalf("%s: %s is not read-only: %v", tc.role, c.fh, ns.Ro)
		}
		if inRo(c.own) != !tc.rw {
			t.Fatalf("%s: clone in ro = %v, want %v", tc.role, inRo(c.own), !tc.rw)
		}
	}
}

// FactoryDoneTests: `factory done` of a coder inside the sandbox, on a clone, in a temp FACTORY_HOME.
func TestWorkerboxCloneCoderDoneIsRefusedWithAnUnpushedCommitAndAcceptedAfterThePush(t *testing.T) {
	tbNeedUserns(t)
	base, err := os.MkdirTemp("/var/tmp", "zz-test-wbdone-") // /tmp is a hide tree: the home must live elsewhere
	if err != nil {
		t.Skipf("cannot create a fixture under /var/tmp: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Errorf("remove %s: %v", base, err)
		}
	})
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", base)
	e := ft.New(t)
	e.NeedPython()
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	h := e.Home
	d := e.RawDB()
	if _, err := d.Exec("INSERT INTO projects(id,name,repo_path,created) VALUES('zz-test-proj','zz-test-proj',?,'t')", h); err != nil {
		t.Fatal(err)
	}
	d.Close()
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	e.OKWith(ft.Opts{Agent: "zz-test-director"}, "config", "task_clones", "1")
	e.SeedGoal("G-001", "zz-test-proj", "zz-test clone goal")
	e.SeedTask(ft.Task{Status: "in_progress"})
	bare := filepath.Join(base, "origin.git") // outside the read-only factory home
	tbGit(t, h, "init", "-q", "--bare", "-b", "main", bare)
	tbGit(t, h, "remote", "add", "origin", bare) // a plain path: the factory refuses a file:// url in git config
	tbGit(t, h, "commit", "-q", "--allow-empty", "-m", "base")
	tbGit(t, h, "push", "-q", "-u", "origin", "main")
	wt := h + "/worktrees/" + tbTask // the standalone clone task_clones=1 makes
	tbGit(t, base, "clone", "-q", bare, wt)
	tbGit(t, wt, "checkout", "-q", "-b", tbBranch)
	d = e.RawDB() // what prepare_worktree records for the task
	if _, err := d.Exec("UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", wt, tbBranch); err != nil {
		t.Fatal(err)
	}
	d.Close()
	// the repo checkout is under the hidden home: the sandbox runs a copy of what bin/factory imports
	if err := os.MkdirAll(h+"/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"factory", "factory_cli.py"} {
		tbWrite(t, h+"/bin/"+f, tbRead(t, repo+"/bin/"+f), 0o755)
	}
	for _, pkg := range []string{"scout", "web"} {
		if out, err := exec.Command("cp", "-a", repo+"/"+pkg, h+"/"+pkg).CombinedOutput(); err != nil {
			t.Fatalf("copy %s: %v\n%s", pkg, err, out)
		}
	}
	w := tbNewWorld(t, base, e.UserHome, h)
	dir := w.mustPrepare("zz-test-coder", "coder", wt, true)
	inside := func(args ...string) (int, string, string) {
		return w.shim(dir, "zz-test-coder", "coder", wt, args...)
	}
	done := []string{"python3", h + "/bin/factory", "done", "finished T-0001"}
	if rc, _, se := inside("git", "commit", "-q", "--allow-empty", "-m", "work"); rc != 0 {
		t.Fatalf("commit: rc=%d %s", rc, se)
	}
	rc, out, se := inside(done...)
	if rc != 3 {
		t.Fatalf("done: rc=%d, want 3\n%s\n%s", rc, out, se)
	}
	tbContains(t, "stderr", se, "is not on origin/")
	tbEq(t, "agent status", e.SQL("SELECT status FROM agents WHERE name='zz-test-coder'")[0][0], "live")
	branch := tbGit(t, wt, "rev-parse", "--abbrev-ref", "HEAD")
	if rc, _, se := inside("git", "push", "-q", "-u", "origin", "HEAD"); rc != 0 {
		t.Fatalf("push: rc=%d %s", rc, se)
	}
	tbEq(t, "bare branch", tbGit(t, bare, "rev-parse", "refs/heads/"+branch), tbGit(t, wt, "rev-parse", "HEAD"))
	if rc, out, se := inside(done...); rc != 0 {
		t.Fatalf("done after push: rc=%d\n%s\n%s", rc, out, se)
	}
	tbEq(t, "agent status", e.SQL("SELECT status FROM agents WHERE name='zz-test-coder'")[0][0], "done")
}
