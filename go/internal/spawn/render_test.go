package spawn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var goldenTokens = SettingsTokens("/w/üser/fa\"c\\tory\t𝔘\x7f", "/w/üser", "/w/üser/.config")

func readGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", name))
	if err != nil {
		t.Fatalf("golden %s: %v", name, err)
	}
	return string(b)
}

type goldenCases struct {
	Slug         [][]string `json:"slug"`
	JSONEscape   [][]string `json:"jsonescape"`
	Kickoff      [][]any    `json:"kickoff"`
	ParseRuntime [][]any    `json:"parse_runtimes"`
	Effort       [][]any    `json:"effort"`
	OrgSection   [][]any    `json:"org_section"`
	ArgsJSON     []string   `json:"args_json"`
	Model        [][]string `json:"model"`
}

func loadCases(t *testing.T) goldenCases {
	t.Helper()
	var c goldenCases
	if err := json.Unmarshal([]byte(readGolden(t, "cases.json")), &c); err != nil {
		t.Fatalf("cases.json: %v", err)
	}
	return c
}

// TestRenderGolden: the brief and the settings text of every role file x org (and single/multi-org factory) equal what
// the Python functions wrote (testdata/golden, produced by the generator in the PR body).
func TestRenderGolden(t *testing.T) {
	for _, role := range Roles {
		for _, org := range []string{"claude", "cursor"} {
			for _, multi := range []bool{false, true} {
				t.Run(fmt.Sprintf("brief/%s/%s/%v", role, org, multi), func(t *testing.T) {
					got, err := BriefFromDir("testdata/roles", role, org, multi)
					if err != nil {
						t.Fatal(err)
					}
					m := 0
					if multi {
						m = 1
					}
					if want := readGolden(t, fmt.Sprintf("brief-%s-%s-%d.md", role, org, m)); got != want {
						t.Fatalf("brief differs from Python:\n got %q\nwant %q", got, want)
					}
				})
			}
		}
		t.Run("settings/"+role, func(t *testing.T) {
			got, r := SettingsFromDir("testdata/roles", role, goldenTokens)
			if r != nil {
				t.Fatal(r)
			}
			if want := readGolden(t, "settings-"+role+".json"); got != want {
				t.Fatalf("settings for %s differ from Python (%d bytes vs %d)", role, len(got), len(want))
			}
		})
	}
}

// TestRenderRealRoles reads the repo's own roles/ (the merge gate has the files): every role renders, the settings are
// valid JSON with no token left, and a deny rule of the template is present in the output.
func TestRenderRealRoles(t *testing.T) {
	rolesDir := filepath.Join("..", "..", "..", "roles")
	for _, role := range Roles {
		brief, err := BriefFromDir(rolesDir, role, "cursor", true)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if !strings.Contains(brief, "## Your org") {
			t.Errorf("%s: no org block", role)
		}
		text, r := SettingsFromDir(rolesDir, role, SettingsTokens("/h/live", "/h", "/h/.config"))
		if r != nil {
			t.Fatalf("%s: %v", role, r)
		}
		if !json.Valid([]byte(text)) || strings.Contains(text, "@FACTORY_") {
			t.Errorf("%s: invalid settings", role)
		}
		var cfg struct {
			Permissions struct{ Deny []string }
		}
		if err := json.Unmarshal([]byte(text), &cfg); err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		tpl, _ := ReadText(filepath.Join(rolesDir, "settings", role+".json.in"))
		var tcfg struct {
			Permissions struct{ Deny []string }
		}
		if err := json.Unmarshal([]byte(strings.NewReplacer("@FACTORY_LIVE_HOME@", "x", "@FACTORY_HOME_DIR@", "x", "@FACTORY_USER_CONFIG@", "x").Replace(tpl)), &tcfg); err != nil {
			t.Fatalf("%s template: %v", role, err)
		}
		if len(cfg.Permissions.Deny) != len(tcfg.Permissions.Deny) {
			t.Errorf("%s: %d deny rules rendered, %d in the template", role, len(cfg.Permissions.Deny), len(tcfg.Permissions.Deny))
		}
	}
}

