// Package releasecheck is the Go port of ci/release_check.py: the release gate. It runs every check on a release
// candidate SHA and prints the owner's tag commands when all pass.
//
//	ci-release-check <sha> [--version X.Y.Z] [--repo OWNER/NAME|DIR] [--require-no-python]
//	ci-release-check --repo DIR [--require-no-python]        (python-under check only)
//
// Checks (one PASS/FAIL line each, all run): on-main, version, ci-green, notes, fresh-install-smoke, public-text,
// tag-free, cursor-runtime. Exit 0 all pass (tag commands printed), 1 a check failed, 2 usage error or unresolvable
// SHA. Read-only: it never tags, pushes or creates a release; it only prints the commands for the owner.
//
// The v0.6.0 rule (G-048 SC2): no Python file under bin/, roles/hooks/, web/, scout/, retro/,
// plugins/hivemill/scripts/. Each directory prints "release-check: python under <dir>: <n>" as a warning; with
// --require-no-python a non-zero count fails the gate (default off until the P7 stage removes the Python).
//
// --repo is OWNER/NAME as in the Python tool, or an existing directory: the checkout to inspect. With a directory
// and no SHA only the python-under check runs. Until P5b/P7 the smoke, public-text and cursor-runtime checks still
// run the Python tools of the checked-out tree through python3.
package releasecheck

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/citools/changelog"
	_ "modernc.org/sqlite"
)

var (
	versionRe      = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	placeholderRe  = regexp.MustCompile(`TODO|TBD|FIXME|XXX|__[A-Z_]+__|<[a-z][a-z -]*>|<!--`)
	smokeVersionRe = regexp.MustCompile(`^hivemill (\S+)(?:\s|$)`)
	remoteRe       = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:)([^/\s]+)/([^/\s]+?)(?:\.git)?/?$`)
	shaRe          = regexp.MustCompile(`^[0-9a-f]{40}$`)
	slugRe         = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)
	smokeWait      = 20 * time.Second
	cursorPaths    = []string{"scout/cursor_harness.py", "roles/hooks/cursor-guard-adapter.py", "scout/agent_runtime.py"}
	pythonRuleDirs = []string{"bin/", "roles/hooks/", "web/", "scout/", "retro/", "plugins/hivemill/scripts/"}
	cursorSnippet  = `
from scout.agent_runtime import parse_runtimes, resolve_runtime
if resolve_runtime("pm", "") != "claude":
    raise SystemExit("empty agent_runtimes: pm must resolve to claude")
m = parse_runtimes("*=cursor")
if m.get("coder") != "cursor":
    raise SystemExit("*=cursor: coder must resolve to cursor")
if resolve_runtime("evaluator", "*=cursor") != "claude":
    raise SystemExit("*=cursor: evaluator must stay claude")
