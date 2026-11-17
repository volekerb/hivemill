package sandbox2

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/sandbox"
)

// Helpers of the ports of tests/test_sandbox_spawn.py and tests/test_researcher_sandbox.py (names prefixed sp).

const spMarker = "zz-test-token-marker-5c1e9d"

var spRoleNames = map[string]string{"lead": "zz-lead", "reviewer": "zz-reviewer", "qa": "zz-qa", "coder": "zz-coder"}

var spUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

var spSlots = make(chan struct{}, 6)

// spRow is one result row: column name -> value as text (NULL is "NULL").
type spRow map[string]string

// spEnv is an Env on the stateful herdr stub (the default of the Python FactoryEnv) with the spawn fixtures of prep().
type spEnv struct{ *ft.Env }

// spNew builds the env and skips where python3 is absent: spawn, relaunch and tick are still Python commands.
func spNew(t *testing.T) *spEnv {
	t.Helper()
	t.Parallel()
	spSlots <- struct{}{} // each CLI run starts a Python process: bound the load
	t.Cleanup(func() { <-spSlots })
	e := ft.New(t)
	e.NeedPython()
	e.HerdrStateful()
	se := &spEnv{e}
	se.spExec("UPDATE config SET value='herdr' WHERE key='terminal_backend'") // the Python harness pins the TUI: both stubs are on PATH
	return se
}

// spNewPrepped is spNew plus spPrep with the fake unshare.
func spNewPrepped(t *testing.T) *spEnv {
	t.Helper()
	e := spNew(t)
	e.spPrep(true)
	return e
}

func spWrite(t testing.TB, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if mode != 0 {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
}

func spMkdir(t testing.TB, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func spRead(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func spExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func spEq(t testing.TB, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", what, got, want)
	}
}

func spTrue(t testing.TB, what string, ok bool) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: want true", what)
	}
}

func spHas(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func spNotHas(t testing.TB, what, got, bad string) {
	t.Helper()
	if strings.Contains(got, bad) {
		t.Fatalf("%s = %q, must not contain %q", what, got, bad)
	}
}

// spPrep is prep(): a fake claude, a fake unshare (userns ok), the three token files, one cwd per worker role.
func (e *spEnv) spPrep(fakeUnshare bool) {
	t := e.T
	t.Helper()
	spWrite(t, filepath.Join(e.StubBin, "claude"), "#!/bin/sh\nexit 0\n", 0o755)
	if fakeUnshare {
		spWrite(t, filepath.Join(e.StubBin, "unshare"), "#!/bin/sh\nexit 0\n", 0o755)
	}
	for _, name := range []string{"worker-oauth-token", "gh-worker-ro-token", "gh-worker-rw-token"} {
		spWrite(t, e.spTok(name), spMarker+"\n", 0o600)
	}
	e.spExec("INSERT INTO config(key,value) VALUES('worker_gh_repos','volekerb/hivemill') ON CONFLICT(key) DO UPDATE SET value=excluded.value")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-lead")
	for _, role := range []string{"reviewer", "qa", "coder"} {
		wt := e.spCwd(role)
		spMkdir(t, wt)
		if role != "reviewer" {
			spMkdir(t, filepath.Join(wt, ".git"))
		}
	}
}

func (e *spEnv) spTok(name string) string {
	return filepath.Join(e.UserHome, ".config", "factory", name)
}

func (e *spEnv) spCwd(role string) string { return filepath.Join(e.Home, "worktrees", "w-"+role) }

// spAgentDir is the sandbox agent dir of a spawned agent.
func (e *spEnv) spAgentDir(name string) string {
	return filepath.Join(e.UserHome, ".local", "state", "factory", "workers", name)
}

func (e *spEnv) spStubUnshare(script string) {
	e.T.Helper()
	spWrite(e.T, filepath.Join(e.StubBin, "unshare"), script, 0)
}

func (e *spEnv) spExec(q string, args ...any) {
	e.T.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		e.T.Fatalf("%s: %v", q, err)
	}
}

// spRows runs a query and returns the rows keyed by column name.
func (e *spEnv) spRows(q string, args ...any) []spRow {
	e.T.Helper()
	d := e.RawDB()
	defer d.Close()
	rs, err := d.Query(q, args...)
	if err != nil {
		e.T.Fatalf("%s: %v", q, err)
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		e.T.Fatal(err)
	}
	var out []spRow
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			e.T.Fatal(err)
		}
		row := spRow{}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case nil:
				row[c] = "NULL"
			case []byte:
				row[c] = string(v)
			default:
				row[c] = fmt.Sprint(v)
			}
		}
		out = append(out, row)
	}
	if err := rs.Err(); err != nil {
		e.T.Fatal(err)
	}
	return out
}

func (e *spEnv) spAgents(name string) []spRow {
	e.T.Helper()
	return e.spRows("SELECT * FROM agents WHERE name=?", name)
}

func (e *spEnv) spLive(name string) []spRow {
	var live []spRow
	for _, r := range e.spAgents(name) {
		if r["status"] == "live" {
			live = append(live, r)
		}
	}
	return live
}

