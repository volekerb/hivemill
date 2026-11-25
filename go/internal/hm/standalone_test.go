package hm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGuardExportIsWrittenForAStandaloneHome(t *testing.T) {
	r, s := testRunner(t, "")
	_ = s.SetFactoryCfg("public_text_repos", "zz-test/www")
	_, _ = s.db.Exec(`INSERT INTO agents(name,role,goal_id,task_id,status) VALUES('claude-coder-t-0001-a7','task','G-001','T-0001','live')`)
	_, _ = s.db.Exec(`UPDATE tasks SET worktree='/wt/G-001-T-0001' WHERE id='T-0001'`)
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	if err := r.WriteGuardExport(now); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(r.Env.Home, "run", "guard-export.json")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	var doc struct {
		Version     int               `json:"version"`
		GeneratedAt string            `json:"generated_at"`
		Config      map[string]string `json:"config"`
		AgentGoals  map[string]string `json:"agent_goals"`
		Worktrees   map[string]string `json:"task_worktrees"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 || doc.GeneratedAt != "2026-11-25T08:00:00+00:00" || doc.Config["public_text_repos"] != "zz-test/www" ||
		doc.Config["test_sandbox"] != "0" || len(doc.Config) != 5 || doc.AgentGoals["claude-coder-t-0001-a7"] != "G-001" ||
		doc.Worktrees["T-0001"] != "/wt/G-001-T-0001" {
		t.Errorf("export: %s", b)
	}
	if _, err := os.Stat(filepath.Join(r.Env.Home, "ci", "scrub_allowlist.txt")); err != nil {
		t.Errorf("the pass gives the home its allowlist: %v", err)
	}
	// a factory checkout: the Python tick owns the file
	_ = os.Remove(p)
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(r.Env.Home, "bin", "factory_cli.py"), []byte("#"), 0o644)
	if err := r.WriteGuardExport(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("the runner must not write the export of a factory checkout")
	}
}

func TestTierRulesFallBackToTheBuiltInCopy(t *testing.T) {
	r, s := testRunner(t, "")
	rules, from, err := r.TierRules("www")
	if err != nil || from != "built-in ci/tier_rules.json" || len(rules.SensitivePaths) == 0 {
		t.Fatalf("no home file: %q %v %v", from, rules.SensitivePaths, err)
	}
	// the home's file wins over the built-in one (a factory checkout keeps its own rules)
	home := filepath.Join(r.Env.Home, "ci", "tier_rules.json")
	_ = os.MkdirAll(filepath.Dir(home), 0o755)
	homeRules := `{"docs_globs":["docs/**"],"docs_excluded_prefixes":[],"tests_globs":[],"sensitive_paths":["home/**"],` +
		`"sensitive_defs":{},"small_lines":10,"large_lines":20}`
	_ = os.WriteFile(home, []byte(homeRules), 0o644)
	if rules, from, err = r.TierRules("www"); err != nil || from != home || rules.SensitivePaths[0] != "home/**" {
		t.Fatalf("home file: %q %v %v", from, rules.SensitivePaths, err)
	}
	// a project rules file wins over the home's; its sensitive_paths replace the file's
	proj := filepath.Join(t.TempDir(), "rules.json")
	_ = os.WriteFile(proj, []byte(strings.Replace(homeRules, "home/**", "proj/**", 1)), 0o644)
	_ = s.SetProfile(ProjectProfile{ID: "www", TierRules: proj})
	if rules, from, err = r.TierRules("www"); err != nil || from != proj || rules.SensitivePaths[0] != "proj/**" {
		t.Fatalf("project file: %q %v %v", from, rules.SensitivePaths, err)
	}
	_ = s.SetProfile(ProjectProfile{ID: "www", SensitivePaths: []string{"infra/**", "package.json"}})
	if rules, from, err = r.TierRules("www"); err != nil || !strings.HasSuffix(from, "with the project's sensitive_paths") ||
		strings.Join(rules.SensitivePaths, ",") != "infra/**,package.json" || rules.SmallLines != 10 {
		t.Fatalf("sensitive paths: %q %v %v", from, rules, err)
	}
	// another project keeps the home's rules
	if rules, _, _ = r.TierRules("other"); rules.SensitivePaths[0] != "home/**" {
		t.Errorf("other project: %v", rules.SensitivePaths)
	}
	// a broken file is an error (the tier stays undecided), never a silent fallback
	_ = os.WriteFile(home, []byte("{"), 0o644)
	if _, _, err = r.TierRules("other"); err == nil {
		t.Error("a broken home file must not fall back")
	}
}

func TestStandaloneSettingsCallHivemillGuard(t *testing.T) {
	home := filepath.Join(t.TempDir(), "hive mill")
	old := SelfBinary
	SelfBinary = func() (string, error) { return "/opt/hm/bin/hivemill", nil }
	t.Cleanup(func() { SelfBinary = old })
	e := Env{Home: home, UserHome: t.TempDir()}
	p, err := RenderSettings(e, "task")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	cmds := hookCommands(t, b)
	want := StandaloneGuardCommand("/opt/hm/bin/hivemill", home) + " --role coder"
	if len(cmds) != 3 {
		t.Fatalf("PreToolUse hooks: %q", cmds)
	}
	for _, c := range cmds {
		if c != want {
			t.Errorf("hook %q, want %q", c, want)
		}
	}
	// a home with the repo's shim renders exactly as before
	_ = os.MkdirAll(filepath.Join(home, "roles", "hooks"), 0o755)
	_ = os.WriteFile(GuardShim(home), []byte("#!/bin/sh\n"), 0o755)
	p, err = RenderSettings(e, "task")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	src, _ := Asset("task.json.in")
	q, _ := json.Marshal(home)
	plain := strings.ReplaceAll(string(src), "@FACTORY_LIVE_HOME@", string(q[1:len(q)-1]))
	plain = strings.ReplaceAll(plain, "@FACTORY_HOME_DIR@", e.UserHome)
	plain = strings.ReplaceAll(plain, "@FACTORY_USER_CONFIG@", filepath.Join(e.UserHome, ".config"))
	if string(b) != plain {
		t.Error("a home with roles/hooks/worker-guard must render byte for byte as before")
	}
	for _, c := range hookCommands(t, b) {
		if c != "sh "+home+"/roles/hooks/worker-guard --role coder" {
			t.Errorf("repo home hook %q", c)
		}
	}
}

func hookCommands(t *testing.T, settings []byte) []string {
	t.Helper()
	var doc struct {
		Hooks struct {
			PreToolUse []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(settings, &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range doc.Hooks.PreToolUse {
		for _, h := range m.Hooks {
			out = append(out, h.Command)
		}
	}
	return out
}

func TestPRCreateOpensTheOwnTasksPR(t *testing.T) {
	r, s := testRunner(t, "claude-coder-t-0001-a7")
	var ghArgs []string
	var ghIn string
	r.Tools.GH = func(in string, args ...string) (int, string, string) {
		ghArgs, ghIn = args, in
		return 0, "https://github.com/zz-test/www/pull/7\n", ""
	}
	branch := "factory/t-0001"
	r.Tools.Git = func(_ string, args ...string) (int, string, string) { return 0, branch + "\n", "" }
	// not a task session
	if _, err := r.PR("create", "T-0001: x", "body", "/wt"); err == nil {
		t.Fatal("an actor without an agents row must be refused")
	}
	_, _ = s.db.Exec(`INSERT INTO agents(name,role,goal_id,task_id,status) VALUES('claude-coder-t-0001-a7','task','G-001','T-0001','live')`)
	out, err := r.PR("create", "T-0001 - add the page", "## Summary\nx", "/wt")
	if err != nil || !strings.Contains(out, "/pull/7") {
		t.Fatalf("create: %q %v", out, err)
	}
	if got := strings.Join(ghArgs, " "); got != "pr create --repo zz-test/www --base main --head factory/t-0001 --title T-0001: add the page --body-file -" ||
		ghIn != "## Summary\nx" {
		t.Errorf("gh %q stdin %q", got, ghIn)
	}
	// another branch than the task's
	branch = "factory/t-0002"
	if _, err := r.PR("create", "T-0001: x", "b", "/wt"); err == nil || !strings.Contains(err.Error(), "current branch") {
		t.Errorf("foreign branch: %v", err)
	}
	branch = "factory/t-0001"
	if _, err := r.PR("create", "T-0001", "b", "/wt"); err != nil { // the task title gives the description
		t.Errorf("id only: %v", err)
	}
	if !strings.Contains(strings.Join(ghArgs, " "), "--title T-0001: zz-test task") {
		t.Errorf("title from the task: %q", ghArgs)
	}
	// a public_text_repos repo: no owner terms refuses; a term in the body refuses without printing it
	_ = s.SetFactoryCfg("public_text_repos", "zz-test/www")
	t.Setenv("SCRUB_TERMS_FILE", "")
	if _, err := r.PR("create", "T-0001: x", "b", "/wt"); err == nil || !strings.Contains(err.Error(), "public text") {
		t.Errorf("no terms: %v", err)
	}
	term := "zebra" + "corp"
	terms := filepath.Join(t.TempDir(), "terms.txt")
	_ = os.WriteFile(terms, []byte(term+"\n"), 0o600)
	_ = s.SetFactoryCfg("scrub_terms_file", terms)
	ghArgs = nil
	if _, err := r.PR("create", "T-0001: x", "notes about "+term, "/wt"); err == nil || strings.Contains(err.Error(), term) || ghArgs != nil {
		t.Errorf("term in the body: %v (gh %q)", err, ghArgs)
	}
	if _, err := r.PR("create", "T-0001: x", "clean body", "/wt"); err != nil {
		t.Errorf("clean text: %v", err)
	}
	if _, err := r.PR("ready", "", "", "/wt"); err != nil || strings.Join(ghArgs, " ") != "pr ready factory/t-0001 --repo zz-test/www" {
		t.Errorf("ready: %v %q", err, ghArgs)
	}
}

func TestPRTitle(t *testing.T) {
	for _, c := range []struct{ given, db, want string }{
		{"T-0001: add x", "", "T-0001: add x"},
		{"t-0001 - add x", "", "T-0001: add x"},
		{"add x", "", "T-0001: add x"},
		{"T-0001", "[SC1] the db title", "T-0001: the db title"},
		{"T-0001:  T-0001", "db", "T-0001: db"},
	} {
		if got, err := PRTitle("T-0001", c.given, c.db); err != nil || got != c.want {
			t.Errorf("%q %q: %q %v", c.given, c.db, got, err)
		}
	}
	if _, err := PRTitle("T-0001", "T-0001", ""); err == nil {
		t.Error("no description anywhere must be refused")
	}
}

func TestAlertRunsAlertCmdOrTheMacNotification(t *testing.T) {
	r, s := testRunner(t, "")
	var calls [][]string
	r.Tools.Sh = func(_ string, _ time.Duration, env []string, argv ...string) (int, string) {
		calls = append(calls, argv)
		return 0, ""
	}
	old := GOOS
	t.Cleanup(func() { GOOS = old })
	GOOS = "linux"
	if how, err := r.Alert("down", "it's \"down\"; `rm -rf`"); err != nil || how != "log" || len(calls) != 0 {
		t.Errorf("linux without alert_cmd logs only: %s %v %q", how, err, calls)
	}
	GOOS = "darwin"
	if how, _ := r.Alert("down", "text"); how != "osascript" || len(calls) != 1 || calls[0][0] != "osascript" ||
		calls[0][len(calls[0])-2] != "down" || calls[0][len(calls[0])-1] != "text" {
		t.Errorf("darwin: %s %q", how, calls)
	}
	_ = s.SetCfg("alert_cmd", `printf '%s\n' "$1" >> /dev/null`)
	if how, _ := r.Alert("down", "a $(text)"); how != "alert_cmd" || len(calls) != 2 ||
		strings.Join(calls[1], "|") != `sh|-c|printf '%s\n' "$1" >> /dev/null|hivemill-alert|down|a $(text)` {
		t.Errorf("alert_cmd: %s %q", how, calls)
	}
	b, _ := os.ReadFile(r.Env.AlertLog())
	if n := strings.Count(string(b), "\n"); n != 3 || !strings.Contains(string(b), "down: text") {
		t.Errorf("alert log: %q", b)
	}
}

func TestProjectGateRunsInsteadOfAffected(t *testing.T) {
	r, s := testRunner(t, "")
	if cmd, _ := r.projectGate("www"); cmd != "" {
		t.Fatalf("no gate_cmd column, no gate: %q", cmd)
	}
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN gate_cmd TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE projects ADD COLUMN gate_timeout_s INTEGER NOT NULL DEFAULT 3600`)
	_, _ = s.db.Exec(`UPDATE projects SET gate_cmd='pnpm --filter web test', gate_timeout_s=60 WHERE id='www'`)
	cmd, limit := r.projectGate("www")
	if cmd != "pnpm --filter web test" || limit != time.Minute {
		t.Fatalf("gate %q %v", cmd, limit)
	}
	var argv []string
	r.Tools.Sh = func(_ string, _ time.Duration, _ []string, a ...string) (int, string) {
		argv = a
		return 1, "FAIL web\n"
	}
	ok, note := r.runProjectGate(t.TempDir(), cmd, limit, t.TempDir())
	if ok || strings.Join(argv, "|") != "bash|-c|pnpm --filter web test" || !strings.Contains(note, "exited 1") {
		t.Errorf("%v %q %q", ok, note, argv)
	}
}