`
	errUsage = errors.New("usage")
)

// RunFunc runs argv and returns (exit code, stdout, stderr). A missing program is 127, a timeout 124.
type RunFunc func(argv []string, cwd string, env []string, timeout time.Duration) (int, string, string)

// ProbeFunc starts argv (factory serve), waits for HTTP 200 on /, stops it.
type ProbeFunc func(argv []string, env []string, port int) (bool, string)

// RunCmd is the real RunFunc.
func RunCmd(argv []string, cwd string, env []string, timeout time.Duration) (int, string, string) {
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	if env != nil {
		cmd.Env = env
	}
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, o.String(), e.String()
	case ctx.Err() == context.DeadlineExceeded:
		return 124, "", fmt.Sprintf("%s timed out after %ds", argv[0], int(timeout.Seconds()))
	case errors.As(err, &ee):
		return ee.ExitCode(), o.String(), e.String()
	}
	return 127, "", fmt.Sprintf("cannot run %s: %v", argv[0], err)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ServeProbe is the real ProbeFunc.
func ServeProbe(argv []string, env []string, port int) (bool, string) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return false, fmt.Sprintf("cannot start factory serve: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}()
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(smokeWait)
	last := "no answer"
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
			done <- err
			return false, fmt.Sprintf("factory serve exited with %d", code)
		default:
		}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err == nil {
			resp.Body.Close()
			return resp.StatusCode == 200, fmt.Sprintf("HTTP %d on /", resp.StatusCode)
		}
		last = err.Error()
		time.Sleep(300 * time.Millisecond)
	}
	return false, fmt.Sprintf("no HTTP 200 on / within %ds (%s)", int(smokeWait.Seconds()), last)
}

// Gate is one release check run.
type Gate struct {
	SHA, Version, Repo string
	Run                RunFunc
	Probe              ProbeFunc
	Root               string
	Env                []string
	Python             string
	tmp                string
}

func (g *Gate) git(args ...string) (int, string, string) {
	return g.Run(append([]string{"git", "-C", g.Root}, args...), "", nil, 0)
}

func (g *Gate) envGet(key string) string {
	for i := len(g.Env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(g.Env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

func (g *Gate) envWithout(keys ...string) []string {
	var out []string
	for _, kv := range g.Env {
		drop := false
		for _, k := range keys {
			if strings.HasPrefix(kv, k+"=") {
				drop = true
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

func trim(s string) string { return strings.TrimSpace(s) }

func either(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

func (g *Gate) onMain() (bool, string) {
	rc, out, err := g.git("rev-list", "--first-parent", "origin/main")
	if rc != 0 {
		return false, fmt.Sprintf("cannot list origin/main (%s). Run: git fetch origin", either(trim(err), fmt.Sprint(rc)))
	}
	for _, f := range strings.Fields(out) {
		if f == g.SHA {
			return true, fmt.Sprintf("%s is on the first-parent history of origin/main", short(g.SHA))
		}
	}
	return false, fmt.Sprintf("%s is not on the first-parent history of origin/main. Pick a main commit (git fetch origin first)", short(g.SHA))
}

func (g *Gate) versionCheck() (bool, string) {
	if !versionRe.MatchString(g.Version) {
		return false, fmt.Sprintf("%s is not MAJOR.MINOR.PATCH", pyRepr(g.Version))
	}
	rc, out, err := g.git("show", g.SHA+":VERSION")
	if rc != 0 {
		return false, fmt.Sprintf("no VERSION file at %s (%s)", short(g.SHA), either(trim(err), fmt.Sprint(rc)))
	}
	if have := trim(out); have != g.Version {
		return false, fmt.Sprintf("VERSION at %s is %s, not %s", short(g.SHA), pyRepr(have), pyRepr(g.Version))
	}
	return true, "VERSION " + trim(out)
}

func (g *Gate) ciGreen() (bool, string) {
	rc, out, err := g.Run([]string{"gh", "run", "list", "-R", g.Repo, "--workflow", "tests.yml", "--commit", g.SHA,
		"--event", "push", "--json", "status,conclusion,databaseId"}, "", nil, 0)
	if rc != 0 {
		return false, fmt.Sprintf("gh run list failed (%s)", either(trim(err), fmt.Sprint(rc)))
	}
	if trim(out) == "" {
		out = "[]"
	}
	var runs []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		ID         any    `json:"databaseId"`
	}
	if json.Unmarshal([]byte(out), &runs) != nil {
		return false, "gh run list printed invalid JSON"
	}
	for _, r := range runs {
		if r.Status == "completed" && r.Conclusion == "success" {
			return true, fmt.Sprintf("tests.yml push run %v succeeded (push runs on main run the full suite)", r.ID)
		}
	}
	if len(runs) == 0 {
		return false, fmt.Sprintf("no tests.yml push run for %s; wait for CI on main", short(g.SHA))
	}
	var parts []string
	for _, r := range runs {
		parts = append(parts, fmt.Sprintf("%v %s/%s", r.ID, r.Status, either(r.Conclusion, "-")))
	}
	return false, "no successful tests.yml push run: " + strings.Join(parts, ", ")
}

func (g *Gate) notes() (bool, string) {
	src := filepath.Join(g.tmp, "src")
	if problems := changelog.VersionProblems(g.Version, src); len(problems) > 0 {
		var lines []string
		for _, p := range problems {
			lines = append(lines, "changelog: check-version: "+p)
		}
		return false, fmt.Sprintf("changelog --check-version %s exit 1: %s", g.Version, strings.Join(lines, "\n"))
	}
	text, err := changelog.ReleaseNotes(src, g.Version)
	if err != nil {
		return false, fmt.Sprintf("changelog --release-notes %s exit 2: changelog: error: %v", g.Version, err)
	}
	var hits []string
	for n, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		for _, m := range placeholderRe.FindAllString(line, -1) {
			hits = append(hits, fmt.Sprintf("line %d: %s", n+1, m))
		}
	}
	if len(hits) > 0 {
		return false, "placeholders in the release notes: " + strings.Join(hits, "; ")
	}
	return true, fmt.Sprintf("notes for %s complete, 0 placeholders", g.Version)
}

// smoke: fresh install from a detached checkout, isolated HOME and FACTORY_HOME under the temp dir.
func (g *Gate) smoke() (bool, string) {
	src := filepath.Join(g.tmp, "src")
	home, fh, demo := filepath.Join(g.tmp, "home"), filepath.Join(g.tmp, "fh"), filepath.Join(g.tmp, "demo")
	os.MkdirAll(home, 0o755)
	os.MkdirAll(fh, 0o755)
	env := append(g.envWithout("HOME", "FACTORY_HOME"), "HOME="+home, "FACTORY_HOME="+fh)
	factory := []string{g.Python, filepath.Join(src, "bin", "factory")}
	rc, out, err := g.Run(append(append([]string{}, factory...), "--version"), "", env, 0)
	m := smokeVersionRe.FindStringSubmatch(out)
	if rc != 0 || m == nil || m[1] != g.Version {
		return false, fmt.Sprintf("factory --version printed %s (exit %d), want version exactly %s (%s). %s",
			pyRepr(trim(out)), rc, pyRepr(g.Version), pyRepr("hivemill "+g.Version), trim(err))
	}
	rc, out, err = g.Run([]string{g.Python, filepath.Join(src, "ci", "demo_seed.py"), "--home", demo}, "", env, 0)
	if rc != 0 {
		t := trim(either(err, out))
		if len(t) > 300 {
			t = t[len(t)-300:]
		}
		return false, fmt.Sprintf("demo_seed.py exit %d: %s", rc, t)
	}
	port, perr := freePort()
	if perr != nil {
		return false, fmt.Sprintf("no free port: %v", perr)
	}
	senv := append(g.envWithout("HOME", "FACTORY_HOME"), "HOME="+home, "FACTORY_HOME="+demo)
	ok, detail := g.Probe(append(append([]string{}, factory...), "serve", "--bind", "127.0.0.1", "--port", fmt.Sprint(port)), senv, port)
	if !ok {
		return false, "factory serve on the demo home: " + detail
	}
	return true, fmt.Sprintf("factory --version, demo_seed.py and factory serve (%s) from a detached checkout", detail)
}

func (g *Gate) publicText() (bool, string) {
	src := filepath.Join(g.tmp, "src")
	notes := filepath.Join(src, "ci", "release-notes", g.Version+".md")
	env := append([]string{}, g.Env...)
	if g.envGet(termsEnv) == "" {
		if terms := g.configTermsFile(); terms != "" {
			env = append(env, termsEnv+"="+terms)
		}
	}
	rc, out, err := g.Run([]string{g.Python, filepath.Join(src, "ci", "public_text_guard.py"), "text", notes}, "", env, 0)
	if rc == 0 {
		return true, fmt.Sprintf("public_text_guard.py text ci/release-notes/%s.md is clean", g.Version)
	}
	why := "hits"
	if rc == 2 {
		why = "owner terms not loaded, the guard fails closed"
	}
	t := trim(either(err, out))
	if len(t) > 300 {
		t = t[len(t)-300:]
	}
	return false, fmt.Sprintf("public_text_guard.py exit %d (%s): %s", rc, why, t)
}

// termsEnv names the environment variable that points at the owner-terms file (the scanners read it).
const termsEnv = "SCRUB_" + "TERMS_FILE"

// configTermsFile is the owner-terms file path from the factory config, or "" when unset or unreadable (the guard then fails closed).
func (g *Gate) configTermsFile() string {
	home := g.envGet("FACTORY_HOME")
	if home == "" {
		home = "~/factory"
	}
	if strings.HasPrefix(home, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, strings.TrimPrefix(home, "~"))
		}
	}
	db := filepath.Join(home, "db", "factory.db")
	con, err := sql.Open("sqlite", "file:"+db+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return ""
	}
	defer con.Close()
	var v sql.NullString
	if err := con.QueryRow("SELECT value FROM config WHERE key = 'scrub_terms_file'").Scan(&v); err != nil {
		return ""
	}
	return trim(v.String)
}

func (g *Gate) tagFree() (bool, string) {
	tag := "v" + g.Version
	rc, out, err := g.git("ls-remote", "--tags", "origin", "refs/tags/"+tag)
	if rc != 0 {
		return false, fmt.Sprintf("git ls-remote failed (%s)", either(trim(err), fmt.Sprint(rc)))
	}
	if trim(out) != "" {
		return false, fmt.Sprintf("tag %s already exists on origin", tag)
	}
	return true, tag + " is not on origin"
}

func (g *Gate) fileAtSHA(rel string) bool {
	rc, _, _ := g.git("show", g.SHA+":"+rel)
	return rc == 0
}

func (g *Gate) cursorRuntimeTree() string {
	src := filepath.Join(g.tmp, "src")
	if st, err := os.Stat(filepath.Join(src, "scout", "agent_runtime.py")); err == nil && st.Mode().IsRegular() {
		return src
	}
	return g.Root
}

func (g *Gate) cursorRuntime() (bool, string) {
	var missing []string
	for _, p := range cursorPaths {
		if !g.fileAtSHA(p) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return false, fmt.Sprintf("missing at %s: %s", short(g.SHA), strings.Join(missing, ", "))
	}
	tree := g.cursorRuntimeTree()
	env := append(g.envWithout("PYTHONPATH"), "PYTHONPATH="+tree)
	rc, out, err := g.Run([]string{g.Python, "-c", cursorSnippet}, tree, env, 60*time.Second)
	if rc != 0 {
		msg := fmt.Sprintf("exit %d", rc)
		if t := trim(either(err, out)); t != "" {
			lines := strings.Split(t, "\n")
			msg = lines[len(lines)-1]
		}
		return false, "runtime resolve check failed: " + msg
	}
	return true, "cursor harness files present; pm/claude, coder/cursor, evaluator/claude resolve"
}

func (g *Gate) prepareCheckout() string {
	src := filepath.Join(g.tmp, "src")
	rc, _, err := g.Run([]string{"git", "clone", "--quiet", "--no-checkout", "--shared", g.Root, src}, "", nil, 0)
	if rc == 0 {
		rc, _, err = g.Run([]string{"git", "-C", src, "checkout", "--quiet", "--detach", g.SHA}, "", nil, 0)
	}
	if rc == 0 {
		return ""
	}
	return fmt.Sprintf("cannot make a detached checkout of %s: %s", short(g.SHA), either(trim(err), fmt.Sprint(rc)))
}

// RunAll runs every check, printing one PASS/FAIL line each; true when all pass.
func (g *Gate) RunAll(out func(string)) bool {
	tmp, err := os.MkdirTemp("", "release-check-")
	if err != nil {
		out(fmt.Sprintf("FAIL on-main: cannot make a temp dir: %v", err))
		return false
	}
	g.tmp = tmp
	defer os.RemoveAll(tmp)
	broken := g.prepareCheckout()
	needsSrc := map[string]bool{"notes": true, "fresh-install-smoke": true, "public-text": true, "cursor-runtime": true}
	checks := []struct {
		name string
		fn   func() (bool, string)
	}{{"on-main", g.onMain}, {"version", g.versionCheck}, {"ci-green", g.ciGreen}, {"notes", g.notes},
		{"fresh-install-smoke", g.smoke}, {"public-text", g.publicText}, {"tag-free", g.tagFree}, {"cursor-runtime", g.cursorRuntime}}
	all := true
	for _, c := range checks {
		ok, detail := false, broken
		if !(broken != "" && needsSrc[c.name]) {
			ok, detail = c.fn()
		}
		word := "FAIL"
		if ok {
			word = "PASS"
		}
		out(fmt.Sprintf("%s %s: %s", word, c.name, detail))
		all = all && ok
	}
	return all
}

// originURL is the https clone URL of the checked repo's origin (freshbox runs as another user, so no ssh URL);
// falls back to --repo.
func (g *Gate) originURL() string {
	rc, url, _ := g.git("remote", "get-url", "origin")
	var owner, name string
	if m := remoteRe.FindStringSubmatch(trim(url)); rc == 0 && m != nil {
		owner, name = m[1], m[2]
	} else {
		owner, name, _ = strings.Cut(g.Repo, "/")
	}
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, name)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./-]+$`)

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// OwnerCommands are the lines printed once all checks pass.
func (g *Gate) OwnerCommands() []string {
	v := g.Version
	var lines []string
	if parts := strings.Split(v, "."); parts[2] == "0" {
		lines = append(lines, fmt.Sprintf("Reminder: v%s is a MINOR (or MAJOR) release. Before tagging, the owner also runs the full fresh-box test:", v),
			fmt.Sprintf("  cd %s && sudo python3 ci/freshbox_test.py --repo %s --ref %s", shellQuote(g.Root), shellQuote(g.originURL()), g.SHA))
	}
	return append(lines, fmt.Sprintf("Release v%s is ready. Owner, run:", v),
		"  git -C ~/factory fetch origin",
		fmt.Sprintf("  git -C ~/factory tag -a v%s -m v%s %s", v, v, g.SHA),
		fmt.Sprintf("  git -C ~/factory push origin v%s", v),
		"Do not run `gh release create`: the tag push makes release.yml create the release.")
}