// spSpawnArgsOf is the decoded spawn_args JSON of the agent's first row.
func (e *spEnv) spSpawnArgsOf(name string) map[string]any {
	e.T.Helper()
	rows := e.spAgents(name)
	if len(rows) == 0 {
		e.T.Fatalf("no agent row %s", name)
	}
	return spJSONObj(e.T, rows[0]["spawn_args"])
}

func spJSONObj(t testing.TB, s string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not a JSON object %q: %v", s, err)
	}
	return m
}

func spTruthy(m map[string]any, k string) bool { b, _ := m[k].(bool); return b }

// spEvents is the events rows of an action (entity_id, detail as text).
func (e *spEnv) spEvents(action string) []spRow {
	e.T.Helper()
	return e.spRows("SELECT entity_id, detail FROM events WHERE action=? ORDER BY id", action)
}

// spReasons is the detail.reason of every event of the action ("sandbox-refused" by default), optionally of one entity.
func (e *spEnv) spReasons(action, entity string) []string {
	e.T.Helper()
	out := []string{}
	for _, r := range e.spEvents(action) {
		if entity == "" || r["entity_id"] == entity {
			s, _ := spJSONObj(e.T, r["detail"])["reason"].(string)
			out = append(out, s)
		}
	}
	return out
}

// spMux is the argv of the herdr calls that start with words.
func (e *spEnv) spMux(words ...string) [][]string {
	e.T.Helper()
	out := [][]string{}
	for _, c := range e.Calls("herdr") {
		if len(c.Argv) >= len(words) && reflect.DeepEqual(c.Argv[:len(words)], words) {
			out = append(out, c.Argv)
		}
	}
	return out
}

// spPanePaths is every PATH=... word of the tab create and pane split calls.
func (e *spEnv) spPanePaths() []string {
	e.T.Helper()
	out := []string{}
	for _, a := range append(e.spMux("tab", "create"), e.spMux("pane", "split")...) {
		for _, x := range a {
			if strings.HasPrefix(x, "PATH=") {
				out = append(out, x)
			}
		}
	}
	return out
}

func (e *spEnv) spSetRoles(v string) { e.T.Helper(); e.OK("config", "sandbox_roles", v) }

func spSpawnArgs(e *spEnv, role, name string, cwd bool) []string {
	if name == "" {
		name = spRoleNames[role]
	}
	a := []string{"spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a"}
	if cwd && role != "lead" {
		a = append(a, "--cwd", e.spCwd(role))
	}
	return a
}

func (e *spEnv) spSpawn(role, name string) ft.Result {
	e.T.Helper()
	return e.Run(spSpawnArgs(e, role, name, true), "")
}

func (e *spEnv) spSpawnNoCwd(role, name string) ft.Result {
	e.T.Helper()
	return e.Run(spSpawnArgs(e, role, name, false), "")
}

// spAssertRefused is assert_refused: a refused spawn, nothing placed, no live row, one more event with the reason.
func (e *spEnv) spAssertRefused(role, reason, name string, cwd bool) ft.Result {
	e.T.Helper()
	if name == "" {
		name = spRoleNames[role]
	}
	before := len(e.Calls("herdr"))
	p := e.Run(spSpawnArgs(e, role, name, cwd), "")
	e.spCheckRefused(p, before, role, reason, name)
	return p
}

// spCheckRefused is the assertions of assert_refused on a spawn that has already run (herdr calls since before).
func (e *spEnv) spCheckRefused(p ft.Result, before int, role, reason, name string) {
	t := e.T
	t.Helper()
	if p.Exit == 0 {
		t.Fatalf("spawn %s: exit 0, want a refusal\n%s", role, p.Stdout)
	}
	spHas(t, "stderr", p.Stderr, "sandbox refused")
	for _, c := range e.Calls("herdr")[before:] {
		if len(c.Argv) < 2 {
			continue
		}
		switch c.Argv[0] + " " + c.Argv[1] {
		case "pane split", "tab create", "agent start", "pane run", "workspace create":
			t.Fatalf("a refused spawn made the call %v", c.Argv)
		}
	}
	spEq(t, "live rows", e.spLive(name), []spRow(nil))
	got := e.spReasons("sandbox-refused", name)
	if len(got) == 0 || got[len(got)-1] != reason {
		t.Fatalf("refusal reasons = %v, want the last to be %q", got, reason)
	}
	evs := e.spRows("SELECT detail FROM events WHERE action='sandbox-refused' AND entity_id=? ORDER BY id", name)
	if last := spJSONObj(t, evs[len(evs)-1]["detail"]); last["role"] != role {
		t.Fatalf("event role = %v, want %s", last["role"], role)
	}
}

