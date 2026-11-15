package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"factory/go/internal/identity"
)

// tierEnv is one run of `task tier` against a fresh taskupd golden DB under home.
type tierEnv struct {
	t     *testing.T
	home  string
	execs [][]string // argv of every hand-over to Python
}

func newTierEnv(t *testing.T) *tierEnv {
	t.Helper()
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, "taskupd"); err != nil {
		t.Fatal(err)
	}
	return &tierEnv{t: t, home: home}
}

func (e *tierEnv) sql(q string, args ...any) {
	e.t.Helper()
	d, err := openRaw(filepath.Join(e.home, "db", "factory.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *tierEnv) scalar(q string, args ...any) string {
	e.t.Helper()
	d, err := openRaw(filepath.Join(e.home, "db", "factory.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer d.Close()
	var v *string
	if err := d.QueryRow(q, args...).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	if v == nil {
		return "<NULL>"
	}
	return *v
}

func (e *tierEnv) tier(id string) string {
	return e.scalar("SELECT tier FROM tasks WHERE id=?", id)
}

func (e *tierEnv) events() []string {
	e.t.Helper()
	d, err := openRaw(filepath.Join(e.home, "db", "factory.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer d.Close()
	rs, err := d.Query("SELECT actor||'|'||entity_id||'|'||detail FROM events WHERE action='tier' ORDER BY id")
	if err != nil {
		e.t.Fatal(err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (e *tierEnv) run(agent string, argv ...string) (int, string, string) {
	e.t.Helper()
	env := map[string]string{"FACTORY_HOME": e.home, "HOME": e.home, "FACTORY_AGENT": agent, "FACTORY_PROC_DIR": e.t.TempDir()}
	var out, errb bytes.Buffer
	code := Run(Deps{Args: append([]string{"task", "tier"}, argv...), Stdout: &out, Stderr: &errb,
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec:     func(path string, argv, env []string) error { e.execs = append(e.execs, argv); return nil },
		Stdin:    strings.NewReader(""), Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			PwDir: func(int) (string, error) { return e.home, nil }, Now: func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) }}})
	return code, out.String(), errb.String()
}

func TestTaskTierShow(t *testing.T) {
	e := newTierEnv(t)
	if code, out, errs := e.run("claude-coder", "T-0001"); code != 0 || out != "-\n" || errs != "" {
		t.Fatalf("unset: %d %q %q", code, out, errs)
	}
	e.sql("UPDATE tasks SET tier='T2' WHERE id='T-0001'")
	for _, agent := range []string{"claude-coder", "claude-lead", "owner"} { // show needs no role
		if code, out, _ := e.run(agent, "T-0001"); code != 0 || out != "T2\n" {
			t.Errorf("%s: %d %q", agent, code, out)
		}
	}
	if code, out, _ := e.run("claude-coder", "T-0001", "up"); code != 0 || out != "T2\n" { // "up" alone is a show
		t.Errorf("up alone: %d %q", code, out)
	}
}

func TestTaskTierRoleGuard(t *testing.T) {
	e := newTierEnv(t)
	code, out, errs := e.run("claude-coder", "T-0001", "up", "T2")
	if code != 3 || out != "" || !strings.Contains(errs, "role 'coder' (claude-coder) may not do this; allowed: reviewer, qa, lead, director, boss, judge") {
		t.Fatalf("coder: %d %q %q", code, out, errs)
	}
	if got := e.tier("T-0001"); got != "<NULL>" || len(e.events()) != 0 {
		t.Fatalf("coder changed the DB: tier %s events %v", got, e.events())
	}
	for _, agent := range []string{"claude-reviewer", "claude-qa", "claude-lead", "claude-director", "claude-boss", "claude-judge", "owner"} {
		e.sql("UPDATE tasks SET tier=NULL WHERE id='T-0001'")
		if code, out, errs := e.run(agent, "T-0001", "up", "T2"); code != 0 || out != "T-0001 tier T2\n" || errs != "" {
			t.Errorf("%s: %d %q %q", agent, code, out, errs)
		}
		if got := e.tier("T-0001"); got != "T2" {
			t.Errorf("%s: tier %s", agent, got)
		}
	}
	if code, _, errs := e.run("claude-coder", "T-0001", "--classify"); code != 3 {
		t.Errorf("coder classify: %d %q", code, errs)
	}
}

func TestTaskTierRaiseAndNeverLower(t *testing.T) {
	e := newTierEnv(t)
	if code, out, _ := e.run("claude-lead", "T-0001", "t1"); code != 0 || out != "T-0001 tier T1\n" { // case-insensitive, stored upper
		t.Fatalf("t1: %d %q", code, out)
	}
	if code, out, _ := e.run("claude-reviewer", "T-0001", "up", "T3"); code != 0 || out != "T-0001 tier T3\n" {
		t.Fatalf("up T3: %d %q", code, out)
	}
	code, out, errs := e.run("claude-reviewer", "T-0001", "T1")
	if code != 2 || out != "" || errs != "factory: T-0001 is T3: a tier is only ever raised, never lowered\n" {
		t.Fatalf("lower: %d %q %q", code, out, errs)
	}
	if got := e.tier("T-0001"); got != "T3" {
		t.Fatalf("lowered tier stored: %s", got)
	}
	// the same tier again: no row, no event
	if code, out, _ := e.run("claude-reviewer", "T-0001", "T3"); code != 0 || out != "T-0001 tier T3\n" {
		t.Fatalf("same: %d %q", code, out)
	}
	want := []string{"claude-lead|T-0001|- -> T1: raised by claude-lead", "claude-reviewer|T-0001|T1 -> T3: raised by claude-reviewer"}
	if got := e.events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events %q, want %q", got, want)
	}
	if got := e.scalar("SELECT updated FROM tasks WHERE id='T-0001'"); got != "2026-11-12T01:02:03+00:00" {
		t.Fatalf("updated %s", got)
	}
}

func TestTaskTierRefusals(t *testing.T) {
	e := newTierEnv(t)
	for _, tc := range []struct {
		name  string
		agent string
		argv  []string
		code  int
		err   string
	}{
		{"unknown task", "claude-lead", []string{"T-9999"}, 1, "factory: no task T-9999\n"},
		{"unknown task before role", "claude-coder", []string{"T-9999", "T2"}, 1, "factory: no task T-9999\n"},
		{"tier and classify", "claude-lead", []string{"T-0001", "T2", "--classify"}, 2, "factory: give a tier to raise to or --classify, not both\n"},
		{"tier and classify before role", "claude-coder", []string{"T-0001", "up", "T2", "--classify"}, 2, "factory: give a tier to raise to or --classify, not both\n"},
		{"T9", "claude-lead", []string{"T-0001", "up", "T9"}, 2, "factory: tier must be one of T0, T1, T2, T3; got 'T9'\n"},
		{"quote repr", "claude-lead", []string{"T-0001", "it's"}, 2, "factory: tier must be one of T0, T1, T2, T3; got \"it's\"\n"},
		// G8: python3 -c 'print(repr(w))' and "w".upper() not in TIERS for each of these
		{"arabic digit", "claude-lead", []string{"T-0001", "T٣"}, 2, "factory: tier must be one of T0, T1, T2, T3; got 'T٣'\n"},
		{"padded", "claude-lead", []string{"T-0001", " T2 "}, 2, "factory: tier must be one of T0, T1, T2, T3; got ' T2 '\n"},
		{"vertical tab", "claude-lead", []string{"T-0001", "T2\v"}, 2, "factory: tier must be one of T0, T1, T2, T3; got 'T2\\x0b'\n"},
		{"nbsp", "claude-lead", []string{"T-0001", "T2\xc2\xa0"}, 2, "factory: tier must be one of T0, T1, T2, T3; got 'T2\\xa0'\n"},
		{"zero width", "claude-lead", []string{"T-0001", "T2\xe2\x80\x8b"}, 2, "factory: tier must be one of T0, T1, T2, T3; got 'T2\\u200b'\n"},
		{"astral", "claude-lead", []string{"T-0001", "T😀"}, 2, "factory: tier must be one of T0, T1, T2, T3; got 'T😀'\n"},
		{"classify with risk_tiers off", "claude-lead", []string{"T-0001", "--classify"}, 2, "factory: risk_tiers is off: the director enables it with `factory config risk_tiers on`\n"},
	} {
		code, out, errs := e.run(tc.agent, tc.argv...)
		if code != tc.code || errs != tc.err || out != "" {
			t.Errorf("%s: exit %d out %q err %q, want %d %q", tc.name, code, out, errs, tc.code, tc.err)
		}
	}
	if got := e.tier("T-0001"); got != "<NULL>" || len(e.events()) != 0 {
		t.Errorf("a refusal wrote: tier %s events %v", got, e.events())
	}
}

func TestTaskTierRepr(t *testing.T) {
	for in, want := range map[string]string{ // python3 repr()
		"T2": "'T2'", "a'b": `"a'b"`, `a'b"c`: `'a\'b"c'`, "a\\b": `'a\\b'`, "a\nb\tc\r": `'a\nb\tc\r'`,
		"\x00\x7f": `'\x00\x7f'`, "é": "'é'", "\u0085": `'\x85'`, "\xe2\x80\xa8": `'\u` + `2028'`, "\U000e0001": `'\U000e0001'`,
		"\xff": `'\udcff'`,
	} {
		if got := taskTierRepr(in); got != want {
			t.Errorf("repr(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestTaskTierStoredValueOutsideTiersDelegates(t *testing.T) {
	e := newTierEnv(t)
	e.sql("UPDATE tasks SET tier='T7' WHERE id='T-0001'")
	if code, out, _ := e.run("claude-lead", "T-0001"); code != 0 || out != "T7\n" { // show prints what is stored
		t.Fatalf("show: %d %q", code, out)
	}
}

// tierRepo makes HOME a git repo: main has src/a.txt, branch "small" changes one line, "big" adds 700 lines, "guard"
// adds ci/guard_x.py and its own (emptied) rules. HOME's own ci/tier_rules.json lists ci/guard_x.py as sensitive.
func (e *tierEnv) repo() {
	e.t.Helper()
	git := func(args ...string) {
		e.t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "-C", e.home}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + e.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if out, err := cmd.CombinedOutput(); err != nil {
			e.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	rules := func(sensitive string) string {
		return `{"docs_globs":["docs/**"],"docs_excluded_prefixes":[],"tests_globs":["tests/test_*.py"],"sensitive_paths":[` + sensitive +
			`],"sensitive_defs":{},"small_lines":150,"large_lines":600}`
	}
	write := func(rel, body string) {
		p := filepath.Join(e.home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write(".gitignore", "db/\n")
	write("src/a.txt", "one\n")
	write("ci/tier_rules.json", rules(`"ci/guard_x.py"`))
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "small", "main")
	write("src/a.txt", "two\n")
	git("commit", "-qam", "small")
	git("checkout", "-q", "-b", "big", "main")
	write("src/big.txt", strings.Repeat("x\n", 700))
	git("add", "-A")
	git("commit", "-q", "-m", "big")
	git("checkout", "-q", "-b", "guard", "main")
	write("ci/guard_x.py", "x = 1\n")
	write("ci/tier_rules.json", rules(""))
	git("add", "-A")
	git("commit", "-q", "-m", "guard")
	git("checkout", "-q", "main") // HOME's working tree: its own rules file
	e.sql("UPDATE config SET value='on' WHERE key='risk_tiers'")
}

func TestTaskTierClassifyKeepsTheHigher(t *testing.T) {
	e := newTierEnv(t)
	e.repo()
	for _, tc := range []struct{ head, out, tier string }{
		{"small", "T-0001 tier T1\n", "T1"},
		{"big", "T-0001 tier T3\n", "T3"},
		{"small", "T-0001 tier T3 (computed T1, kept the higher)\n", "T3"},
	} {
		code, out, errs := e.run("claude-qa", "T-0001", "--classify", "--base", "main", "--head", tc.head)
		if code != 0 || out != tc.out || errs != "" || e.tier("T-0001") != tc.tier {
			t.Fatalf("%s: %d %q %q tier %s", tc.head, code, out, errs, e.tier("T-0001"))
		}
	}
	want := []string{"claude-qa|T-0001|- -> T1: no sensitive path or def", "claude-qa|T-0001|T1 -> T3: more than 600 changed lines"}
	got := e.events()
	if len(got) != 2 || got[0] != want[0] || !strings.HasPrefix(got[1], "claude-qa|T-0001|T1 -> T3: ") {
		t.Fatalf("events %q", got)
	}
}

func TestTaskTierClassifyRulesComeFromHome(t *testing.T) {
	e := newTierEnv(t)
	e.repo()
	code, out, errs := e.run("claude-reviewer", "T-0001", "--classify", "--base=main", "--head=guard")
	if code != 0 || !strings.Contains(out, "tier T3") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	ev := e.events()
	if len(ev) != 1 || !strings.Contains(ev[0], "sensitive path ci/guard_x.py") {
		t.Fatalf("events %q", ev)
	}
}

func TestTaskTierClassifyBranchAndRefs(t *testing.T) {
	e := newTierEnv(t)
	e.repo()
	if code, _, errs := e.run("claude-qa", "T-0001", "--classify"); code != 2 || errs != "factory: T-0001 has no branch: give --head REF\n" {
		t.Errorf("no branch: %d %q", code, errs)
	}
	if code, _, errs := e.run("claude-qa", "T-0001", "--classify", "--head="); code != 2 || !strings.Contains(errs, "--head REF") {
		t.Errorf("empty head: %d %q", code, errs)
	}
	e.sql("UPDATE tasks SET branch='nope' WHERE id='T-0001'") // head is origin/nope: git fails, the text comes back
	if code, _, errs := e.run("claude-qa", "T-0001", "--classify", "--base", "main"); code != 2 || !strings.Contains(errs, "origin/nope") {
		t.Errorf("branch head: %d %q", code, errs)
	}
	out := filepath.Join(e.home, "zz")
	code, _, errs := e.run("claude-qa", "T-0001", "--classify", "--base=--output="+out, "--head", "small")
	if code != 2 || errs != "factory: bad ref '--output="+out+"': a ref must not start with '-'\n" { // python repr quoting
		t.Errorf("dash base: %d %q", code, errs)
	}
	if _, err := os.Stat(out); err == nil {
		t.Errorf("--output was honoured: %s exists", out)
	}
	if got := e.tier("T-0001"); got != "<NULL>" || len(e.events()) != 0 {
		t.Errorf("failed classify wrote: %s %v", got, e.events())
	}
}

// G10: the rules path is read from HOME; a missing file, a dangling link and a link loop are exit 2 naming the path.
func TestTaskTierClassifyRulesMissingAndSymlink(t *testing.T) {
	for _, kind := range []string{"missing", "dangling", "loop", "directory"} {
		e := newTierEnv(t)
		e.repo()
		p := filepath.Join(e.home, "ci", "tier_rules.json")
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		switch kind {
		case "dangling":
			if err := os.Symlink(filepath.Join(e.home, "ci", "gone.json"), p); err != nil {
				t.Fatal(err)
			}
		case "loop":
			if err := os.Symlink(p, p); err != nil {
				t.Fatal(err)
			}
		case "directory":
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		code, out, errs := e.run("claude-qa", "T-0001", "--classify", "--base", "main", "--head", "small")
		if code != 2 || out != "" || !strings.Contains(errs, "cannot load") || !strings.Contains(errs, "tier_rules.json") {
			t.Errorf("%s: %d %q %q", kind, code, out, errs)
		}
		if kind != "directory" && !strings.Contains(errs, e.home) {
			t.Errorf("%s: stderr does not name HOME: %q", kind, errs)
		}
		if got := e.tier("T-0001"); got != "<NULL>" {
			t.Errorf("%s: tier stored %s", kind, got)
		}
	}
}

// The show and raise paths need no rules file and no git.
func TestTaskTierRaiseNeedsNoRulesFile(t *testing.T) {
	e := newTierEnv(t)
	if _, err := os.Stat(filepath.Join(e.home, "ci")); err == nil {
		t.Fatal("test home has ci/")
	}
	if code, out, errs := e.run("claude-lead", "T-0001", "T2"); code != 0 || out != "T-0001 tier T2\n" {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}

func TestTaskTierParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		ok   bool
		want taskTierCmd
	}{
		{"show", []string{"T-1"}, true, taskTierCmd{id: "T-1", base: "origin/main"}},
		{"raise", []string{"T-1", "up", "T2"}, true, taskTierCmd{id: "T-1", words: []string{"up", "T2"}, base: "origin/main"}},
		{"flags first", []string{"--classify", "--head", "h", "T-1"}, true, taskTierCmd{id: "T-1", classify: true, head: "h", hasHead: true, base: "origin/main"}},
		{"equals dash value", []string{"T-1", "--classify", "--base=-x"}, true, taskTierCmd{id: "T-1", classify: true, base: "-x"}},
		{"no id", []string{"--classify"}, false, taskTierCmd{}},
		{"four positionals", []string{"T-1", "a", "b", "c"}, false, taskTierCmd{}},
		{"empty positional", []string{"T-1", ""}, false, taskTierCmd{}},
		{"separate dash value", []string{"T-1", "--base", "--output=zz"}, false, taskTierCmd{}},
		{"missing value", []string{"T-1", "--base"}, false, taskTierCmd{}},
		{"abbreviation", []string{"T-1", "--class"}, false, taskTierCmd{}},
		{"other flag", []string{"T-1", "--full"}, false, taskTierCmd{}},
		{"repeated", []string{"T-1", "--head", "a", "--head", "b"}, false, taskTierCmd{}},
		{"classify=x", []string{"T-1", "--classify=1"}, false, taskTierCmd{}},
		{"double dash", []string{"T-1", "--", "T2"}, false, taskTierCmd{}},
		{"negative-looking word", []string{"T-1", "-1"}, false, taskTierCmd{}},
	} {
		got, ok := parseTaskTier(tc.args)
		if ok != tc.ok || (ok && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("%s: %v %+v, want %v %+v", tc.name, ok, got, tc.ok, tc.want)
		}
	}
}

// The still-unported argv forms go to Python through Run.
func TestTaskTierDelegatesWhatItCannotRead(t *testing.T) {
	e := newTierEnv(t)
	e.run("claude-lead", "T-0001", "--class")
	if len(e.execs) != 1 {
		t.Fatalf("not handed to Python: %v", e.execs)
	}
	if got := e.tier("T-0001"); got != "<NULL>" {
		t.Fatalf("tier %s", got)
	}
}

// G7: no panic, and an accepted argv has an id and at most two words.
func FuzzTaskTierArgs(f *testing.F) {
	f.Add("T-1", "up", "T2", "--classify")
	f.Add("T-1", "--base=-x", "--head", "h")
	f.Add("T-1", "T٣", " T2 ", "T2\v")
	f.Add("--classify", "--base", "--output=zz", "")
	f.Fuzz(func(t *testing.T, a, b, c, d string) {
		got, ok := parseTaskTier([]string{a, b, c, d})
		if !ok {
			return
		}
		cmd := got.(taskTierCmd)
		if cmd.id == "" || len(cmd.words) > 2 {
			t.Fatalf("accepted %q -> %+v", []string{a, b, c, d}, cmd)
		}
		for _, w := range cmd.words {
			if r := taskTierRepr(w); !utf8.ValidString(r) {
				t.Fatalf("repr of %q is not UTF-8: %q", w, r)
			}
			_ = taskTierASCIIUpper(w)
		}
	})
}