// PythonCounts is the number of tracked .py files under each directory of the v0.6.0 rule. With sha it reads the
// tree of that commit, else the tracked files of the checkout at root (a plain walk when root is not a git repo).
func PythonCounts(run RunFunc, root, sha string) ([]string, []int) {
	counts := make([]int, len(pythonRuleDirs))
	var rc int
	var out string
	if sha != "" {
		rc, out, _ = run([]string{"git", "-C", root, "-c", "core.quotepath=false", "ls-tree", "-r", "-z", "--name-only", sha}, "", nil, 0)
	} else {
		rc, out, _ = run([]string{"git", "-C", root, "-c", "core.quotepath=false", "ls-files", "-z"}, "", nil, 0)
	}
	var files []string
	if rc == 0 {
		files = strings.Split(out, "\x00")
	} else {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			if err == nil && !d.IsDir() {
				if r, e := filepath.Rel(root, p); e == nil {
					files = append(files, filepath.ToSlash(r))
				}
			}
			return nil
		})
	}
	for _, f := range files {
		if !strings.HasSuffix(f, ".py") {
			continue
		}
		for i, d := range pythonRuleDirs {
			if strings.HasPrefix(f, d) {
				counts[i]++
			}
		}
	}
	return pythonRuleDirs, counts
}