func TestTestProofWithASpaceIsTheProjectsCommand(t *testing.T) {
	wt := t.TempDir()
	if argv, dir := TestArgv(wt, "pnpm --filter web test"); strings.Join(argv, "|") != "bash|-c|pnpm --filter web test" || dir != wt {
		t.Errorf("command form: %q %s", argv, dir)
	}
	if argv, _ := TestArgv(wt, "tests/test_x.py"); argv[0] != "python3" {
		t.Errorf("a .py file stays unittest: %q", argv)
	}
	if argv, _ := TestArgv(wt, "tests.test_x.T.test_y"); argv != nil {
		t.Errorf("a dotted id without tests/ cannot run: %q", argv)
	}
	c := Criteria("1. the page builds - test:pnpm --filter web test\n2. lint - cmd:pnpm lint => exit 0")
	if len(LintProblems("1. the page builds - test:pnpm --filter web test\n2. lint - cmd:pnpm lint => exit 0")) != 0 ||
		c[0].Kind != "test" || c[0].Proof != "pnpm --filter web test" {
		t.Errorf("spec gate: %+v", c)
	}
}

func TestFactoryKeysAreChecked(t *testing.T) {
	for _, c := range []struct {
		k, v string
		ok   bool
	}{
		{"noreply_email", "1+u" + NoreplySuffix, true},
		{"noreply_email", "me@example.com", false},
		{"noreply_email", "", true},
		{"scrub_terms_file", "relative.txt", false},
		{"scrub_terms_file", "/nonexistent/terms.txt", false},
		{"public_text_repos", "acme/www, acme/api", true},
		{"public_text_repos", "not a slug", false},
		{"model", "x", false},
	} {
		if err := CheckFactoryKey(c.k, c.v); (err == nil) != c.ok {
			t.Errorf("%s=%q: %v", c.k, c.v, err)
		}
	}
}

func TestHealthLaunchAgentRunsEveryFiveMinutes(t *testing.T) {
	home := t.TempDir()
	e := Env{Home: home, UserHome: "/Users/user"}
	p, err := RenderLaunchAgent(e, "health", PlistInput{FactoryHome: home, Path: DefaultLaunchdPath(e.UserHome), LogDir: home + "/logs"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>hivemill.health</string>", "health --alert", "<key>StartInterval</key>\n\t<integer>300</integer>",
		home + "/logs/hivemill.health.log"} {
		if !strings.Contains(p, want) {
			t.Errorf("health plist lacks %q", want)
		}
	}
	if _, err := RenderLaunchAgent(e, "nope", PlistInput{}); err == nil {
		t.Error("unknown LaunchAgent")
	}
}
