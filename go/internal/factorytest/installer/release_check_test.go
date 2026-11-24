package installer

// Port of tests/test_release_check.py (42 cases) to the Go ci-release-check tool.
//
// Unit-style cases (CheckTest) call releasecheck.Exec with a fake process runner, the same process boundary the
// Python tests inject (git, gh and the python children). The real-git cases build a temp repo and use the real
// runner; the entry-point case drives the built ci-release-check binary.
//
// not ported: (none)
//
// Adaptations forced by the Go design (assertions are unchanged):
//   - The notes check (changelog --check-version / --release-notes) runs in-process on the checkout, so the Python
//     "check-version" and "release-notes" runner overrides become the content of the checkout's CHANGELOG.md and
//     ci/release-notes/<ver>.md, written by the fake clone. "### Upgrade" is the last line of the fake notes.
//   - --repo is a DIR or an OWNER/NAME slug, never both, so the real-git cases pass --repo DIR and the repo gets an
//     origin remote that names acme/widget.
//   - test_cursor_runtime_resolve_contract runs the real scout/agent_runtime.py through python3 (the Python original
//     imported it directly).
//   - Default env gets a throw-away FACTORY_HOME so the config lookup never reads the real ~/factory.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"factory/go/internal/citools/releasecheck"
	ft "factory/go/internal/factorytest"
)

var rcSHA = strings.Repeat("a", 40)

var rcChecks = []string{"on-main", "version", "ci-green", "notes", "fresh-install-smoke", "public-text", "tag-free", "cursor-runtime"}

const rcTermsEnv = "SCRUB_" + "TERMS_FILE"

var rcGreenRun = func() string {
	b, _ := json.Marshal([]map[string]any{{"status": "completed", "conclusion": "success", "databaseId": 777}})
	return string(b)
}()

type rcRes struct {
	rc       int
	out, err string
}

// rcFake is green by default; over maps a key (see rcKey) to a result.
type rcFake struct {
	t                *testing.T
	version          string
	over             map[string]rcRes
	notes, changelog string
	probeFail        bool
	probeDetail      string
	calls, envs      [][]string
}

func newRCFake(t *testing.T, version string, over map[string]rcRes) *rcFake {
	if over == nil {
		over = map[string]rcRes{}
	}
	return &rcFake{t: t, version: version, over: over, notes: "# Notes\nAll good\n### Upgrade\n",
		changelog: "## [" + version + "] - x\n", probeDetail: "HTTP 200 on /"}
}

func rcKey(argv []string) string {
	switch argv[0] {
	case "gh":
		return "gh"
	case "git":
		rest := argv[1:]
		if argv[1] == "-C" {
			rest = argv[3:]
		}
		if rest[0] == "show" && len(rest) > 1 {
			path := rest[1]
			if _, p, ok := strings.Cut(rest[1], ":"); ok {
				path = p
			}
			return "git-show:" + path
		}
		return "git-" + rest[0]
	case "python3":
		s := argv[1]
		if strings.HasSuffix(s, "bin/factory") {
			if argv[2] == "--version" {
				return "factory-version"
			}
			return "factory-" + argv[2]
		}
		if s == "-c" {
			return "python-c"
		}
		return filepath.Base(s)
	}
	return argv[0]
}

func rcWrite(t testing.TB, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *rcFake) run(argv []string, cwd string, env []string, timeout time.Duration) (int, string, string) {
	f.calls = append(f.calls, append([]string{}, argv...))
	f.envs = append(f.envs, env)
	k := rcKey(argv)
	if r, ok := f.over[k]; ok {
		return r.rc, r.out, r.err
	}
	switch {
	case k == "git-clone": // the fake checkout: what the notes check and the cursor tree read
		src := argv[len(argv)-1]
		rcWrite(f.t, filepath.Join(src, "VERSION"), f.version+"\n")
		rcWrite(f.t, filepath.Join(src, "CHANGELOG.md"), f.changelog)
		rcWrite(f.t, filepath.Join(src, "ci", "release-notes", f.version+".md"), f.notes)
		rcWrite(f.t, filepath.Join(src, "scout", "agent_runtime.py"), "")
		return 0, "", ""
	case k == "git-show:VERSION":
		if r, ok := f.over["git-show"]; ok {
			return r.rc, r.out, r.err
		}
		return 0, f.version + "\n", ""
	case strings.HasPrefix(k, "git-show:"):
		return 0, "ok\n", ""
	}
	switch k {
	case "git-rev-parse":
		return 0, rcSHA + "\n", ""
	case "git-show":
		return 0, f.version + "\n", ""
	case "git-remote":
		return 0, "https://github.com/acme/widget.git\n", ""
	case "git-rev-list":
		return 0, rcSHA + "\n" + strings.Repeat("b", 40) + "\n", ""
	case "gh":
		return 0, rcGreenRun, ""
	case "factory-version":
		return 0, "hivemill " + f.version + " (git abc)\n", ""
	}
	return 0, "", ""
}