type args struct {
	sha, version, repo string
	requireNoPython    bool
}

func parseArgs(argv []string) (args, error) {
	var a args
	var pos []string
	for i := 0; i < len(argv); i++ {
		s := argv[i]
		switch {
		case s == "--require-no-python":
			a.requireNoPython = true
		case s == "--version" || s == "--repo":
			if i+1 >= len(argv) {
				return a, errUsage
			}
			i++
			if s == "--version" {
				a.version = argv[i]
			} else {
				a.repo = argv[i]
			}
		case strings.HasPrefix(s, "--version="):
			a.version = strings.TrimPrefix(s, "--version=")
		case strings.HasPrefix(s, "--repo="):
			a.repo = strings.TrimPrefix(s, "--repo=")
		case strings.HasPrefix(s, "-"):
			return a, errUsage
		default:
			pos = append(pos, s)
		}
	}
	if len(pos) > 1 {
		return a, errUsage
	}
	if len(pos) == 1 {
		a.sha = pos[0]
	}
	return a, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// findRoot is the nearest parent of the working directory that holds VERSION and ci/.
func findRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; d = filepath.Dir(d) {
		if isDir(filepath.Join(d, "ci")) {
			if st, err := os.Stat(filepath.Join(d, "VERSION")); err == nil && st.Mode().IsRegular() {
				return d, nil
			}
		}
		if filepath.Dir(d) == d {
			return "", errors.New("cannot find a factory checkout (VERSION and ci/) above the working directory: pass --repo DIR")
		}
	}
}