func TestSettingsRefusals(t *testing.T) {
	tok := SettingsTokens("/l", "/h", "/c")
	if _, r := SettingsText(`{"a": @FACTORY_LIVE_HOME@}`, "x.json.in", tok); r == nil || r.Code != 3 ||
		!strings.HasPrefix(r.Msg, "settings template x.json.in renders to invalid JSON (") {
		t.Errorf("invalid JSON: %+v", r)
	}
	_, r := SettingsText(`{"a": "@FACTORY_TYPO@"}`, "x.json.in", tok)
	want := "settings template x.json.in has an unknown @FACTORY_ token; known: @FACTORY_LIVE_HOME@, @FACTORY_HOME_DIR@, @FACTORY_USER_CONFIG@"
	if r == nil || r.Code != 3 || r.Msg != want {
		t.Errorf("unknown token: %+v", r)
	}
	if got, r := SettingsText(`{"a": "@FACTORY_LIVE_HOME@"}`, "x", SettingsTokens(`a"b`, "", "")); r != nil || got != `{"a": "a\"b"}` {
		t.Errorf("escaped value: %q %v", got, r)
	}
	if r := CheckLivePin(true, "/a", "/b", "/a"); r == nil || r.Code != 3 ||
		r.Msg != "live_home /a != running checkout /b: refusing to render settings (FACTORY_HOME=/a); fix or remove ~/.config/factory/live_home, or run the live checkout's bin/factory" {
		t.Errorf("pin mismatch: %+v", r)
	}
	if r := CheckLivePin(true, "/a", "/a", "/a"); r != nil {
		t.Errorf("pin match refused: %+v", r)
	}
	if r := CheckLivePin(false, "/a", "/b", "/c"); r != nil {
		t.Errorf("temp home refused: %+v", r)
	}
}

// TestSettingsEnvOrder: the settings "env" object is applied in file order with Python's str() of the values.
func TestSettingsEnvOrder(t *testing.T) {
	got, err := settingsEnv(`{"permissions": {"env": {"no": "x"}}, "env": {"B": "1", "A": true, "C": null}, "x": 1}`)
	if err != nil || fmt.Sprint(got) != "[{B 1} {A True} {C None}]" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := settingsEnv(`[1]`); got != nil || err != nil {
		t.Errorf("non-object: %v %v", got, err)
	}
	if _, err := settingsEnv(`{"env": {"A": 1}}`); err == nil {
		t.Errorf("a number value is not a Python-str case this port decides: want an error")
	}
}

func TestGoldenCases(t *testing.T) {
	c := loadCases(t)
	for _, x := range c.Slug {
		if got := Slug(x[0]); got != x[1] {
			t.Errorf("Slug(%q) = %q, Python %q", x[0], got, x[1])
		}
	}
	for _, x := range c.JSONEscape {
		if got := jsonEscape(x[0]); got != x[1] {
			t.Errorf("jsonEscape(%q) = %q, Python %q", x[0], got, x[1])
		}
	}
	for _, x := range c.Kickoff {
		a := x[0].([]any)
		s := func(i int) string {
			if a[i] == nil {
				return ""
			}
			return a[i].(string)
		}
		if got := Kickoff(s(0), s(1), s(2), s(3), s(4), s(5)); got != x[1].(string) {
			t.Errorf("Kickoff(%v) = %q, Python %q", a, got, x[1])
		}
	}
	for _, x := range c.ParseRuntime {
		got, err := ParseRuntimes(x[0].(string), x[1].(string))
		want := x[2].(map[string]any)
		if e, bad := want["err"]; bad {
			if err == nil || err.Error() != e.(string) {
				t.Errorf("ParseRuntimes(%q,%q) err %v, Python %q", x[0], x[1], err, e)
			}
			continue
		}
		w := want["ok"].(map[string]any)
		if err != nil || len(got) != len(w) {
			t.Errorf("ParseRuntimes(%q,%q) = %v %v, Python %v", x[0], x[1], got, err, w)
			continue
		}
		for k, v := range w {
			if got[k] != v.(string) {
				t.Errorf("ParseRuntimes(%q,%q)[%s] = %q, Python %q", x[0], x[1], k, got[k], v)
			}
		}
	}
	for _, x := range c.Effort {
		lvl, want := x[0].(string), x[2].(map[string]any)
		_, r := EffortArgs(lvl, x[1].(string))
		if msg, bad := want["err"]; bad {
			if r == nil || "factory: "+r.Msg != msg.(string) || float64(r.Code) != want["code"].(float64) {
				t.Errorf("EffortArgs(%q) = %+v, Python %v", lvl, r, want)
			}
		} else if r != nil {
			t.Errorf("EffortArgs(%q) refused: %+v", lvl, r)
		}
	}
	for _, x := range c.OrgSection {
		org := ""
		if x[0] != nil {
			org = x[0].(string)
		}
		if got := OrgSection(org, x[1].(bool)); got != x[2].(string) {
			t.Errorf("OrgSection(%q,%v) = %q, Python %q", org, x[1], got, x[2])
		}
	}
	for _, x := range c.Model {
		if got := OrgModel(x[1], x[0]); got != x[2] {
			t.Errorf("OrgModel(%s,%s) = %q, Python %q", x[1], x[0], got, x[2])
		}
	}
}

