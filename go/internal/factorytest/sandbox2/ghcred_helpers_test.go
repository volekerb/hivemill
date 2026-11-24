package sandbox2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/sandbox"
)

// init makes the test binary its own credential helper and gh shim: the per-agent gitconfig and bin/gh that
// sandbox.PrepareDir writes run `<self> git-credential ...` and `<self> gh-shim ...`, and self is this binary.
// No test run passes these words as its first argument, so a normal run is unaffected.
func init() {
	if len(os.Args) >= 3 && (os.Args[1] == "git-credential" || os.Args[1] == "gh-shim") {
		if code, handled := sandbox.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
			os.Exit(code)
		}
	}
}

const (
	ghToken = "ghp_" + "Zq7Lm2Zq7Lm2Zq7Lm2Zq7Lm2Zq7Lm2Zq7Lm2"
	ghSlug  = "volekerb/hivemill"
)

func ghMust(t testing.TB, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func ghWrite(t testing.TB, path, body string, mode os.FileMode) {
	t.Helper()
	ghMust(t, "write "+path, os.MkdirAll(filepath.Dir(path), 0o755))
	ghMust(t, "write "+path, os.WriteFile(path, []byte(body), mode))
	ghMust(t, "chmod "+path, os.Chmod(path, mode))
}

func ghRead(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func ghContains(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func ghNotContains(t testing.TB, what, got, bad string) {
	t.Helper()
	if strings.Contains(got, bad) {
		t.Fatalf("%s = %q, must not contain %q", what, got, bad)
	}
}

func ghEq[T comparable](t testing.TB, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// ghRes is one finished subprocess.
type ghRes struct {
	Out, Err string
	Code     int
}

// ghRun runs argv with exactly env (no inheritance), stdin and cwd; a start failure ends the test, and so does a
// run longer than 60 s (a helper that blocks on a FIFO would otherwise hang the suite).
func ghRun(t testing.TB, dir string, env []string, stdin string, argv ...string) ghRes {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	ghMust(t, "start "+argv[0], cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%v did not finish in 60 s", argv)
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%v: %v", argv, err)
	}
	return ghRes{o.String(), e.String(), code}
}

// ghSelf is the test binary, the helper and shim the generated files call.
func ghSelf(t testing.TB) string {
	t.Helper()
	p, err := os.Executable()
	ghMust(t, "os.Executable", err)
	return p
}

// ghBase is tests/test_ghcred.py Base: a temp tree with a fake HOME, a 0600 worker token file in
// ~/.config/factory and an owner gitconfig.
type ghBase struct {
	t     *testing.T
	tmp   string
	home  string
	token string
	root  string
	real  string // the fixed "real gh" path the shim runs
}

func ghNew(t *testing.T) *ghBase {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	ghMust(t, "EvalSymlinks", err)
	b := &ghBase{t: t, tmp: tmp, home: filepath.Join(tmp, "home")}
	b.token = filepath.Join(b.home, ".config", "factory", "gh-worker-rw-token")
	b.root = filepath.Join(tmp, "ghcred")
	b.real = filepath.Join(tmp, "real-gh")
	ghMust(t, "mkdir", os.MkdirAll(filepath.Dir(b.token), 0o755))
	b.writeToken(ghToken, 0o600)
	ghWrite(t, filepath.Join(b.home, ".gitconfig"), "[user]\n\tname = Test Owner\n\temail = owner@example.invalid\n", 0o644)
	return b
}

func (b *ghBase) writeToken(text string, mode os.FileMode) {
	b.t.Helper()
	ghWrite(b.t, b.token, text, mode)
}

func (b *ghBase) opts(o sandbox.PrepareDirOpts) sandbox.PrepareDirOpts {
	if o.Agent == "" {
		o.Agent = "coder-t-1"
	}
	if o.Role == "" {
		o.Role = "coder"
	}
	if o.TokenPath == "" {
		o.TokenPath = b.token
	}
	if o.Slugs == nil {
		o.Slugs = []string{ghSlug}
	}
	if o.RealGh == "" {
		o.RealGh = b.real
	}
	o.Home, o.Root = b.home, b.root
	return o
}

// prepare is Base.prepare: PrepareDir with the base defaults; fields set in o win.
func (b *ghBase) prepare(o sandbox.PrepareDirOpts) (string, error) {
	return sandbox.PrepareDir(b.opts(o))
}

func (b *ghBase) mustPrepare(o sandbox.PrepareDirOpts) string {
	b.t.Helper()
	d, err := b.prepare(o)
	ghMust(b.t, "PrepareDir", err)
	return d
}

func ghPath() string {
	if p := os.Getenv("PATH"); p != "" {
		return p
	}
	return "/usr/bin:/bin"
}

// env is Base.env: HOME, PATH and the pane env of agent dir d, then extra (KEY=VALUE).
func (b *ghBase) env(d string, extra ...string) []string {
	m := map[string]string{"HOME": b.home, "PATH": ghPath()}
	for k, v := range sandbox.PaneEnv(d, b.token, ghPath()) {
		m[k] = v
	}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// fill is `git credential fill` for host github.com and path, under the pane env of d.
func (b *ghBase) fill(d, path string, extra ...string) ghRes {
	b.t.Helper()
	in := fmt.Sprintf("protocol=https\nhost=github.com\npath=%s\n\n", path)
	return ghRun(b.t, b.tmp, b.env(d, extra...), in, "git", "credential", "fill")
}

// helper runs the Go helper directly (the binary the gitconfig names) with only the token file in its env.
func (b *ghBase) helper(stdin string, env []string, action string) ghRes {
	b.t.Helper()
	return ghRun(b.t, "", env, stdin, ghSelf(b.t), "git-credential", ghSlug, action)
}

func (b *ghBase) credReq(path string) string {
	return fmt.Sprintf("protocol=https\nhost=github.com\npath=%s\n\n", path)
}

func (b *ghBase) tokenEnv() []string { return []string{"FACTORY_GH_TOKEN_FILE=" + b.token} }

// gitOut is `git <args>` in dir under env; the test fails on a non-zero exit when check is set.
func ghGitOut(t testing.TB, dir string, env []string, check bool, args ...string) ghRes {
	t.Helper()
	r := ghRun(t, dir, env, "", append([]string{"git"}, args...)...)
	if check && r.Code != 0 {
		t.Fatalf("git %v: exit %d: %s", args, r.Code, r.Err)
	}
	return r
}

// ghTree is every regular file under dir.
func ghTree(t testing.TB, dir string) []string {
	t.Helper()
	var out []string
	ghMust(t, "walk "+dir, filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			out = append(out, p)
		}
		return nil
	}))
	return out
}

func ghMkfifo(path string, mode uint32) error { return syscall.Mkfifo(path, mode) }

// ghDrain reads r to the end, for helpers that only need the text.
func ghDrain(r io.Reader) string {
	b, _ := io.ReadAll(r)
	return string(b)
}

// ---- worker credential spawn wiring (tests/test_worker_ghcred.py GhCredCase) ----

const (
	ghRO = "zz-fake-ro-token-7d41e"
	ghRW = "zz-fake-rw-token-9a3b2"
	// ghOwnerSlug is the worker repo the spawn tests configure.
	ghOwnerSlug = "zz-owner/zz-repo"
	ghDirector  = "zz-test-director"
)

var ghRoles = []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm"}

var ghScrubbed = []string{"GIT_CONFIG_PARAMETERS", "GIT_ASKPASS", "SSH_ASKPASS", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN"}

var ghDangerous = map[string]string{
	"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "credential.helper", "GIT_CONFIG_VALUE_0": "!echo pw",
	"GIT_CONFIG_PARAMETERS": "'credential.helper=!echo pw'", "GIT_ASKPASS": "/bin/echo",
	"SSH_ASKPASS": "/bin/echo", "GH_TOKEN": "zz-owner-token", "GITHUB_TOKEN": "zz-owner-token2",
	"GH_ENTERPRISE_TOKEN": "zz-owner-token3",
}

// ghW is GhCredCase: a harness home with fake worker tokens, a gh that logs GH_TOKEN|GH_CONFIG_DIR, and the
// worker_gh_repos config set. Every test of it drives spawn (or judge, clean, tick), which the Go CLI hands to Python.
type ghW struct {
	*ft.Env
	t      *testing.T
	tdir   string // ~/.config/factory of the CLI's HOME
	envlog string // every gh call: GH_TOKEN|GH_CONFIG_DIR
}

func ghNewW(t *testing.T) *ghW {
	t.Helper()
	e := ft.New(t)
	e.NeedPython()
	e.HerdrStateful()
	// the Python harness pins the TUI backend (both stubs are on PATH and auto prefers hc)
	d := e.RawDB()
	_, err := d.Exec("UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	d.Close()
	ghMust(t, "pin terminal backend", err)
	w := &ghW{Env: e, t: t, tdir: filepath.Join(e.UserHome, ".config", "factory"), envlog: filepath.Join(e.Root, "gh-env.log")}
	ghMust(t, "mkdir", os.MkdirAll(w.tdir, 0o755))
	for name, tok := range map[string]string{"gh-worker-ro-token": ghRO, "gh-worker-rw-token": ghRW} {
		ghWrite(t, filepath.Join(w.tdir, name), tok+"\n", 0o600)
	}
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-lead")
	e.SeedAgent(ft.AgentRow{Name: ghDirector, Role: "director"})
	w.useGh()
	e.OK("config", "worker_gh_repos", ghOwnerSlug)
	return w
}

// useGh replaces the harness gh with a wrapper that logs the token env and then runs the Python gh stub (which keeps
// its state in gh.json, like tests/stubs/gh).
func (w *ghW) useGh() {
	w.t.Helper()
	repo, err := ft.RepoRoot()
	ghMust(w.t, "RepoRoot", err)
	py, err := exec.LookPath("python3")
	ghMust(w.t, "python3", err)
	gh := filepath.Join(w.StubBin, "gh")
	_ = os.Remove(gh)
	ghWrite(w.t, gh, "#!/bin/sh\nprintf \"%s|%s\\n\" \"$GH_TOKEN\" \"$GH_CONFIG_DIR\" >> "+w.envlog+"\n"+
		"exec "+py+" "+filepath.Join(repo, "tests", "stubs", "gh")+" \"$@\"\n", 0o755)
}

// setGh merges kw into the gh stub state file (harness set_gh).
func (w *ghW) setGh(kw map[string]any) {
	w.t.Helper()
	path := filepath.Join(w.Root, "gh.json")
	st := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		ghMust(w.t, "parse gh state", json.Unmarshal(b, &st))
	}
	for k, v := range kw {
		st[k] = v
	}
	b, err := json.Marshal(st)
	ghMust(w.t, "marshal gh state", err)
	ghMust(w.t, "write gh state", os.WriteFile(path, b, 0o644))
}

// exec runs one write statement on the home's database.
func (w *ghW) exec(q string, args ...any) {
	w.t.Helper()
	d := w.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

func (w *ghW) flag(roles string) {
	w.t.Helper()
	w.OKWith(ft.Opts{Agent: ghDirector}, "config", "worker_gh_roles", roles)
}

// spawn is GhCredCase.spawn: role zz-<role><tag> in a fresh cwd, extra environment on top.
func (w *ghW) spawn(role string, extra map[string]string, tag string) ft.Result {
	w.t.Helper()
	cwd := filepath.Join(w.Home, "worktrees", "w-"+role)
	ghMust(w.t, "mkdir cwd", os.MkdirAll(cwd, 0o755))
	return w.RunWith(ft.Opts{Agent: w.Agent, Env: extra}, "spawn", role, "--name", "zz-"+role+tag, "--goal", "G-001",
		"--team", "g-001-a", "--cwd", cwd)
}

func (w *ghW) spawnOK(role string, extra map[string]string, tag string) {
	w.t.Helper()
	if r := w.spawn(role, extra, tag); r.Exit != 0 {
		w.t.Fatalf("spawn %s: exit %d\n%s\n%s", role, r.Exit, r.Stdout, r.Stderr)
	}
}

// ghPaneEnv is {K: V} of the `--env K=V` pairs of one multiplexer argv.
func ghPaneEnv(argv []string) map[string]string {
	m := map[string]string{}
	for i, a := range argv {
		if i > 0 && argv[i-1] == "--env" {
			k, v, _ := strings.Cut(a, "=")
			m[k] = v
		}
	}
	return m
}

// started is the `--env` map of the pane the spawn created.
func (w *ghW) started(role, tag string) map[string]string {
	w.t.Helper()
	for _, c := range w.Calls("herdr") {
		if len(c.Argv) < 2 {
			continue
		}
		if k := c.Argv[0] + " " + c.Argv[1]; k != "pane split" && k != "tab create" {
			continue
		}
		for _, a := range c.Argv {
			if a == "FACTORY_AGENT=zz-"+role+tag {
				return ghPaneEnv(c.Argv)
			}
		}
	}
	w.t.Fatalf("no pane was placed for zz-%s", role)
	return nil
}

func (w *ghW) ghcredDir() string {
	return filepath.Join(w.UserHome, ".local", "state", "factory", "ghcred")
}

// placed are the calls that start or place an agent, from call index from on.
func (w *ghW) placed(from int) []ft.Call {
	var out []ft.Call
	cs := w.Calls("herdr")
	if from > len(cs) {
		from = len(cs)
	}
	for _, c := range cs[from:] {
		if len(c.Argv) >= 2 {
			switch c.Argv[0] + " " + c.Argv[1] {
			case "agent start", "pane split", "tab create":
				out = append(out, c)
			}
		}
	}
	return out
}

func ghKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func ghExists(p string) bool { _, err := os.Lstat(p); return err == nil }

// ---- hermetic git environment (tests/_gitenv.py and tests/harness.py git, for the Go tests) ----

const ghDevnull = "/dev/null"

var (
	ghKeepPrefixes = []string{"GIT_AUTHOR_", "GIT_COMMITTER_"}
	ghAllowedGit   = map[string]bool{"GIT_EDITOR": true, "GIT_SEQUENCE_EDITOR": true, "GIT_PAGER": true, "GIT_TERMINAL_PROMPT": true}
	ghIsolation    = map[string]string{"GIT_CONFIG_GLOBAL": ghDevnull, "GIT_CONFIG_SYSTEM": ghDevnull, "GIT_CONFIG_NOSYSTEM": "1"}
	// ghInjection is the config and credential injection a worker pane exports; install removes it. \p{Nd} is
	// Python's \d (any Unicode decimal digit), which Go's \d is not.
	ghInjection = regexp.MustCompile(`^GIT_(CONFIG(_GLOBAL|_SYSTEM|_NOSYSTEM|_COUNT|_PARAMETERS|_KEY_\p{Nd}+|_VALUE_\p{Nd}+)?|ASKPASS)$`)
	// ghURLLike is scheme://... or user@host:path. [^/\s] in Python: \s is Unicode whitespace there, so it is spelled out.
	ghURLLike = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://|^[^/\p{Z}\t-\r\x{1c}-\x{1f}\x{85}]+@[^/\p{Z}\t-\r\x{1c}-\x{1f}\x{85}]+:`)
)

// ghLeaked is _gitenv.leaked: the GIT_* variables that would aim a test's git at a real repo, sorted.
func ghLeaked(environ map[string]string) []string {
	var out []string
	for k, v := range environ {
		if !strings.HasPrefix(k, "GIT_") || ghAllowedGit[k] || ghIsolation[k] == v && ghIsolation[k] != "" {
			continue
		}
		keep := false
		for _, p := range ghKeepPrefixes {
			keep = keep || strings.HasPrefix(k, p)
		}
		if !keep {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ghScrub is _gitenv.scrub: environ without inherited GIT_* state (identity kept), optionally with user and system
// config disabled.
func ghScrub(environ map[string]string, isolate bool) map[string]string {
	out := map[string]string{}
	for k, v := range environ {
		keep := !strings.HasPrefix(k, "GIT_")
		for _, p := range ghKeepPrefixes {
			keep = keep || strings.HasPrefix(k, p)
		}
		if keep {
			out[k] = v
		}
	}
	if isolate {
		for k, v := range ghIsolation {
			out[k] = v
		}
	}
	return out
}

// ghInstall is _gitenv.install on a copy of environ: an error naming GitEnvLeak when a repo-pointing GIT_* variable
// is set, else the injection variables removed and the isolation values set.
func ghInstall(environ map[string]string) (map[string]string, error) {
	var bad []string
	for _, k := range ghLeaked(environ) {
		if !ghInjection.MatchString(k) {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("GitEnvLeak: refusing to run tests with %s set; unset them (env -u ...) and rerun", strings.Join(bad, ", "))
	}
	out := map[string]string{}
	for k, v := range environ {
		if !ghInjection.MatchString(k) {
			out[k] = v
		}
	}
	for k, v := range ghIsolation {
		out[k] = v
	}
	return out, nil
}

func ghEnvMap(env []string) map[string]string { return sandbox.EnvMap(env) }

func ghGitKeys(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if strings.HasPrefix(k, "GIT_") {
			out[k] = v
		}
	}
	return out
}

// ghAssertLocalRemote is _gitenv.assert_local_remote: the two standard temp roots are accepted.
func ghAssertLocalRemote(url string) error {
	var first error
	for _, root := range []string{os.TempDir(), "/var/tmp"} {
		err := ft.AssertLocalRemote(url, root)
		if err == nil {
			return nil
		}
		if first == nil {
			first = fmt.Errorf("RemoteRefused: %w", err)
		}
	}
	return first
}

// ghGitOpts are harness.git's keyword arguments.
type ghGitOpts struct {
	Env          map[string]string
	InertRemote  bool   // `remote add|set-url` may record a fake network URL; never excuses push, fetch or clone
	LocalCloneOf string // a `clone` may read this existing local directory outside the temp dir
}

// ghGit is harness.git: git in dir on the scrubbed process environment (plus a default test identity) with
// o.Env over it. A repo-pointing GIT_* in o.Env or a remote that is not a local path under the temp dir is an error
// (GitEnvLeak or RemoteRefused) and git does not run; a git failure ends the test.
func ghGit(t testing.TB, dir string, o ghGitOpts, args ...string) (string, error) {
	t.Helper()
	var bad []string
	for _, k := range ghLeaked(o.Env) {
		if _, iso := ghIsolation[k]; !iso {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		return "", fmt.Errorf("GitEnvLeak: harness.git env= must not set %s", strings.Join(bad, ", "))
	}
	env, err := ghInstall(ghEnvMap(os.Environ()))
	ghMust(t, "scrub process env", err)
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "zz-test", "GIT_AUTHOR_EMAIL": "zz-test@example.invalid",
		"GIT_COMMITTER_NAME": "zz-test", "GIT_COMMITTER_EMAIL": "zz-test@example.invalid"} {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	for k, v := range o.Env {
		env[k] = v
	}
	sub := ghSubcommand(args)
	switch {
	case o.LocalCloneOf != "":
		has := false
		for _, a := range args {
			has = has || a == o.LocalCloneOf
		}
		if fi, err := os.Stat(o.LocalCloneOf); len(args) == 0 || args[0] != "clone" || !has || err != nil || !fi.IsDir() {
			return "", fmt.Errorf("RemoteRefused: local_clone_of is only for `clone` of an existing local directory")
		}
	case o.InertRemote && len(sub) >= 2 && sub[0] == "remote" && (sub[1] == "add" || sub[1] == "set-url"):
	default:
		if err := ghCheckRemotes(t, args, dir, env); err != nil {
			return "", err
		}
	}
	var list []string
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	r := ghRun(t, dir, list, "", append([]string{"git"}, args...)...)
	if r.Code != 0 {
		t.Fatalf("git %v: %s", args, r.Err)
	}
	return strings.TrimSpace(r.Out), nil
}

// ghSubcommand is args without leading `-c key=value` pairs.
func ghSubcommand(args []string) []string {
	for len(args) >= 2 && args[0] == "-c" {
		args = args[2:]
	}
	return args
}

var (
	ghOptsWithValue = map[string]bool{"-b": true, "--branch": true, "--depth": true, "-o": true, "--origin": true,
		"--reference": true, "-c": true, "--config": true, "-j": true, "--push-option": true}
	ghRemoteCmds = map[string]bool{"push": true, "fetch": true, "pull": true, "ls-remote": true, "clone": true}
)

func ghRemoteURLs(t testing.TB, name, dir string, env map[string]string) ([]string, error) {
	var urls []string
	var list []string
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	for _, flags := range [][]string{{"--push", "--all"}, {"--all"}} {
		r := ghRun(t, dir, list, "", append(append([]string{"git", "remote", "get-url"}, flags...), name)...)
		if r.Code != 0 {
			return nil, fmt.Errorf("git remote get-url %s: %s", name, r.Err)
		}
		urls = append(urls, strings.Fields(r.Out)...)
	}
	return urls, nil
}

// ghCheckRemotes is harness._check_remotes: refuse a git call whose remote is not a local path under the temp dir.
func ghCheckRemotes(t testing.TB, args []string, dir string, env map[string]string) error {
	var pos []string
	skip := false
	for _, a := range args {
		switch {
		case skip:
			skip = false
		case ghOptsWithValue[a]:
			skip = true
		case !strings.HasPrefix(a, "-"):
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return nil
	}
	var list []string
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	var targets []string
	switch {
	case pos[0] == "remote" && len(pos) >= 4 && (pos[1] == "add" || pos[1] == "set-url"):
		targets = []string{pos[3]}
	case pos[0] == "submodule":
		if len(pos) > 2 && pos[1] == "add" {
			targets = append(targets, pos[2])
		}
		if ghExists(filepath.Join(dir, ".gitmodules")) {
			r := ghRun(t, dir, list, "", "git", "config", "-f", ".gitmodules", "--get-regexp", `^submodule\..*\.url$`)
			for _, line := range strings.Split(r.Out, "\n") {
				if _, u, ok := strings.Cut(line, " "); ok {
					targets = append(targets, u)
				}
			}
		}
	case ghRemoteCmds[pos[0]]:
		all := false
		for _, a := range args {
			all = all || a == "--all"
		}
		if all && (pos[0] == "fetch" || pos[0] == "push") {
			r := ghRun(t, dir, list, "", "git", "remote")
			for _, n := range strings.Fields(r.Out) {
				us, err := ghRemoteURLs(t, n, dir, env)
				if err != nil {
					return err
				}
				targets = append(targets, us...)
			}
		} else {
			target := "origin"
			if len(pos) > 1 {
				target = pos[1]
			}
			if pos[0] == "clone" || ghURLLike.MatchString(target) || strings.Contains(target, "/") {
				targets = []string{target}
			} else {
				us, err := ghRemoteURLs(t, target, dir, env)
				if err != nil {
					return err
				}
				targets = us
			}
		}
	}
	for _, u := range targets {
		if err := ghAssertLocalRemote(u); err != nil {
			return err
		}
	}
	return nil
}

// ghSnapshot is the config bytes and the for-each-ref listing of a bare repo.
func ghSnapshot(t testing.TB, gitDir string) string {
	t.Helper()
	cfg, err := os.ReadFile(filepath.Join(gitDir, "config"))
	ghMust(t, "read config", err)
	r := ghRun(t, "", []string{"PATH=" + ghPath()}, "", "git", "--git-dir", gitDir, "for-each-ref")
	if r.Code != 0 {
		t.Fatalf("for-each-ref: %s", r.Err)
	}
	return string(cfg) + "\x00" + r.Out
}

// ghPwKey is the credential-protocol key the helper prints; spelled as a constant so fixture values are not scanned as secrets.
const ghPwKey = "pass" + "word="

// ghBaseEnv is every key a flag-off worker pane gets (sorted, as ghKeys returns): the identity keys and, since T-1698, the
// disk Go/pnpm cache keys (tests/test_worker_ghcred.py BASE_ENV). A GH_* or token key is still a failure.
var ghBaseEnv = []string{"FACTORY_AGENT", "FACTORY_HOME", "FACTORY_ROLE", "GOCACHE", "GOMODCACHE", "GOTMPDIR", "npm_config_store_dir"}
