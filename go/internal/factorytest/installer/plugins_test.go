package installer

// Port of tests/test_plugins.py (9 cases, one top-level func each, prefix TestPlugins). The checkout is read through
// ft.RepoRoot. The spawn case drives `factory spawn`, which the Go CLI still hands to Python (see Env.NeedPython), so it
// is the only case here that needs python3.
// Not ported: none.

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

var (
	plPgWithPlugins = map[string]bool{"coder": true, "director": true, "lead": true, "pm": true, "qa": true,
		"researcher": true, "reviewer": true, "staff-engineer": true}
	plPgBadExt   = map[string]bool{".sh": true, ".py": true, ".js": true, ".ts": true, ".cmd": true, ".ps1": true, ".html": true}
	plPgBadNames = map[string]bool{"hooks": true, "hooks.json": true, ".mcp.json": true}
)

func plPgRoot(t testing.TB) string { return filepath.Join(plHmRepo(t), "roles", "plugins") }

func plPgReal(t testing.TB, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// plPgRoles is ROLES of bin/factory_cli.py (the Python harness reads the same literal).
func plPgRoles(t testing.TB) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)\nROLES = \[(.*?)\]`).FindStringSubmatch(plHmRead(t, "bin", "factory_cli.py"))
	if m == nil {
		t.Fatal("ROLES not found in bin/factory_cli.py")
	}
	var roles []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
		roles = append(roles, q[1])
	}
	return roles
}

func plPgSpawnName(role string) string {
	if role == "retro" {
		return "cursor-zz-test-retro"
	}
	return "zz-test-" + role
}

func plPgSpawnCmd(role string) []string {
	base := []string{"spawn", role, "--name", plPgSpawnName(role)}
	switch role {
	case "coder", "qa", "reviewer", "researcher":
		base = append(base, "--goal", "G-001", "--task", "T-0001")
		if role == "qa" { // T-0956: a no-PR qa task needs an explicit cwd
			base = append(base, "--cwd", os.TempDir())
		}
	}
	return base
}

func plPgExec(t testing.TB, e *ft.Env, q string) {
	t.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q); err != nil {
		t.Fatal(err)
	}
}

func plPgWrite(t testing.TB, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestPluginsPluginDirIffRoleDirExists(t *testing.T) {
	e := ft.New(t)
	e.HerdrStateful()
	plPgExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")        // the Python harness pins the TUI
	e.NeedPython()                                                                        // spawn still runs in Python
	plPgWrite(t, filepath.Join(e.StubBin, "agent"), "#!/bin/sh\necho CURSOR_OK\n", 0o755) // cursor retro/scout spawns need agent on PATH
	// prep(): the researcher is always sandboxed (T-0847): a fake claude and unshare, the three token files, the repo scope
	plPgWrite(t, filepath.Join(e.StubBin, "claude"), "#!/bin/sh\nexit 0\n", 0o755)
	plPgWrite(t, filepath.Join(e.StubBin, "unshare"), "#!/bin/sh\nexit 0\n", 0o755)
	for _, n := range []string{"worker-oauth-token", "gh-worker-ro-token", "gh-worker-rw-token"} {
		plPgWrite(t, filepath.Join(e.UserHome, ".config", "factory", n), "zz-test-token-marker-5c1e9d\n", 0o600)
	}
	plPgExec(t, e, "INSERT INTO config(key,value) VALUES('worker_gh_repos','volekerb/hivemill') ON CONFLICT(key) DO UPDATE SET value=excluded.value")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-lead")
	for _, role := range []string{"reviewer", "qa", "coder"} {
		wt := filepath.Join(e.Home, "worktrees", "w-"+role)
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		if role != "reviewer" {
			if err := os.Mkdir(filepath.Join(wt, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.SeedTask(ft.Task{ID: "T-0001", Role: "coder", Status: "todo", PRURL: "-", Team: "g-001-a"})
	plPgExec(t, e, "UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
	e.OK("config", "max_live_agents", "99")
	e.OK("config", "max_total_claude", "99") // one pane per role now exceeds the default 10

	var spawnable []string
	for _, r := range plPgRoles(t) {
		if r != "evaluator" { // the CLI refuses `factory spawn evaluator`: tests/test_scout_roles.py
			spawnable = append(spawnable, r)
		}
	}
	for _, role := range spawnable {
		e.OK(plPgSpawnCmd(role)...)
	}
	starts := map[string][]string{}
	for _, argv := range e.HerdrStarts() {
		starts[argv[2]] = argv
	}
	for _, role := range spawnable {
		argv := starts[plPgSpawnName(role)]
		var flags []int
		for i, a := range argv {
			if a == "--plugin-dir" {
				flags = append(flags, i)
			}
		}
		pdir := filepath.Join(plPgRoot(t), role)
		if st, err := os.Stat(pdir); err == nil && st.IsDir() {
			if len(flags) != 1 {
				t.Errorf("%s: want one --plugin-dir: %v (starts %v)", role, argv, e.HerdrStarts())
				continue
			}
			if got := plPgReal(t, argv[flags[0]+1]); got != plPgReal(t, pdir) {
				t.Errorf("%s: plugin dir %s, want %s", role, got, pdir)
			}
		} else if len(flags) != 0 {
			t.Errorf("%s got --plugin-dir: %v", role, argv)
		}
		if plPgWithPlugins[role] != (len(flags) > 0) {
			t.Errorf("%s: %v", role, argv)
		}
	}
}

// plPgRoleTrees is role_trees(): roles/plugins/<role>/ for each real role ('goals' and any other name is skipped).
func plPgRoleTrees(t testing.TB) []string {
	t.Helper()
	roles := map[string]bool{}
	for _, r := range plPgRoles(t) {
		roles[r] = true
	}
	ents, err := os.ReadDir(plPgRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, en := range ents {
		p := filepath.Join(plPgRoot(t), en.Name())
		if st, err := os.Stat(p); err == nil && st.IsDir() && roles[en.Name()] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func plPgSubdirs(t testing.TB, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, en := range ents {
		if st, err := os.Stat(filepath.Join(dir, en.Name())); err == nil && st.IsDir() {
			out = append(out, en.Name())
		}
	}
	sort.Strings(out)
	return out
}

func TestPluginsEveryChildIsAPlugin(t *testing.T) {
	want := map[string][]string{
		"coder":          {"coder-skills"},
		"director":       {"factory-director"},
		"lead":           {"factory-lead"},
		"pm":             {"pm-skills"},
		"qa":             {"qa-skills"},
		"researcher":     {"research-skills"},
		"reviewer":       {"reviewer-skills"},
		"staff-engineer": {"feature-dev", "pr-review-toolkit", "staff-engineer-skills"},
	}
	kids := map[string][]string{}
	for _, tree := range plPgRoleTrees(t) {
		kids[filepath.Base(tree)] = plPgSubdirs(t, tree)
	}
	gj, _ := json.Marshal(kids)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("plugin dirs = %s, want %s", gj, wj)
	}
	for _, tree := range plPgRoleTrees(t) {
		for _, k := range kids[filepath.Base(tree)] {
			pj := filepath.Join(tree, k, ".claude-plugin", "plugin.json")
			b, err := os.ReadFile(pj)
			if err != nil {
				t.Errorf("%s/%s: %v", filepath.Base(tree), k, err)
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			if n, _ := m["name"].(string); n == "" {
				t.Errorf("%s/%s: no name", filepath.Base(tree), k)
			}
		}
	}
}

func plPgLeadSkills(t testing.TB) string {
	return filepath.Join(plPgRoot(t), "lead", "factory-lead", "skills")
}

func TestPluginsLeadSkillsFrontMatter(t *testing.T) {
	skills := plPgLeadSkills(t)
	ents, err := os.ReadDir(skills)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	plHmEq(t, names, []string{"audit-round", "blocked-worker", "fix-loop", "park", "pr-triage", "research-flow", "spec-change", "worker-brief"})
	fmRe := regexp.MustCompile(`(?s)^---\n(.*?)\n---\n`)
	nameRe := regexp.MustCompile(`(?m)^name:\s*(\S+)\s*$`)
	descRe := regexp.MustCompile(`(?m)^description:\s*(.+)$`)
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(skills, n, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		m := fmRe.FindStringSubmatch(text)
		if m == nil {
			t.Errorf("%s: no front matter", n)
			continue
		}
		nm := nameRe.FindStringSubmatch(m[1])
		if nm == nil || nm[1] != n {
			t.Errorf("%s: name = %v", n, nm)
		}
		dm := descRe.FindStringSubmatch(m[1])
		if dm == nil {
			t.Errorf("%s: no description", n)
		} else if !strings.Contains(dm[1], "Use when") {
			t.Errorf("%s: description lacks 'Use when'", n)
		}
		if l := len(plHmLines(text)); l >= 70 {
			t.Errorf("%s: %d lines, want < 70", n, l)
		}
	}
}

func TestPluginsLeadSkillContents(t *testing.T) {
	skills := plPgLeadSkills(t)
	read := func(n string) string {
		b, err := os.ReadFile(filepath.Join(skills, n, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	sc := read("spec-change")
	for _, n := range []string{"factory stop", "--acceptance", "factory spawn"} {
		plHmContains(t, sc, n)
	}
	wb := read("worker-brief")
	for _, n := range []string{"factory inbox", "factory verdict", "goal show", "merge origin/main", "Test count:",
		"pilot-test-first", "docs/test-first-pilot.md"} {
		plHmContains(t, wb, n)
	}
	pt := read("pr-triage")
	plHmContains(t, pt, "gh pr view")
	plHmContains(t, pt, "mergeable")
	if strings.Index(pt, "gh pr view") >= strings.Index(pt, "gh pr checks") {
		t.Error("gh pr view must come before gh pr checks")
	}
}

func TestPluginsTestFirstPilotDoc(t *testing.T) {
	doc := plHmRead(t, "docs", "test-first-pilot.md")
	for _, n := range []string{"pilot-test-first", "byte-identical", "factory src origin/main", "grep '^-' | grep -v '^--- a/'", "5"} {
		plHmContains(t, doc, n)
	}
}

func TestPluginsNoCodeHooksOrMcp(t *testing.T) {
	repo := plPgReal(t, plHmRepo(t))
	namedWorkflow := filepath.Join(repo, "roles", "workflows", "multi-angle-review.js")
	probeWorkflow := filepath.Join(repo, "roles", "workflows", "guard-probe.js")
	var bad []string
	for _, tree := range plPgRoleTrees(t) {
		err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == tree {
				return nil
			}
			n := d.Name()
			isDir := d.IsDir()
			if d.Type()&fs.ModeSymlink != 0 { // os.walk lists a symlink to a directory with the dirs
				if st, err := os.Stat(p); err == nil && st.IsDir() {
					isDir = true
				}
			}
			if plPgBadNames[n] || plPgBadExt[strings.ToLower(filepath.Ext(n))] {
				ok := false
				if strings.HasSuffix(p, "/workflows/multi-angle-review.js") && plPgReal(t, p) == namedWorkflow {
					ok = true // the one allowed .js: a symlink to the single vendored workflow (T-0366)
				}
				if strings.HasSuffix(p, "/qa/qa-skills/workflows/guard-probe.js") && plPgReal(t, p) == probeWorkflow {
					ok = true // the qa-only guard-probe symlink (T-0751)
				}
				if !ok {
					bad = append(bad, p)
				}
			}
			if !isDir {
				st, err := os.Stat(p)
				if err != nil {
					return err
				}
				if st.Mode()&0o111 != 0 {
					bad = append(bad, p+" (executable)")
				}
				if n == "plugin.json" {
					b, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					var data map[string]any
					if err := json.Unmarshal(b, &data); err != nil {
						return err
					}
					for _, key := range []string{"hooks", "mcpServers"} {
						if _, has := data[key]; has {
							bad = append(bad, p+" has "+key)
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("bad entries: %q", bad)
	}
}

func plPgSESkills(t testing.TB) (plugins, skills string) {
	plugins = filepath.Join(plPgRoot(t), "staff-engineer")
	return plugins, filepath.Join(plugins, "staff-engineer-skills", "skills")
}

// plPgMarkdown returns every *.md under dir (rglob), sorted.
func plPgMarkdown(t testing.TB, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func plPgReadFile(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPluginsOutputRewiredToFactoryReport(t *testing.T) {
	plugins, skills := plPgSESkills(t)
	noRe := regexp.MustCompile(`(?i)tailwind|cdn\.|mermaid|HTML-REPORT|<html`)
	for _, name := range []string{"improve-codebase-architecture", "differential-review"} {
		var parts []string
		for _, f := range plPgMarkdown(t, filepath.Join(skills, name)) {
			parts = append(parts, plPgReadFile(t, f))
		}
		text := strings.Join(parts, "\n")
		if !strings.Contains(text, "factory report") {
			t.Errorf("%s: no 'factory report'", name)
		}
		if m := noRe.FindString(text); m != "" {
			t.Errorf("%s: matches %q", name, m)
		}
	}
	da := plPgReadFile(t, filepath.Join(skills, "differential-review", "SKILL.md"))
	m := regexp.MustCompile(`(?m)^allowed-tools:(.*)$`).FindStringSubmatch(da)
	if m == nil {
		t.Fatal("no allowed-tools line")
	}
	tools := map[string]bool{}
	for _, f := range strings.Fields(m[1]) {
		tools[f] = true
	}
	if tools["Write"] {
		t.Error("Write is allowed")
	}
	for _, need := range []string{"Read", "Grep", "Glob", "Bash"} {
		if !tools[need] {
			t.Errorf("missing tool %s", need)
		}
	}
	lic := plPgReadFile(t, filepath.Join(plugins, "staff-engineer-skills", "LICENSE-trailofbits"))
	for _, n := range []string{"github.com/trailofbits/skills", "196a9073ab10d35bacf111f945e02c00b2c6883a", "CC-BY-SA-4.0",
		"output rewired to factory report; Write removed"} {
		plHmContains(t, lic+da, n)
	}
}

func TestPluginsNoFileOutputInstructions(t *testing.T) {
	plugins, skills := plPgSESkills(t)
	pat := regexp.MustCompile(`(?i)output file|create[^.\n]{0,30}lazily|(add|update|write)[^.\n]{0,30}GLOSSARY\.md|grilling`)
	var bad []string
	for _, name := range []string{"improve-codebase-architecture", "differential-review"} {
		for _, f := range plPgMarkdown(t, filepath.Join(skills, name)) {
			rel, _ := filepath.Rel(plugins, f)
			for n, line := range plHmLines(plPgReadFile(t, f)) {
				if !strings.Contains(line, "Modified:") && pat.MatchString(line) {
					bad = append(bad, rel+":"+strconv.Itoa(n+1)+": "+strings.TrimSpace(line))
				}
			}
		}
	}
	if len(bad) != 0 {
		t.Fatalf("file-output instructions: %q", bad)
	}
	ents, err := os.ReadDir(filepath.Join(skills, "systematic-debugging"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^(CREATION-LOG|test-)`)
	for _, e := range ents {
		if re.MatchString(e.Name()) {
			t.Errorf("%s", e.Name())
		}
	}
}

func TestPluginsLicencesPresent(t *testing.T) {
	plugins, _ := plPgSESkills(t)
	for _, f := range []string{"LICENSE-mattpocock", "LICENSE-trailofbits", "LICENSE-superpowers"} {
		st, err := os.Stat(filepath.Join(plugins, "staff-engineer-skills", f))
		if err != nil || st.Size() <= 500 {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, p := range []string{"feature-dev", "pr-review-toolkit"} {
		if st, err := os.Stat(filepath.Join(plugins, p, "LICENSE")); err != nil || !st.Mode().IsRegular() {
			t.Errorf("%s/LICENSE: %v", p, err)
		}
	}
}