func (f *rcFake) probe(argv []string, env []string, port int) (bool, string) {
	f.calls = append(f.calls, append([]string{}, argv...))
	f.envs = append(f.envs, env)
	return !f.probeFail, f.probeDetail
}

func rcLines(out string) []string {
	if out == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

func rcHasTagLine(lines []string) bool {
	for _, ln := range lines {
		if strings.Contains(ln, "tag -a") {
			return true
		}
	}
	return false
}

// rcExec runs Exec with the fake and an explicit env; no owner commands may appear unless the exit is 0.
func rcExec(t *testing.T, f *rcFake, env []string, args ...string) (int, []string, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := releasecheck.Exec(args, f.run, f.probe, env, &o, &e)
	lines := rcLines(o.String())
	if code != 0 && rcHasTagLine(lines) {
		t.Fatalf("owner commands printed on exit %d: %v", code, lines)
	}
	return code, lines, e.String()
}

// rcGo is the Python go(): the SHA plus args, a default hermetic env.
func rcGo(t *testing.T, f *rcFake, args ...string) (int, []string) {
	t.Helper()
	env := []string{"PATH=/usr/bin", "FACTORY_AGENT=x", "FACTORY_HOME=" + t.TempDir()}
	code, lines, _ := rcExec(t, f, env, append([]string{rcSHA}, args...)...)
	return code, lines
}

func rcStatus(t *testing.T, lines []string, name string) string {
	t.Helper()
	var got []string
	for _, ln := range lines {
		if p := strings.SplitN(ln, " ", 3); len(p) >= 2 && p[1] == name+":" {
			got = append(got, p[0])
		}
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one %q status line, got %v in %v", name, got, lines)
	}
	return got[0]
}

func rcLine(t *testing.T, lines []string, sub string) string {
	t.Helper()
	for _, ln := range lines {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	t.Fatalf("no line contains %q in %v", sub, lines)
	return ""
}

func rcAssertFail(t *testing.T, name string, version string, over map[string]rcRes, mod func(*rcFake), args ...string) []string {
	t.Helper()
	f := newRCFake(t, version, over)
	if mod != nil {
		mod(f)
	}
	code, lines := rcGo(t, f, args...)
	if got := rcStatus(t, lines, name); got != "FAIL" {
		t.Fatalf("%s status %s, want FAIL: %v", name, got, lines)
	}
	if code != 1 {
		t.Fatalf("exit %d, want 1: %v", code, lines)
	}
	for _, other := range rcChecks { // all checks still ran
		if s := rcStatus(t, lines, other); s != "PASS" && s != "FAIL" {
			t.Fatalf("%s status %q", other, s)
		}
	}
	return lines
}

func rcEq(t *testing.T, got, want any) {
	t.Helper()
	if g, w := toJSON(got), toJSON(want); g != w {
		t.Fatalf("got %s, want %s", g, w)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestReleaseCheckAllGreenPrintsExactCommands(t *testing.T) {
	f := newRCFake(t, "0.3.1", nil)
	code, lines := rcGo(t, f)
	if code != 0 {
		t.Fatalf("exit %d: %v", code, lines)
	}
	for _, c := range rcChecks {
		if s := rcStatus(t, lines, c); s != "PASS" {
			t.Fatalf("%s %s: %v", c, s, lines)
		}
	}
	rcEq(t, lines[len(lines)-5:], []string{
		"Release v0.3.1 is ready. Owner, run:", "  git -C ~/factory fetch origin",
		"  git -C ~/factory tag -a v0.3.1 -m v0.3.1 " + rcSHA, "  git -C ~/factory push origin v0.3.1",
		"Do not run `gh release create`: the tag push makes release.yml create the release."})
	notContains(t, strings.Join(lines, "\n"), "freshbox")
	contains(t, rcLine(t, lines, "ci-green"), "777")
}

func TestReleaseCheckMinorReleasePrintsFreshboxReminderBeforeCommands(t *testing.T) {
	code, lines := rcGo(t, newRCFake(t, "0.3.0", nil))
	if code != 0 {
		t.Fatalf("exit %d: %v", code, lines)
	}
	if !strings.HasPrefix(lines[len(lines)-7], "Reminder: v0.3.0") {
		t.Fatalf("lines[-7] = %q", lines[len(lines)-7])
	}
	contains(t, lines[len(lines)-6], "ci/freshbox_test.py")
	rcEq(t, lines[len(lines)-5], "Release v0.3.0 is ready. Owner, run:")
}

func TestReleaseCheckOnMainFail(t *testing.T) {
	rcAssertFail(t, "on-main", "0.3.1", map[string]rcRes{"git-rev-list": {0, strings.Repeat("b", 40) + "\n", ""}}, nil)
}

func TestReleaseCheckOnMainFailWhenRevListErrors(t *testing.T) {
	rcAssertFail(t, "on-main", "0.3.1", map[string]rcRes{"git-rev-list": {128, "", "bad ref"}}, nil)
}

func TestReleaseCheckVersionFailMismatch(t *testing.T) {
	rcAssertFail(t, "version", "0.3.1", map[string]rcRes{"git-show": {0, "0.9.9\n", ""}}, nil, "--version", "0.3.1")
}

func TestReleaseCheckVersionFailFormat(t *testing.T) {
	f := newRCFake(t, "v1", map[string]rcRes{"git-show": {0, "v1\n", ""}})
	code, lines := rcGo(t, f)
	if s := rcStatus(t, lines, "version"); s != "FAIL" || code != 1 {
		t.Fatalf("%s %d: %v", s, code, lines)
	}
}

func TestReleaseCheckVersionFlagOverridesDefault(t *testing.T) {
	f := newRCFake(t, "0.3.1", map[string]rcRes{"git-show": {0, "0.2.0\n", ""}})
	code, lines := rcGo(t, f, "--version", "0.3.1")
	if s := rcStatus(t, lines, "version"); s != "FAIL" || code != 1 { // equals VERSION at the SHA is still required
		t.Fatalf("%s %d: %v", s, code, lines)
	}
}

func TestReleaseCheckCiGreenFailNoRun(t *testing.T) {
	rcAssertFail(t, "ci-green", "0.3.1", map[string]rcRes{"gh": {0, "[]", ""}}, nil)
}

func TestReleaseCheckCiGreenFailPending(t *testing.T) {
	out := `[{"status":"in_progress","conclusion":"","databaseId":5}]`
	rcAssertFail(t, "ci-green", "0.3.1", map[string]rcRes{"gh": {0, out, ""}}, nil)
}

func TestReleaseCheckCiGreenFailFailure(t *testing.T) {
	out := `[{"status":"completed","conclusion":"failure","databaseId":5}]`
	rcAssertFail(t, "ci-green", "0.3.1", map[string]rcRes{"gh": {0, out, ""}}, nil)
}

func TestReleaseCheckCiGreenFailGhError(t *testing.T) {
	rcAssertFail(t, "ci-green", "0.3.1", map[string]rcRes{"gh": {1, "", "no auth"}}, nil)
}

func TestReleaseCheckCiGreenAsksForPushRunsOfTheSha(t *testing.T) {
	f := newRCFake(t, "0.3.1", nil)
	rcGo(t, f)
	var gh []string
	for _, c := range f.calls {
		if c[0] == "gh" {
			gh = c
			break
		}
	}
	if len(gh) < 3 || strings.Join(gh[:3], " ") != "gh run list" {
		t.Fatalf("gh call %v", gh)
	}
	for _, want := range [][2]string{{"-R", "acme/widget"}, {"--workflow", "tests.yml"}, {"--commit", rcSHA}, {"--event", "push"}} {
		i := -1
		for j, w := range gh {
			if w == want[0] {
				i = j
			}
		}
		if i < 0 || i+1 >= len(gh) || gh[i+1] != want[1] {
			t.Fatalf("gh call %v lacks %v", gh, want)
		}
	}
}

func TestReleaseCheckNotesFailCheckVersion(t *testing.T) {
	// Python: check-version exits 1. Go: the checkout's CHANGELOG.md has no "## [0.3.1]" heading.
	rcAssertFail(t, "notes", "0.3.1", nil, func(f *rcFake) { f.changelog = "# Changelog\n" })
}

func TestReleaseCheckNotesFailPlaceholdersNameLines(t *testing.T) {
	lines := rcAssertFail(t, "notes", "0.3.1", nil, func(f *rcFake) {
		f.notes = "ok\nTODO write\nfine <version> here __NAME__\n### Upgrade\n"
	})
	msg := rcLine(t, lines, "FAIL notes")
	for _, want := range []string{"line 2: TODO", "line 3: <version>", "line 3: __NAME__"} {
		contains(t, msg, want)
	}
}

func TestReleaseCheckSmokeFailVersionOutput(t *testing.T) {
	rcAssertFail(t, "fresh-install-smoke", "0.3.1", map[string]rcRes{"factory-version": {0, "hivemill 9.9.9\n", ""}}, nil)
}

func TestReleaseCheckSmokeVersionMustMatchExactly(t *testing.T) {
	rcAssertFail(t, "fresh-install-smoke", "0.3.1", map[string]rcRes{"factory-version": {0, "hivemill 0.3.10 (git abc)\n", ""}}, nil)
	_, lines := rcGo(t, newRCFake(t, "0.3.1", map[string]rcRes{"factory-version": {0, "hivemill 0.3.1\n", ""}}))
	if s := rcStatus(t, lines, "fresh-install-smoke"); s != "PASS" {
		t.Fatalf("%s: %v", s, lines)
	}
}

func TestReleaseCheckNotesPlaceholdersEachPatternAndClean(t *testing.T) {
	for _, text := range []string{"TODO", "TBD", "FIXME", "XXX", "__FOO__", "<fill me>", "<!-- c -->", "TODOs", "XXXL"} {
		lines := rcAssertFail(t, "notes", "0.3.1", nil, func(f *rcFake) { f.notes = "ok\n" + text + "\n### Upgrade\n" })
		if !strings.Contains(rcLine(t, lines, "FAIL notes"), "line 2:") {
			t.Errorf("%s: no line 2 hit in %v", text, lines)
		}
	}
	f := newRCFake(t, "0.3.1", nil)
	f.notes = "# Notes\nAll good, a < b and c > d\n### Upgrade\n"
	_, lines := rcGo(t, f)
	if s := rcStatus(t, lines, "notes"); s != "PASS" {
		t.Fatalf("%s: %v", s, lines)
	}
}

func TestReleaseCheckFreshboxReminderPrintsAbsoluteCwd(t *testing.T) {
	_, lines := rcGo(t, newRCFake(t, "0.3.0", nil))
	cmd := rcLine(t, lines, "ci/freshbox_test.py")
	root, err := ft.RepoRoot() // no --repo: the tool finds the checkout above the working directory
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("root %q is not absolute", root)
	}
	q := root
	if !regexp.MustCompile(`^[A-Za-z0-9@%+=:,./-]+$`).MatchString(root) {
		q = "'" + root + "'"
	}
	contains(t, cmd, "cd "+q+" && ")
}

func TestReleaseCheckFreshboxReminderPassesRepoRefAndQuotesCd(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a b", "factory")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newRCFake(t, "0.3.0", nil)
	code, lines, _ := rcExec(t, f, []string{"PATH=/usr/bin", "FACTORY_HOME=" + t.TempDir()}, rcSHA, "--repo", root)
	if code != 0 {
		t.Fatalf("exit %d: %v", code, lines)
	}
	cmd := rcLine(t, lines, "ci/freshbox_test.py")
	contains(t, cmd, "  cd '"+root+"' && sudo python3 ci/freshbox_test.py")
	contains(t, cmd, "--repo https://github.com/acme/widget.git")
	if !strings.HasSuffix(cmd, "--ref "+rcSHA) {
		t.Fatalf("no --ref %s at the end: %s", rcSHA, cmd)
	}
}

// rcTermsRun is the Python _terms_run: a factory.db config row and/or the env var feed the terms-file path.
func rcTermsRun(t *testing.T, envTerms, configTerms string) (string, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	con, err := sql.Open("sqlite", filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := con.Exec("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if configTerms != "" {
		if _, err := con.Exec("INSERT INTO config VALUES ('scrub_terms_file', ?)", configTerms); err != nil {
			t.Fatal(err)
		}
	}
	con.Close()
	env := []string{"PATH=/usr/bin", "FACTORY_HOME=" + home}
	if envTerms != "" {
		env = append(env, rcTermsEnv+"="+envTerms)
	}
	f := newRCFake(t, "0.3.1", nil)
	seen := ""
	guard := func(argv []string, cwd string, e []string, to time.Duration) (int, string, string) {
		if len(argv) > 1 && strings.Contains(argv[1], "public_text_guard.py") {
			v := ""
			for _, kv := range e {
				if s, ok := strings.CutPrefix(kv, rcTermsEnv+"="); ok {
					v = s
				}
			}
			if v == "" {
				return 2, "", "public-text-guard: no owner terms loaded"
			}
			seen = v
		}
		return f.run(argv, cwd, e, to)
	}
	var o, e bytes.Buffer
	releasecheck.Exec([]string{rcSHA}, guard, f.probe, env, &o, &e)
	return rcStatus(t, rcLines(o.String()), "public-text"), seen
}

func TestReleaseCheckPublicTextUsesConfigTermsFileWhenEnvUnset(t *testing.T) {
	s, terms := rcTermsRun(t, "", "/cfg/terms")
	if s != "PASS" || terms != "/cfg/terms" {
		t.Fatalf("%s %q", s, terms)
	}
}

func TestReleaseCheckPublicTextFailsClosedWithNeitherSet(t *testing.T) {
	s, terms := rcTermsRun(t, "", "")
	if s != "FAIL" || terms != "" {
		t.Fatalf("%s %q", s, terms)
	}
}

func TestReleaseCheckPublicTextEnvTermsFileWinsOverConfig(t *testing.T) {
	s, terms := rcTermsRun(t, "/env/terms", "/cfg/terms")
	if s != "PASS" || terms != "/env/terms" {
		t.Fatalf("%s %q", s, terms)
	}
}

func TestReleaseCheckSmokeFailDemoSeed(t *testing.T) {
	rcAssertFail(t, "fresh-install-smoke", "0.3.1", map[string]rcRes{"demo_seed.py": {1, "", "boom"}}, nil)
}

func TestReleaseCheckSmokeFailServe(t *testing.T) {
	rcAssertFail(t, "fresh-install-smoke", "0.3.1", nil, func(f *rcFake) { f.probeFail, f.probeDetail = true, "HTTP 500 on /" })
}

func TestReleaseCheckSmokeFailWhenCheckoutFails(t *testing.T) {
	lines := rcAssertFail(t, "fresh-install-smoke", "0.3.1", map[string]rcRes{"git-clone": {128, "", "no space"}}, nil)
	contains(t, strings.Join(lines, "\n"), "no space")
}

func TestReleaseCheckPublicTextFailHits(t *testing.T) {
	rcAssertFail(t, "public-text", "0.3.1", map[string]rcRes{"public_text_guard.py": {1, "", "hit"}}, nil)
}

func TestReleaseCheckPublicTextWithoutTermsFailsClosed(t *testing.T) {
	lines := rcAssertFail(t, "public-text", "0.3.1",
		map[string]rcRes{"public_text_guard.py": {2, "", "public-text-guard: no owner terms loaded"}}, nil)
	contains(t, rcLine(t, lines, "FAIL public-text"), "fails closed")
}

func TestReleaseCheckTagFreeFail(t *testing.T) {
	rcAssertFail(t, "tag-free", "0.3.1", map[string]rcRes{"git-ls-remote": {0, rcSHA + "\trefs/tags/v0.3.1\n", ""}}, nil)
}

func TestReleaseCheckTagFreeFailLsRemoteError(t *testing.T) {
	rcAssertFail(t, "tag-free", "0.3.1", map[string]rcRes{"git-ls-remote": {128, "", "no network"}}, nil)
}

func TestReleaseCheckCursorRuntimePassesOnRealTree(t *testing.T) {
	_, lines := rcGo(t, newRCFake(t, "0.3.1", nil))
	if s := rcStatus(t, lines, "cursor-runtime"); s != "PASS" {
		t.Fatalf("%s: %v", s, lines)
	}
	contains(t, rcLine(t, lines, "cursor-runtime"), "pm/claude")
}

func TestReleaseCheckCursorRuntimeResolveContract(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	snippet := "from scout.agent_runtime import parse_runtimes, resolve_runtime\n" +
		"print(resolve_runtime('pm', ''), parse_runtimes('*=cursor').get('coder'), resolve_runtime('evaluator', '*=cursor'))\n"
	c := exec.Command(py, "-c", snippet)
	c.Dir = root
	c.Env = append(os.Environ(), "PYTHONPATH="+root)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "claude cursor claude" {
		t.Fatalf("got %q, want %q", got, "claude cursor claude")
	}
}

func TestReleaseCheckCursorRuntimeFailMissingAdapter(t *testing.T) {
	path := "roles/hooks/cursor-guard-adapter.py"
	lines := rcAssertFail(t, "cursor-runtime", "0.3.1", map[string]rcRes{"git-show:" + path: {128, "", "missing"}}, nil)
	rcLine(t, lines, "FAIL cursor-runtime:")
}

func TestReleaseCheckUnresolvableShaExit2(t *testing.T) {
	f := newRCFake(t, "0.3.1", map[string]rcRes{"git-rev-parse": {128, "", "Needed a single revision"}})
	code, lines := rcGo(t, f)
	if code != 2 || len(lines) != 0 {
		t.Fatalf("exit %d, stdout %v", code, lines)
	}
}

func TestReleaseCheckUsageExit2(t *testing.T) {
	for _, argv := range [][]string{nil, {"--bogus"}} {
		var o, e bytes.Buffer
		if code := releasecheck.Exec(argv, newRCFake(t, "0.3.1", nil).run, nil, nil, &o, &e); code != 2 {
			t.Errorf("%v: exit %d, want 2", argv, code)
		}
	}
}

func TestReleaseCheckRepoUrlParsingAndOverride(t *testing.T) {
	f := newRCFake(t, "0.3.1", nil)
	rcGo(t, f, "--repo", "o/r")
	found := false
	for _, c := range f.calls {
		if c[0] == "gh" {
			found = found || strings.Contains(" "+strings.Join(c, " ")+" ", " o/r ")
			break
		}
	}
	if !found {
		t.Fatalf("the gh call lacks o/r: %v", f.calls)
	}
	f = newRCFake(t, "0.3.1", map[string]rcRes{"git-remote": {0, "https://example.com/x\n", ""}})
	var o, e bytes.Buffer
	if code := releasecheck.Exec([]string{rcSHA}, f.run, f.probe, nil, &o, &e); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestReleaseCheckNeverMutates(t *testing.T) {
	for _, over := range []map[string]rcRes{{}, {"gh": {0, "[]", ""}}} {
		f := newRCFake(t, "0.3.1", over)
		rcGo(t, f)
		for _, argv := range f.calls {
			if argv[0] == "git" {
				sub := argv[1]
				if argv[1] == "-C" {
					sub = argv[3]
				}
				if sub == "tag" || sub == "push" {
					t.Errorf("mutating call %v", argv)
				}
			}
			if argv[0] == "gh" {
				if len(argv) > 1 && argv[1] == "release" {
					t.Errorf("gh release call %v", argv)
				}
				for _, w := range argv {
					if !strings.HasPrefix(w, "-") && w == "create" {
						t.Errorf("gh create call %v", argv)
					}
				}
			}
		}
	}
}

func rcEnvMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestReleaseCheckSmokeIsolatedAndTempRemoved(t *testing.T) {
	live := "/live/factory/home"
	f := newRCFake(t, "0.3.1", nil)
	rcExec(t, f, []string{"PATH=/usr/bin", "HOME=/live/home", "FACTORY_HOME=" + live, "FACTORY_AGENT=coder-x"}, rcSHA)
	var smoke []map[string]string
	for i, argv := range f.calls {
		j := strings.Join(argv, " ")
		if f.envs[i] != nil && (strings.Contains(j, "bin/factory") || strings.Contains(j, "demo_seed.py")) {
			smoke = append(smoke, rcEnvMap(f.envs[i]))
		}
	}
	if len(smoke) != 3 { // --version, demo_seed, serve
		t.Fatalf("%d smoke calls with an env, want 3", len(smoke))
	}
	tmp := ""
	for _, e := range smoke {
		if e["HOME"] == "/live/home" || e["FACTORY_HOME"] == live {
			t.Errorf("smoke saw the live env: %v", e)
		}
		if e["FACTORY_AGENT"] != "coder-x" { // passed through untouched
			t.Errorf("FACTORY_AGENT %q", e["FACTORY_AGENT"])
		}
		if tmp == "" {
			tmp = filepath.Dir(e["HOME"])
		}
		if !strings.HasPrefix(e["HOME"], tmp) || !strings.HasPrefix(e["FACTORY_HOME"], tmp) {
			t.Errorf("HOME/FACTORY_HOME not under %s: %v", tmp, e)
		}
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Errorf("temp dir %s not removed", tmp)
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), live) {
			t.Errorf("the live home leaked into %v", c)
		}
	}
}

func TestReleaseCheckTempRemovedAfterFailure(t *testing.T) {
	f := newRCFake(t, "0.3.1", nil)
	f.probeFail, f.probeDetail = true, "x"
	rcGo(t, f)
	tmp := ""
	for _, c := range f.calls {
		if len(c) > 1 && c[0] == "git" && c[1] == "clone" {
			tmp = filepath.Dir(c[len(c)-1])
			break
		}
	}
	if tmp == "" {
		t.Fatal("no clone call")
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Errorf("temp dir %s not removed", tmp)
	}
}

// rcRepo is the Python RealGitTest.setUp: a real temp repo with a stub CLI, the cursor runtime files, origin/main
// at the first commit and a "side" commit off main.
type rcRepoFix struct {
	dir, sha, side string
}

func (r *rcRepoFix) git(t *testing.T, args ...string) string {
	t.Helper()
	return hermeticGit(t, r.dir, args...)
}

func newRCRepo(t *testing.T) *rcRepoFix {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	r := &rcRepoFix{dir: t.TempDir()}
	rcWrite(t, filepath.Join(r.dir, "bin", "factory"), "import sys\nprint('hivemill 0.3.1 (git x)' if sys.argv[1:] == ['--version'] else '')\n")
	rcWrite(t, filepath.Join(r.dir, "ci", "demo_seed.py"), "import sys,os\nos.makedirs(sys.argv[2])\n")
	rcWrite(t, filepath.Join(r.dir, "VERSION"), "0.3.1\n")
	// the Go notes check runs in-process on the checkout, so the notes are real files here (Python stubbed changelog.py)
	rcWrite(t, filepath.Join(r.dir, "CHANGELOG.md"), "## [0.3.1] - x\n")
	rcWrite(t, filepath.Join(r.dir, "ci", "release-notes", "0.3.1.md"), "Notes.\n\n### Upgrade\n\nRun it.\n")
	for _, rel := range []string{"scout/cursor_harness.py", "roles/hooks/cursor-guard-adapter.py", "scout/agent_runtime.py", "scout/__init__.py"} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		rcWrite(t, filepath.Join(r.dir, rel), string(b))
	}
	r.git(t, "init", "-q", "-b", "main")
	r.git(t, "remote", "add", "origin", "https://github.com/acme/widget.git")
	r.git(t, "add", ".")
	r.git(t, "commit", "-q", "-m", "one")
	r.sha = strings.TrimSpace(r.git(t, "rev-parse", "HEAD"))
	r.git(t, "update-ref", "refs/remotes/origin/main", "HEAD")
	r.git(t, "checkout", "-q", "-b", "side")
	r.git(t, "commit", "-q", "--allow-empty", "-m", "side")
	r.side = strings.TrimSpace(r.git(t, "rev-parse", "HEAD"))
	return r
}

// run is the Python RealGitTest.runner: gh, ls-remote and the guard are fake; everything else is real.
func (r *rcRepoFix) run(argv []string, cwd string, env []string, to time.Duration) (int, string, string) {
	switch {
	case argv[0] == "gh":
		return 0, rcGreenRun, ""
	case len(argv) > 3 && argv[0] == "git" && argv[1] == "-C" && argv[3] == "ls-remote":
		return 0, "", ""
	case strings.Contains(strings.Join(argv, " "), "public_text_guard.py"):
		return 0, "notes ok\n", ""
	}
	return releasecheck.RunCmd(argv, cwd, env, to)
}

func (r *rcRepoFix) exec(t *testing.T, sha string) (int, []string) {
	t.Helper()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "FACTORY_HOME="+t.TempDir())
	probe := func(argv []string, env []string, port int) (bool, string) { return true, "HTTP 200 on /" }
	var o, e bytes.Buffer
	code := releasecheck.Exec([]string{sha, "--version", "0.3.1", "--repo", r.dir}, r.run, probe, env, &o, &e)
	lines := rcLines(o.String())
	if code != 0 && rcHasTagLine(lines) {
		t.Fatalf("owner commands printed on exit %d: %v", code, lines)
	}
	return code, lines
}

func TestReleaseCheckOnMainAndSmokePassOnRealRepo(t *testing.T) {
	r := newRCRepo(t)
	code, lines := r.exec(t, r.sha)
	got := []string{rcStatus(t, lines, "on-main"), rcStatus(t, lines, "fresh-install-smoke")}
	if got[0] != "PASS" || got[1] != "PASS" || code != 0 {
		t.Fatalf("%v exit %d: %v", got, code, lines)
	}
}

func TestReleaseCheckCommitOffMainFailsOnMainRealRepo(t *testing.T) {
	r := newRCRepo(t)
	code, lines := r.exec(t, r.side)
	if s := rcStatus(t, lines, "on-main"); s != "FAIL" || code != 1 {
		t.Fatalf("%s exit %d: %v", s, code, lines)
	}
	if rcHasTagLine(lines) {
		t.Fatalf("owner commands printed: %v", lines)
	}
}

func TestReleaseCheckSmokeFailsWhenCliIsBrokenRealRepo(t *testing.T) {
	r := newRCRepo(t)
	rcWrite(t, filepath.Join(r.dir, "bin", "factory"), "print('hivemill 0.0.0')\n")
	r.git(t, "commit", "-q", "-am", "break")
	r.git(t, "update-ref", "refs/remotes/origin/main", "HEAD")
	sha := strings.TrimSpace(r.git(t, "rev-parse", "HEAD"))
	code, lines := r.exec(t, sha)
	if s := rcStatus(t, lines, "fresh-install-smoke"); s != "FAIL" || code != 1 {
		t.Fatalf("%s exit %d: %v", s, code, lines)
	}
	if rcHasTagLine(lines) {
		t.Fatalf("owner commands printed: %v", lines)
	}
}

func TestReleaseCheckCliUsageAndBadShaExit2(t *testing.T) {
	bin := buildCmd(t, "ci-release-check")
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"not-a-sha-xyz"}} {
		c := exec.Command(bin, args...)
		c.Dir = root
		var o, e bytes.Buffer
		c.Stdout, c.Stderr = &o, &e
		err := c.Run()
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 {
			t.Errorf("%v: err %v, want exit 2 (stderr %s)", args, err, e.String())
		}
		notContains(t, o.String(), "tag -a")
	}
}