// Main is the real entry point.
func Main(argv []string, stdout, stderr io.Writer) int {
	return Exec(argv, RunCmd, ServeProbe, os.Environ(), stdout, stderr)
}

// Exec runs the tool with injected process runner, serve probe and environment.
func Exec(argv []string, run RunFunc, probe ProbeFunc, env []string, stdout, stderr io.Writer) int {
	a, err := parseArgs(argv)
	if err != nil {
		fmt.Fprintln(stderr, "usage: ci-release-check <sha> [--version X.Y.Z] [--repo OWNER/NAME|DIR] [--require-no-python]")
		return 2
	}
	root, slug := "", ""
	if a.repo != "" && isDir(a.repo) {
		abs, aerr := filepath.Abs(a.repo)
		if aerr != nil {
			fmt.Fprintf(stderr, "release_check: cannot resolve --repo %q: %v\n", a.repo, aerr)
			return 2
		}
		root = abs
	} else if a.repo != "" {
		if !slugRe.MatchString(a.repo) {
			fmt.Fprintf(stderr, "release_check: --repo %q is neither a directory nor OWNER/NAME.\n", a.repo)
			return 2
		}
		slug = a.repo
	}
	if root == "" {
		r, rerr := findRoot()
		if rerr != nil {
			fmt.Fprintf(stderr, "release_check: %v\n", rerr)
			return 2
		}
		root = r
	}
	out := func(s string) { fmt.Fprintln(stdout, s) }
	python := func() int {
		dirs, counts := PythonCounts(run, root, a.sha)
		total := 0
		for i, d := range dirs {
			out(fmt.Sprintf("release-check: python under %s: %d", d, counts[i]))
			total += counts[i]
		}
		if a.requireNoPython && total > 0 {
			out(fmt.Sprintf("FAIL no-python: %d Python file(s) remain under %s (--require-no-python)", total, strings.Join(dirs, ", ")))
			return 1
		}
		return 0
	}
	if a.sha == "" {
		if a.repo == "" || slug != "" {
			fmt.Fprintln(stderr, "usage: ci-release-check <sha> [--version X.Y.Z] [--repo OWNER/NAME|DIR] [--require-no-python]")
			return 2
		}
		return python()
	}
	rc, full, rerr := run([]string{"git", "-C", root, "rev-parse", "--verify", a.sha + "^{commit}"}, "", nil, 0)
	full = trim(full)
	if rc != 0 || !shaRe.MatchString(full) {
		fmt.Fprintf(stderr, "release_check: cannot resolve %s to a commit (%s). Run: git fetch origin\n", pyRepr(a.sha), either(trim(rerr), "no output"))
		return 2
	}
	version := a.version
	if version == "" {
		if rc, v, _ := run([]string{"git", "-C", root, "show", full + ":VERSION"}, "", nil, 0); rc == 0 {
			version = trim(v)
		}
	}
	if slug == "" {
		rc, url, _ := run([]string{"git", "-C", root, "remote", "get-url", "origin"}, "", nil, 0)
		m := remoteRe.FindStringSubmatch(trim(url))
		if rc != 0 || m == nil {
			fmt.Fprintln(stderr, "release_check: cannot parse OWNER/NAME from the origin remote. Pass --repo OWNER/NAME.")
			return 2
		}
		slug = m[1] + "/" + m[2]
	}
	pyRC := python()
	g := &Gate{SHA: full, Version: version, Repo: slug, Run: run, Probe: probe, Root: root, Env: env, Python: "python3"}
	if !g.RunAll(out) || pyRC != 0 {
		return 1
	}
	for _, l := range g.OwnerCommands() {
		out(l)
	}
	return 0
}
