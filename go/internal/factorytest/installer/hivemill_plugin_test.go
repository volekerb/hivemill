package installer

// Port of tests/test_hivemill_plugin.py (27 cases, one top-level func each, prefix TestHivemillPlugin). Files are
// read from the real checkout (ft.RepoRoot). The skill lint of tests/test_skill_authoring.py is re-implemented below.
// Not ported: none.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	ft "factory/go/internal/factorytest"
)

const (
	plHmSkillRel = "plugins/hivemill/skills/setup/SKILL.md"
	plHmIosRel   = "plugins/hivemill/skills/ios-testflight-release/SKILL.md"
)

var (
	plHmRequired = []string{
		"Never ask for, accept or repeat a token, password or API key in this chat.",
		"If the user pastes one, do not use it. Tell them to revoke it and create a new one in their own terminal.",
		"Do not run the setup steps yourself. The installer does them; if it fails, stop and report.",
	}
	plHmForbidden = []string{"git clone", "factory init", "factory up", "systemctl", "curl", "--with-token",
		"GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}
	plHmSecretPrefixes = []string{"ghp_", "github_pat_", "sk-ant-"}

	plNameRe    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	plTriggerRe = regexp.MustCompile(`(?i)\buse (when|whenever)\b`)
	plFmKeyRe   = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*(.*)$`)
)

func plHmRepo(t testing.TB) string {
	t.Helper()
	r, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func plHmRead(t testing.TB, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{plHmRepo(t)}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func plHmJSON(t testing.TB, parts ...string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(plHmRead(t, parts...)), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func plHmExists(t testing.TB, parts ...string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(append([]string{plHmRepo(t)}, parts...)...))
	return err == nil
}

func plHmMarketplace(t testing.TB) map[string]any {
	return plHmJSON(t, ".claude-plugin", "marketplace.json")
}

func plHmManifest(t testing.TB) map[string]any {
	return plHmJSON(t, "plugins", "hivemill", ".claude-plugin", "plugin.json")
}

func plHmSkill(t testing.TB) string {
	return plHmRead(t, "plugins", "hivemill", "skills", "setup", "SKILL.md")
}

func plHmIosSkill(t testing.TB) string {
	return plHmRead(t, "plugins", "hivemill", "skills", "ios-testflight-release", "SKILL.md")
}

func plHmIosReadme(t testing.TB) string {
	return plHmRead(t, "plugins", "hivemill", "scripts", "ios-release", "README.md")
}

func plHmStr(t testing.TB, m map[string]any, k string) string {
	t.Helper()
	s, _ := m[k].(string)
	return s
}

func plHmObj(m map[string]any, k string) map[string]any {
	o, _ := m[k].(map[string]any)
	return o
}

func plHmContains(t testing.TB, hay, needle string) {
	t.Helper()
	if !strings.Contains(hay, needle) {
		t.Errorf("missing %q", needle)
	}
}

func plHmAbsent(t testing.TB, hay, needle string) {
	t.Helper()
	if strings.Contains(hay, needle) {
		t.Errorf("unexpected %q", needle)
	}
}

func plHmForbiddenHits(text string) []string {
	var out []string
	for _, f := range plHmForbidden {
		if strings.Contains(text, f) {
			out = append(out, f)
		}
	}
	return out
}

func plHmLines(text string) []string {
	if text == "" {
		return nil
	}
	l := strings.Split(text, "\n")
	if l[len(l)-1] == "" { // Python splitlines: no trailing empty line
		l = l[:len(l)-1]
	}
	return l
}

func plUnquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == v[len(v)-1] && (v[0] == '\'' || v[0] == '"') {
		return v[1 : len(v)-1]
	}
	return v
}

// plParseFrontMatter is parse_front_matter of tests/test_skill_authoring.py: top-level scalar keys, nil when none.
func plParseFrontMatter(text string) map[string]string {
	lines := strings.Split(text, "\n")
	if strings.TrimRight(lines[0], " \t\r\f\v") != "---" {
		return nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r\f\v") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil
	}
	out := map[string]string{}
	i := 1
	for i < end {
		m := plFmKeyRe.FindStringSubmatch(lines[i])
		i++
		if m == nil {
			continue
		}
		key, val := m[1], strings.TrimSpace(m[2])
		if val != "" && (val[0] == '>' || val[0] == '|') {
			var parts []string
			for i < end && (strings.TrimSpace(lines[i]) == "" || lines[i][0] == ' ' || lines[i][0] == '\t') {
				if strings.TrimSpace(lines[i]) != "" {
					parts = append(parts, strings.TrimSpace(lines[i]))
				}
				i++
			}
			val = strings.Join(parts, " ")
		} else {
			val = plUnquote(val)
		}
		out[key] = val
	}
	return out
}

// plLintSkill is lint_skill of tests/test_skill_authoring.py.
func plLintSkill(relpath, text string) []string {
	fm := plParseFrontMatter(text)
	if fm == nil {
		return []string{relpath + ": no front matter"}
	}
	var found []string
	name, desc := fm["name"], fm["description"]
	if name == "" {
		found = append(found, relpath+": missing name")
	} else {
		if !plNameRe.MatchString(name) {
			found = append(found, relpath+": name "+strconv.Quote(name)+" must be lowercase letters, digits and single hyphens")
		}
		if n := utf8.RuneCountInString(name); n > 64 {
			found = append(found, relpath+": name is "+strconv.Itoa(n)+" chars, max 64")
		}
		parent := ""
		if strings.Contains(relpath, "/") {
			p := strings.Split(relpath, "/")
			if len(p) >= 2 {
				parent = p[len(p)-2]
			}
		}
		if name != parent {
			found = append(found, relpath+": name "+strconv.Quote(name)+" != directory "+strconv.Quote(parent))
		}
	}
	if desc == "" {
		found = append(found, relpath+": missing description")
	} else {
		if n := utf8.RuneCountInString(desc); n > 1024 {
			found = append(found, relpath+": description is "+strconv.Itoa(n)+" chars, max 1024")
		}
		if !strings.HasPrefix(relpath, "roles/plugins/staff-engineer/") && !plTriggerRe.MatchString(desc) {
			found = append(found, relpath+": description has no 'Use when' trigger")
		}
	}
	return found
}

func plHmNeedLint(t *testing.T, rel, text string) {
	t.Helper()
	if got := plLintSkill(rel, text); len(got) != 0 {
		t.Fatalf("lint: %v", got)
	}
}

func plHmLinesWith(text string, match func(string) bool) []string {
	var out []string
	for _, l := range plHmLines(text) {
		if match(l) {
			out = append(out, l)
		}
	}
	return out
}

func plHmEq(t testing.TB, got, want []string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") || len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// ---- ManifestTests -------------------------------------------------------------------------------------------

func TestHivemillPluginMarketplaceFields(t *testing.T) {
	m := plHmMarketplace(t)
	if m["name"] != "hivemill" {
		t.Errorf("name = %v", m["name"])
	}
	if plHmStr(t, plHmObj(m, "owner"), "name") == "" {
		t.Error("owner.name empty")
	}
	plugins, _ := m["plugins"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("plugins = %d, want 1", len(plugins))
	}
	e, _ := plugins[0].(map[string]any)
	if e["name"] != "hivemill" || e["source"] != "./plugins/hivemill" {
		t.Errorf("entry = %v", e)
	}
	if _, ok := e["version"]; ok {
		t.Error("entry has version")
	}
}

func TestHivemillPluginPluginFields(t *testing.T) {
	p := plHmManifest(t)
	if p["name"] != "hivemill" {
		t.Errorf("name = %v", p["name"])
	}
	if plHmStr(t, p, "description") == "" {
		t.Error("description empty")
	}
	if plHmStr(t, plHmObj(p, "author"), "name") == "" {
		t.Error("author.name empty")
	}
	if _, ok := p["version"]; ok { // users track commits
		t.Error("plugin.json has version")
	}
	plugins, _ := plHmMarketplace(t)["plugins"].([]any)
	e, _ := plugins[0].(map[string]any)
	if p["name"] != e["name"] {
		t.Errorf("plugin name %v != marketplace %v", p["name"], e["name"])
	}
}

func TestHivemillPluginSourceDirExistsAndHasInstaller(t *testing.T) {
	st, err := os.Stat(filepath.Join(plHmRepo(t), "plugins", "hivemill", "scripts", "install"))
	if err != nil || !st.Mode().IsRegular() {
		t.Fatalf("plugins/hivemill/scripts/install is not a file: %v", err)
	}
}

func TestHivemillPluginNoHooksOrMcp(t *testing.T) {
	for _, p := range [][]string{{"plugins", "hivemill", "hooks"}, {"plugins", "hivemill", ".mcp.json"},
		{"plugins", "hivemill", ".claude-plugin", "hooks.json"}} {
		if plHmExists(t, p...) {
			t.Errorf("%v exists", p)
		}
	}
	m := plHmManifest(t)
	for _, k := range []string{"hooks", "mcpServers"} {
		if _, ok := m[k]; ok {
			t.Errorf("plugin.json has %s", k)
		}
	}
}

func TestHivemillPluginNoRepoRootClaudeSkills(t *testing.T) {
	if plHmExists(t, ".claude", "skills") {
		t.Error(".claude/skills exists")
	}
}

// ---- SkillTests ----------------------------------------------------------------------------------------------

func TestHivemillPluginLint(t *testing.T) { plHmNeedLint(t, plHmSkillRel, plHmSkill(t)) }

func TestHivemillPluginRequiredTextOnce(t *testing.T) {
	text := plHmSkill(t)
	for _, r := range plHmRequired {
		if n := strings.Count(text, r); n != 1 {
			t.Errorf("%q appears %d times", r, n)
		}
	}
}

func TestHivemillPluginNoForbiddenText(t *testing.T) {
	if h := plHmForbiddenHits(plHmSkill(t)); len(h) != 0 {
		t.Fatalf("forbidden: %v", h)
	}
}

func TestHivemillPluginForbiddenDetectorRejects(t *testing.T) {
	plHmEq(t, plHmForbiddenHits(plHmSkill(t)+"\ncurl https://x\n"), []string{"curl"})
}

func TestHivemillPluginFlagsExistInInstaller(t *testing.T) {
	installer := plHmRead(t, "go", "internal", "install", "args.go")
	// Python: (?<![\w-])(--[a-z][a-z-]+); RE2 has no lookbehind, so the preceding character is matched instead.
	re := regexp.MustCompile(`(?:^|[^\w-])(--[a-z][a-z-]+)`)
	flags := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(plHmSkill(t), -1) {
		flags[m[1]] = true
	}
	if !flags["--yes"] {
		t.Error("--yes not in the skill")
	}
	for f := range flags {
		if !strings.Contains(installer, `"`+f+`"`) {
			t.Errorf("%s not in go/internal/install/args.go", f)
		}
	}
}

func TestHivemillPluginThin(t *testing.T) {
	if n := len(plHmLines(plHmSkill(t))); n > 90 {
		t.Fatalf("%d lines, max 90", n)
	}
}

func TestHivemillPluginNoSecretPrefixesInSkillAndManifests(t *testing.T) {
	// the installer source holds redaction regexes with these prefixes, so it is not scanned
	for _, p := range [][]string{{"plugins", "hivemill", "skills", "setup", "SKILL.md"},
		{"plugins", "hivemill", ".claude-plugin", "plugin.json"}, {".claude-plugin", "marketplace.json"}} {
		text := plHmRead(t, p...)
		for _, s := range plHmSecretPrefixes {
			plHmAbsent(t, text, s)
		}
	}
}

func TestHivemillPluginInstallerOnlyAllowedTools(t *testing.T) {
	got := plHmLinesWith(plHmSkill(t), func(l string) bool { return strings.HasPrefix(l, "allowed-tools:") })
	plHmEq(t, got, []string{"allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/install *)"})
}

func TestHivemillPluginGhLoginNotViaBang(t *testing.T) {
	text := plHmSkill(t)
	plHmContains(t, text, "separate terminal")
	plHmContains(t, text, "never through `!`")
}

func TestHivemillPluginFreshBoxDoesNotDeadEnd(t *testing.T) {
	text := plHmSkill(t)
	plHmContains(t, text, "`herdr` or `gh` `missing`")
	plHmContains(t, text, "the installer installs them")
}

func TestHivemillPluginScoutDefaultOff(t *testing.T) {
	text := plHmSkill(t)
	line := plHmLinesWith(text, func(l string) bool { return strings.Contains(l, "scout, the skills triage helper") })
	if len(line) != 1 {
		t.Fatalf("scout lines = %d, want 1", len(line))
	}
	plHmContains(t, line[0], "default off")
	plHmContains(t, line[0], "pass `--no-scout` unless")
	plHmAbsent(t, text, "default yes")
}

func TestHivemillPluginAptLineNotClaimedInCheck(t *testing.T) {
	text := plHmSkill(t)
	plHmContains(t, text, "not in the `--check` report")
	plHmAbsent(t, text, "the apt line the report printed")
}

// ---- IosReleaseNodeTests -------------------------------------------------------------------------------------

func TestHivemillPluginIosReleaseNodeTests(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is not installed but CI is set: the ios-release tests must run in CI")
		}
		t.Skip("node is not installed")
	}
	out, _ := exec.Command(node, "-p", "process.versions.node.split('.')[0]").Output()
	major := strings.TrimSpace(string(out))
	if n, err := strconv.Atoi(major); err != nil || n < 20 {
		if major == "" {
			major = "unknown"
		}
		if os.Getenv("CI") != "" {
			t.Fatalf("node >= 20 is required in CI, found %s", major)
		}
		t.Skip("node >= 20 is required")
	}
	repo := plHmRepo(t)
	tests, _ := filepath.Glob(filepath.Join(repo, "plugins/hivemill/scripts/ios-release/test/*.test.mjs"))
	if len(tests) == 0 {
		t.Fatal("no *.test.mjs files under plugins/hivemill/scripts/ios-release/test")
	}
	sort.Strings(tests)
	cmd := exec.Command(node, append([]string{"--test", "--test-reporter=tap"}, tests...)...)
	cmd.Dir = repo
	done := time.AfterFunc(300*time.Second, func() { _ = cmd.Process.Kill() })
	b, err := cmd.CombinedOutput()
	done.Stop()
	if err != nil {
		tail := string(b)
		if len(tail) > 3000 {
			tail = tail[len(tail)-3000:]
		}
		t.Fatalf("node --test: %v\n%s", err, tail)
	}
	plHmContains(t, string(b), "# fail 0")
}

// ---- IosReleaseSkillTests ------------------------------------------------------------------------------------

var plHmIosRules = []string{
	"Never ask for, accept or repeat an App Store Connect key, key id or issuer id in this chat.",
	"Never read, print or cat a credential file or a .p8 key.",
	"Always run --dry-run first and show its result before a real upload.",
	"Run a real upload only when the user asked for one in this conversation.",
	"Do not run xcodebuild or call the App Store Connect API yourself. release.sh does them; " +
		"if it fails, stop and report the exit code.",
	"Report the final JSON summary line and the exit code; do not paste the full log.",
}

const plHmIosAllowed = "allowed-tools: Bash(bash ${CLAUDE_PLUGIN_ROOT}/scripts/ios-release/release.sh *)"

func TestHivemillPluginIosLintClean(t *testing.T) { plHmNeedLint(t, plHmIosRel, plHmIosSkill(t)) }

func TestHivemillPluginIosRulesLinesExactlyOnce(t *testing.T) {
	lines := plHmLines(plHmIosSkill(t))
	for _, rule := range plHmIosRules {
		n := 0
		for _, l := range lines {
			if l == "- "+rule {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%q appears %d times", rule, n)
		}
	}
}

func TestHivemillPluginIosAllowedToolsLine(t *testing.T) {
	got := plHmLinesWith(plHmIosSkill(t), func(l string) bool { return strings.HasPrefix(l, "allowed-tools:") })
	plHmEq(t, got, []string{plHmIosAllowed})
}

func TestHivemillPluginIosThinAndModelInvocable(t *testing.T) {
	text := plHmIosSkill(t)
	if n := len(plHmLines(text)); n > 80 {
		t.Errorf("%d lines, max 80", n)
	}
	plHmAbsent(t, text, "disable-model-invocation")
	plHmContains(t, strings.ToLower(text), "beta app review")
}

func TestHivemillPluginIosNoSecretPrefixes(t *testing.T) {
	for name, text := range map[string]string{"SKILL.md": plHmIosSkill(t), "README.md": plHmIosReadme(t)} {
		for _, bad := range append(append([]string{}, plHmSecretPrefixes...), "-----BEGIN") {
			if strings.Contains(text, bad) {
				t.Errorf("%s contains %s", name, bad)
			}
		}
	}
}

func TestHivemillPluginIosReadmeExitCodes(t *testing.T) {
	re := regexp.MustCompile(`(?m)^\| (\d) \|`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(plHmIosReadme(t), -1) {
		seen[m[1]] = true
	}
	var got []string
	for k := range seen {
		got = append(got, k)
	}
	sort.Strings(got)
	plHmEq(t, got, []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"})
}

func TestHivemillPluginIosReadmeHasEveryHelpFlag(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("bash is not installed but CI is set")
		}
		t.Skip("bash is not installed")
	}
	readme := plHmIosReadme(t)
	cmd := exec.Command(bash, filepath.Join(plHmRepo(t), "plugins", "hivemill", "scripts", "ios-release", "release.sh"), "--help")
	done := time.AfterFunc(60*time.Second, func() { _ = cmd.Process.Kill() })
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	done.Stop()
	if err != nil {
		t.Fatalf("release.sh --help: %v\n%s", err, stderr.String())
	}
	flags := map[string]bool{}
	for _, f := range regexp.MustCompile(`--[a-z][a-z-]*`).FindAllString(string(out), -1) {
		flags[f] = true
	}
	if !flags["--signing"] || !flags["--dry-run"] {
		t.Errorf("flags = %v", flags)
	}
	for f := range flags {
		if !strings.Contains(readme, f) {
			t.Errorf("README lacks %s", f)
		}
	}
}

func TestHivemillPluginIosReadmeRequiredText(t *testing.T) {
	readme := plHmIosReadme(t)
	for _, needle := range []string{"xcodeAccount", "apiKey", "Admin", "App Manager", "Cloud signing permission error",
		"/usr/bin:/bin:/usr/sbin:/sbin", "rsync", "forbiddenWorktrees", "heavySlotCommand",
		"{build}", "submitExternal", "hasAccessToAllBuilds", "beta app review",
		"An App Manager key cannot cloud-sign", "Settings > Accounts", "chmod 600",
		"security add-generic-password", "MISSING_EXPORT_COMPLIANCE", "--resume-build"} {
		plHmContains(t, readme, needle)
	}
	plHmAbsent(t, readme, `"heavySlot"`)
}

func TestHivemillPluginIosReadmeHasEveryExampleKey(t *testing.T) {
	var example map[string]json.RawMessage
	if err := json.Unmarshal([]byte(plHmRead(t, "plugins", "hivemill", "scripts", "ios-release", "ios-release.example.json")), &example); err != nil {
		t.Fatal(err)
	}
	readme := plHmIosReadme(t)
	for k := range example {
		plHmContains(t, readme, k)
	}
}