// TestArgsJSON: spawn_args serialises exactly like Python's json.dumps of the same dict.
func TestArgsJSON(t *testing.T) {
	c := loadCases(t)
	a := &orderedArgs{}
	a.set("role", "coder")
	a.set("goal", "G-1")
	a.set("team", nil)
	a.set("task", "T-1")
	a.set("name", "claude-coder-t-1")
	a.set("cwd", nil)
	a.set("prompt", "p é\n")
	a.set("model", nil)
	a.set("resume", nil)
	a.set("fresh", false)
	a.set("org", "claude")
	a.set("host_backend", "hc")
	a.set("host_backend_pinned", true)
	a.set("sandbox_optin", true)
	got, err := a.json()
	if err != nil || got != c.ArgsJSON[0] {
		t.Errorf("args:\n got %s (%v)\nwant %s", got, err, c.ArgsJSON[0])
	}
	e := &orderedArgs{}
	e.set("role", "evaluator")
	e.set("name", "e")
	e.set("env_extra", []KV{{"FACTORY_HOME", "/x/fh"}, {"PATH", "/x/bin:/b"}})
	e.set("plugin_dirs", []string{"/v/a", "/v/b"})
	e.set("add_home_dir", false)
	if got, err := e.json(); err != nil || got != c.ArgsJSON[1] {
		t.Errorf("evaluator args:\n got %s (%v)\nwant %s", got, err, c.ArgsJSON[1])
	}
}

// TestUnicode: Python str semantics for whitespace, \d and lower(): NBSP, \v and U+0663 (Python output quoted in
// the comments: ' \xa0x\v '.strip() == 'x'; re.fullmatch(r'G-\d+', 'G-٣') matches).
func TestUnicode(t *testing.T) {
	if got := pyStrip("  x\v "); got != "x" {
		t.Errorf("pyStrip = %q, Python 'x'", got)
	}
	if got := csv("  \v coder, pm\x1f"); fmt.Sprint(got) != "[coder pm]" {
		t.Errorf("csv = %v, Python ['coder', 'pm']", got)
	}
	if !goalIDRE.MatchString("G-٣") || goalIDRE.MatchString("G-") || goalIDRE.MatchString("G-1\n") {
		t.Errorf("goal id pattern is not Python's fullmatch of G-\\d+")
	}
	if got := Slug("İ"); got != "i" {
		t.Errorf("Slug(İ) = %q, Python 'i' (lower() is 'i' + U+0307, which becomes '-' and is trimmed)", got)
	}
	if got := Slug("Kx"); got != "kx" {
		t.Errorf("Slug(Kelvin sign) = %q, Python 'kx'", got)
	}
	if s, err := DecodeText([]byte("a\r\nb\rc\n")); err != nil || s != "a\nb\nc\n" {
		t.Errorf("DecodeText = %q %v", s, err)
	}
	if _, err := DecodeText([]byte("a\xffb")); err == nil {
		t.Errorf("invalid UTF-8 must be an error (UnicodeDecodeError)")
	}
}

// TestSymlinks: product code that reads repo paths returns an error naming the path for a dangling link, a chain
// through a missing component and a loop (Python: FileNotFoundError / OSError on read_text -> die exit 3 for settings,
// a traceback for the brief).
func TestSymlinks(t *testing.T) {
	dir := t.TempDir()
	rd := filepath.Join(dir, "roles")
	if err := os.MkdirAll(filepath.Join(rd, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustLink := func(target, link string) {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	mustLink(filepath.Join(dir, "nowhere"), filepath.Join(rd, "_common.md"))              // dangling
	mustLink(filepath.Join(dir, "gone", "deeper", "x.md"), filepath.Join(rd, "coder.md")) // chain through a missing component
	mustLink("pm.md", filepath.Join(rd, "pm.md"))                                         // loop
	for _, role := range []string{"coder", "pm"} {
		_, err := BriefFromDir(rd, role, "claude", false)
		if err == nil || !strings.Contains(err.Error(), filepath.Join(rd, "_common.md")) {
			t.Errorf("%s: brief error %v does not name the path", role, err)
		}
	}
	if err := os.Remove(filepath.Join(rd, "_common.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rd, "_common.md"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"coder", "pm"} {
		_, err := BriefFromDir(rd, role, "claude", false)
		if err == nil || !strings.Contains(err.Error(), filepath.Join(rd, role+".md")) {
			t.Errorf("%s: brief error %v does not name the role file", role, err)
		}
	}
	mustLink(filepath.Join(dir, "nowhere"), filepath.Join(rd, "settings", "coder.json.in"))
	mustLink("pm.json.in", filepath.Join(rd, "settings", "pm.json.in"))
	for _, role := range []string{"coder", "pm", "qa"} {
		_, r := SettingsFromDir(rd, role, nil)
		path := filepath.Join(rd, "settings", role+".json.in")
		if r == nil || r.Code != 3 || !strings.HasPrefix(r.Msg, "cannot read the settings template "+path+": ") ||
			!strings.HasSuffix(r.Msg, "; restore it with git checkout") {
			t.Errorf("%s: settings refusal %+v", role, r)
		}
	}
}