// spBreakTemplate swaps the temp home's roles symlink for a copy-by-symlink whose <role> settings template is not JSON.
func (e *spEnv) spBreakTemplate(role string) {
	t := e.T
	t.Helper()
	roles := filepath.Join(e.Home, "roles")
	src, err := filepath.EvalSymlinks(roles)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(roles); err != nil {
		t.Fatal(err)
	}
	spMkdir(t, roles)
	items, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Name() != "settings" {
			if err := os.Symlink(filepath.Join(src, it.Name()), filepath.Join(roles, it.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	spMkdir(t, filepath.Join(roles, "settings"))
	sets, err := os.ReadDir(filepath.Join(src, "settings"))
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range sets {
		if err := os.Symlink(filepath.Join(src, "settings", it.Name()), filepath.Join(roles, "settings", it.Name())); err != nil {
			t.Fatal(err)
		}
	}
	broken := filepath.Join(roles, "settings", role+".json.in")
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	spWrite(t, broken, "{ not json", 0)
}

// spNormalise is normalise(): the factory home, the temp root and uuids replaced by placeholders.
func (e *spEnv) spNormalise(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		a = strings.ReplaceAll(a, e.Home, "<HOME>")
		a = strings.ReplaceAll(a, e.Root, "<TMP>")
		out[i] = spUUID.ReplaceAllString(a, "<SID>")
	}
	return out
}

// spRecord is record(): {role: normalised multiplexer argv per spawn} for a lead, reviewer, qa and coder spawn.
func spRecord(t *testing.T, configure func(e *spEnv)) map[string][][]string {
	t.Helper()
	e := spNewPrepped(t)
	if configure != nil {
		configure(e)
	}
	out := map[string][][]string{}
	for _, role := range []string{"lead", "reviewer", "qa", "coder"} {
		before := len(e.Calls("herdr"))
		p := e.Run(spSpawnArgs(e, role, "", true), "")
		if p.Exit != 0 {
			t.Fatalf("%s: exit %d\n%s%s", role, p.Exit, p.Stdout, p.Stderr)
		}
		rows := [][]string{}
		for _, c := range e.Calls("herdr")[before:] {
			rows = append(rows, e.spNormalise(c.Argv))
		}
		out[role] = rows
	}
	return out
}

// spStripPathEnv is strip_path_env: argv without its `--env PATH=...` pair, and the removed values.
func spStripPathEnv(argv []string) ([]string, []string) {
	out, gone := []string{}, []string{}
	for i := 0; i < len(argv); {
		if argv[i] == "--env" && i+1 < len(argv) && strings.HasPrefix(argv[i+1], "PATH=") {
			gone = append(gone, argv[i+1])
			i += 2
			continue
		}
		out = append(out, argv[i])
		i++
	}
	return out, gone
}

// spGolden reads tests/fixtures/spawn_golden_main.json.
func spGolden(t *testing.T) map[string][][]string {
	t.Helper()
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	g := map[string][][]string{}
	if err := json.Unmarshal([]byte(spRead(t, filepath.Join(root, "tests", "fixtures", "spawn_golden_main.json"))), &g); err != nil {
		t.Fatalf("golden: %v", err)
	}
	return g
}

// spPyLiteral decodes a Python str literal ('...' or "..."), the repr the shim embeds its cfg with.
func spPyLiteral(lit string) (string, error) {
	if len(lit) < 2 || (lit[0] != '\'' && lit[0] != '"') || lit[len(lit)-1] != lit[0] {
		return "", fmt.Errorf("not a python string literal: %.40q", lit)
	}
	s := lit[1 : len(lit)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'x', 'u':
			n := 2
			if s[i] == 'u' {
				n = 4
			}
			if i+n >= len(s) {
				return "", fmt.Errorf("short escape in %.40q", lit)
			}
			v, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
			if err != nil {
				return "", fmt.Errorf("bad escape in %.40q: %w", lit, err)
			}
			b.WriteRune(rune(v))
			i += n
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), nil
}

var spShimCfgRE = regexp.MustCompile(`json\.loads\((.*)\), sys\.argv`)

// spShimCfg is the cfg the spawned agent's shim carries (what workerbox.shim_main reads): the Python shim embeds it
// as json.loads(<repr>), the Go shim as the one quoted argument of `worker-shim`.
func (e *spEnv) spShimCfg(name string) map[string]any {
	t := e.T
	t.Helper()
	shim := spRead(t, filepath.Join(e.spAgentDir(name), "bin", "claude"))
	var js string
	if m := spShimCfgRE.FindStringSubmatch(shim); m != nil {
		s, err := spPyLiteral(m[1])
		if err != nil {
			t.Fatal(err)
		}
		js = s
	} else if _, rest, ok := strings.Cut(shim, "worker-shim '"); ok {
		js = strings.TrimSuffix(rest, "' \"$@\"\n")
	} else {
		t.Fatalf("shim has no cfg line:\n%s", shim)
	}
	return spJSONObj(t, js)
}

// spUserns reports whether this box has user namespaces (nsbox.userns_available()[0]).
func spUserns() (bool, string) { return sandbox.UsernsAvailable("unshare") }

// ftResult and the small constructors keep the researcher file free of the harness import alias.
type ftResult = ft.Result

func optsEnv(env map[string]string) ft.Opts { return ft.Opts{Env: env} }

func taskResearcher() ft.Task {
	return ft.Task{ID: "T-0001", Goal: "G-001", Role: "researcher", Status: "todo", Team: "g-001-a"}
}
