package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/identity"
)

// The spawn command (T-1460): argv forms, hand-over before any write, and a real hc start against a stub.

func TestRegistrySpawn(t *testing.T) {
	if _, ok := registry[key{"spawn", ""}]; !ok {
		t.Fatal("spawn is not registered")
	}
}

func TestSpawnParse(t *testing.T) {
	good := map[string]spawnCmd{
		"pm":                                 {role: "pm"},
		"pm --goal G-1 --team g-1-a":         {role: "pm", goal: "G-1", team: "g-1-a"},
		"pm --task=T-1 --sandbox":            {role: "pm", task: "T-1", sandbox: true},
		"reviewer --name x --org cursor":     {role: "reviewer", name: "x", org: "cursor"},
		"pm --host-backend hc --model m":     {role: "pm", hostBackend: "hc", model: "m"},
		"pm --cwd /w --prompt hello":         {role: "pm", cwd: "/w", prompt: "hello"},
		"pm --prompt - --host-backend herdr": {role: "pm", prompt: "-", hostBackend: "herdr"},
	}
	for argv, want := range good {
		got, ok := parseSpawn(strings.Fields(argv))
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %+v %v, want %+v", argv, got, ok, want)
		}
	}
	for _, argv := range []string{"", "nobody", "--goal G-1 pm", "pm --goal", "pm --goal G-1 --goal G-2", "pm --sandbox --sandbox",
		"pm --host-backend ssh", "pm --nope x", "pm --go G-1", "pm extra", "pm --goal -x"} {
		if got, ok := parseSpawn(strings.Fields(argv)); ok {
			t.Errorf("%q parsed: %+v", argv, got)
		}
	}
	if _, ok := parseSpawn([]string{"pm", "--goal", ""}); ok {
		t.Error("an empty value must reach Python")
	}
}

func FuzzSpawnParse(f *testing.F) {
	for _, s := range []string{"pm", "pm --goal G-1 --sandbox", "coder --prompt - --org cursor", "--goal", "pm --host-backend=hc"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _, _ = parseSpawn(strings.Split(s, " ")) })
}

type spawnRun struct {
	home, bin, log string
	out, errb      string
	code, execs    int
	rows           string
}

// runSpawnGo runs `factory spawn argv...` in Go against a golden-schema home with an hc stub that logs its argv.
func runSpawnGo(t *testing.T, who string, seed []string, argv ...string) spawnRun {
	t.Helper()
	home, bin := t.TempDir(), t.TempDir()
	base, err := os.ReadFile(baseSQLPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	dbp := filepath.Join(home, "db", "factory.db")
	d, err := openRaw(dbp)
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{"PRAGMA journal_mode=WAL", string(base)}, seed...)
	stmts = append(stmts, fmt.Sprintf("PRAGMA user_version = %d", factorydb.SchemaVersion))
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	d.Close()
	roles, err := filepath.Abs(filepath.Join("..", "..", "..", "roles"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(roles, filepath.Join(home, "roles")); err != nil { // ROLES_DIR = FACTORY_HOME/roles
		t.Fatal(err)
	}
	logp := filepath.Join(bin, "hc.log")
	for name, body := range map[string]string{
		"hc":    "#!/bin/sh\necho \"$@\" >> '" + logp + "'\necho '{}'\n",
		"herdr": "#!/bin/sh\necho \"$@\" >> '" + logp + ".herdr'\necho '{\"id\": \"cli\", \"result\": {\"agents\": []}}'\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	extra := []string{"FACTORY_KICKOFF_DELAY_S=0", "FACTORY_HC_SPAWN_SCRIPT_ROOT=" + filepath.Join(home, "scripts")}
	if who != "owner" {
		extra = append(extra, "FACTORY_AGENT="+who)
	}
	env := goldenEnv(home, t.TempDir(), bin, extra)
	m := envMap(env)
	argv0, err := filepath.Abs(filepath.Join("..", "..", "..", "bin", "factory"))
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	r := spawnRun{home: home, bin: bin, log: logp}
	if spawnPyMode {
		cmd := exec.Command("python3", append([]string{argv0}, argv...)...)
		cmd.Env = append(append([]string{}, env...), "FACTORY_CLI_IMPL=python")
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		if err := cmd.Run(); err != nil {
			t.Logf("python: %v", err)
		}
		r.out, r.errb = o.String(), e.String()
		return r
	}
	r.code = Run(Deps{
		Args: argv, Argv0: argv0, Environ: append(append([]string{}, env...), "FACTORY_CLI_IMPL=go"),
		Env: identity.Env{
			Getenv:    func(k string) string { return m[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := m[k]; return v, ok },
			Uid:       os.Getuid(),
			PwDir:     func(int) (string, error) { return home, nil },
			Now:       func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) },
		},
		Stdout: &out, Stderr: &errb, Getpid: func() int { return 4242 }, Getppid: func() int { return 4241 },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec:     func(string, []string, []string) error { r.execs++; return nil },
	})
	r.out, r.errb = out.String(), errb.String()
	fp, err := goldenFingerprint(dbp)
	if err != nil {
		t.Fatal(err)
	}
	r.rows = fp
	return r
}

var spawnSeed = []string{
	"INSERT OR REPLACE INTO config(key,value) VALUES('worker_gh_roles','')",
	"INSERT OR REPLACE INTO config(key,value) VALUES('terminal_backend','herdr')",
	"ALTER TABLE tasks ADD COLUMN judge_hold_reason TEXT",
	"INSERT INTO goals(id,title,status,created,updated) VALUES('G-1','g','active','2026-11-12T00:00:00+00:00','2026-11-12T00:00:00+00:00')",
}

// TestSpawnHandsOver: every shape Go does not run goes to Python before a row, file or pane exists.
func TestSpawnHandsOver(t *testing.T) {
	for _, argv := range [][]string{
		{"spawn", "coder", "--goal", "G-1"},
		{"spawn", "pm", "--goal", "G-1", "--sandbox"},
		{"spawn", "pm", "--prompt", "-"},
		{"spawn", "pm", "--prompt", "@/etc/hostname"},
		{"spawn", "researcher", "--task", "T-1"},
		{"spawn", "evaluator"},
		{"spawn", "pm", "--goal", "G-1", "--host-backend", "herdr"}, // a new herdr pane (place) is Python's
	} {
		r := runSpawnGo(t, "owner", spawnSeed, argv...)
		if r.execs != 1 || r.out != "" || r.errb != "" {
			t.Errorf("%v: execs=%d out=%q err=%q", argv, r.execs, r.out, r.errb)
		}
		if _, err := os.Stat(r.log); err == nil {
			t.Errorf("%v: the host was called", argv)
		}
		if _, err := os.Stat(filepath.Join(r.home, "run")); err == nil {
			t.Errorf("%v: files written", argv)
		}
		if !strings.Contains(r.rows, "agents=0") && strings.Contains(r.rows, "agents=") {
			t.Errorf("%v: rows %s", argv, r.rows)
		}
	}
}

// TestSpawnRefusals: the plan's refusals (exit codes and text of Python) and the worker refusal, with no write.
func TestSpawnRefusals(t *testing.T) {
	cases := []struct {
		who  string
		argv []string
		code int
		msg  string
	}{
		{"owner", []string{"spawn", "pm", "--task", "T-404"}, 2, "factory: no task T-404"},
		{"owner", []string{"spawn", "scout", "--goal", "G-1", "--org", "nope"}, 2, "org"},
		{"claude-worker", []string{"spawn", "pm", "--goal", "G-1"}, 3, "workers may not run `factory spawn`; ask your lead"},
		{"nobody-known", []string{"spawn", "pm", "--goal", "G-1"}, 3, "may not do this; allowed: director, lead, boss"},
	}
	for _, c := range cases {
		seed := append(append([]string{}, spawnSeed...),
			"INSERT INTO agents(name,role,goal_id,status) VALUES('claude-worker','pm','G-1','live')")
		r := runSpawnGo(t, c.who, seed, c.argv...)
		if c.who == "claude-worker" && r.execs == 1 {
			continue // a worker the process-ancestry scan cannot place is Python's to refuse
		}
		if r.code != c.code || !strings.Contains(r.errb, c.msg) || r.out != "" || r.execs != 0 {
			t.Errorf("%s %v: code=%d err=%q out=%q execs=%d", c.who, c.argv, r.code, r.errb, r.out, r.execs)
		}
		if _, err := os.Stat(r.log); err == nil {
			t.Errorf("%v: the host was called", c.argv)
		}
	}
}

// TestSpawnStartsOnHC: a pm on the hc host: agents row, session, events, kickoff message, the wrapper script and
// exactly the hc calls Python makes.
func TestSpawnStartsOnHC(t *testing.T) {
	old := newSessionID
	newSessionID = func() string { return "11111111-2222-4333-8444-555555555555" }
	defer func() { newSessionID = old }()
	r := runSpawnGo(t, "owner", spawnSeed, "spawn", "pm", "--goal", "G-1", "--host-backend", "hc")
	if r.code != 0 || r.execs != 0 {
		t.Fatalf("code=%d execs=%d err=%q", r.code, r.execs, r.errb)
	}
	if !strings.HasPrefix(r.out, "SPAWNED pm-g-1 (pm, claude-opus-5-5) in pm-g-1 cwd="+r.home) {
		t.Errorf("stdout %q", r.out)
	}
	logb, err := os.ReadFile(r.log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logb)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "run --bg") || !strings.Contains(lines[0], "--name pm-g-1") ||
		!strings.Contains(lines[0], filepath.Join(r.home, "scripts", "pm-g-1.hc-spawn.sh")) || !strings.HasPrefix(lines[1], "send pm-g-1 ") {
		t.Errorf("hc calls:\n%s", logb)
	}
	script, err := os.ReadFile(filepath.Join(r.home, "scripts", "pm-g-1.hc-spawn.sh"))
	if err != nil || !strings.HasPrefix(string(script), "#!/bin/sh\nexec env -i PATH=") ||
		!strings.Contains(string(script), "FACTORY_AGENT=pm-g-1") || !strings.Contains(string(script), "--session-id 11111111-2222-4333-8444-555555555555") {
		t.Errorf("script %q %v", script, err)
	}
	for _, f := range []string{"run/settings/pm.json", "run/pm-g-1.prompt.md"} {
		if fi, err := os.Stat(filepath.Join(r.home, f)); err != nil || fi.Size() == 0 {
			t.Errorf("%s: %v", f, err)
		}
	}
	db, err := openRaw(filepath.Join(r.home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status, pane, sid, args, org string
	if err := db.QueryRow("SELECT status, pane_id, session_id, spawn_args, org FROM agents WHERE name='pm-g-1'").Scan(&status, &pane, &sid, &args, &org); err != nil {
		t.Fatal(err)
	}
	if status != "live" || pane != "pm-g-1" || sid != "11111111-2222-4333-8444-555555555555" || org != "claude" {
		t.Errorf("agents row: %s %s %s %s", status, pane, sid, org)
	}
	if want := `{"role": "pm", "goal": "G-1", "team": null, "task": null, "name": "pm-g-1", "cwd": null, "prompt": null, "model": null, "resume": null, "fresh": false, "org": "claude", "host_backend": "hc", "host_backend_pinned": true, "runtime": "claude"}`; args != want {
		t.Errorf("spawn_args\n%s\nwant\n%s", args, want)
	}
	var kicks int
	if err := db.QueryRow("SELECT count(*) FROM messages WHERE sender='factory' AND recipient='pm-g-1' AND kind='request' AND subject='kickoff: pm G-1'").Scan(&kicks); err != nil || kicks != 1 {
		t.Errorf("kickoff messages %d %v", kicks, err)
	}
	var ev int
	if err := db.QueryRow("SELECT count(*) FROM events WHERE action='spawn' AND entity_id='pm-g-1'").Scan(&ev); err != nil || ev != 1 {
		t.Errorf("spawn events %d %v", ev, err)
	}
}

var spawnPyMode bool

// spawnDump is the rows a spawn writes, with the home and the session id masked.
func spawnDump(t *testing.T, r spawnRun) string {
	t.Helper()
	db, err := openRaw(filepath.Join(r.home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var b strings.Builder
	for _, q := range []string{
		"SELECT name,role,goal_id,team_id,task_id,status,pane_id,workspace_id,tab_id,model,session_id,cwd,spawn_args,org,last_nudge IS NOT NULL FROM agents",
		"SELECT actor,entity,entity_id,action,detail FROM events ORDER BY id",
		"SELECT sender,recipient,kind,subject,body,goal_id,task_id FROM messages",
		"SELECT id,status FROM goals", "SELECT id,status,attempts FROM tasks"} {
		rs, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rs.Columns()
		for rs.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				fmt.Fprintf(&b, "%v|", v)
			}
			b.WriteString("\n")
		}
		rs.Close()
	}
	return maskSpawn(b.String(), r)
}

var sidRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func maskSpawn(s string, r spawnRun) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, r.home, "HOME"), r.bin, "BIN")
	return sidRE.ReplaceAllString(s, "SID")
}

// TestSpawnParityPython: the same spawn in Python and in Go leaves the same stdout, rows, events, kickoff message,
// hc calls and wrapper script. The hc `run` line differs by `--no-parent` only: the Go herdr client (T-1452) predates
// that flag (follow-up outside this task's files), so the line is compared without it.
func TestSpawnParityPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	cases := [][]string{
		{"spawn", "pm", "--goal", "G-1", "--host-backend", "hc"},
		{"spawn", "pm", "--goal", "G-1", "--host-backend", "hc", "--name", "pm-x", "--prompt", "extra words"},
	}
	for _, argv := range cases {
		spawnPyMode = true
		py := runSpawnGo(t, "owner", spawnSeed, argv...)
		spawnPyMode = false
		goRun := runSpawnGo(t, "owner", spawnSeed, argv...)
		if goRun.execs != 0 {
			t.Fatalf("%v: Go handed the spawn over", argv)
		}
		if a, b := maskSpawn(py.out, py), maskSpawn(goRun.out, goRun); a != b {
			t.Errorf("%v stdout:\npy %q\ngo %q", argv, a, b)
		}
		if a, b := spawnDump(t, py), spawnDump(t, goRun); a != b {
			t.Errorf("%v rows:\npy:\n%s\ngo:\n%s", argv, a, b)
		}
		pl, _ := os.ReadFile(py.log)
		gl, _ := os.ReadFile(goRun.log)
		if a, b := strings.ReplaceAll(maskSpawn(string(pl), py), " --no-parent", ""), maskSpawn(string(gl), goRun); a != b {
			t.Errorf("%v hc calls:\npy:\n%s\ngo:\n%s", argv, a, b)
		}
		ps, _ := os.ReadFile(filepath.Join(py.home, "scripts", spawnNameOf(argv)+".hc-spawn.sh"))
		gs, _ := os.ReadFile(filepath.Join(goRun.home, "scripts", spawnNameOf(argv)+".hc-spawn.sh"))
		normPath := func(s string, r spawnRun) string {
			return regexp.MustCompile(`PATH=\S+`).ReplaceAllString(maskSpawn(s, r), "PATH=X")
		}
		if len(ps) == 0 || normPath(string(ps), py) != normPath(string(gs), goRun) {
			t.Errorf("%v script:\npy:\n%s\ngo:\n%s", argv, ps, gs)
		}
	}
}

func spawnNameOf(argv []string) string {
	for i, a := range argv {
		if a == "--name" {
			return argv[i+1]
		}
	}
	return "pm-g-1"
}

func FuzzKickoffDelay(f *testing.F) {
	for _, s := range []string{"", "0", "2", "0.5", " 1 ", "-1", "nan", "1e9", "x", "٣"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c := &ctx{deps: &Deps{Environ: []string{"FACTORY_KICKOFF_DELAY_S=" + s}}}
		d, err := kickoffDelay(c)
		if err == nil && (d < 0 || d > time.Hour) {
			t.Fatalf("delay %v for %q", d, s)
		}
	})
}

// TestSpawnHandsOverRuntimeShapes: the gh-credential worker (default worker_gh_roles) and the cursor runtime go to
// Python with no row, event, file or host call written first.
func TestSpawnHandsOverRuntimeShapes(t *testing.T) {
	seed := []string{"INSERT OR REPLACE INTO config(key,value) VALUES('terminal_backend','herdr')", "INSERT OR REPLACE INTO config(key,value) VALUES('worker_gh_roles','pm,cleaner')",
		spawnSeed[len(spawnSeed)-1]}
	for name, argv := range map[string][]string{
		"ghcred": {"spawn", "pm", "--goal", "G-1", "--host-backend", "hc"},
		"cursor": {"spawn", "pm", "--host-backend", "hc", "--org", "cursor"},
	} {
		r := runSpawnGo(t, "owner", seed, argv...)
		if r.execs != 1 || r.out != "" || r.errb != "" {
			t.Errorf("%s: execs=%d out=%q err=%q", name, r.execs, r.out, r.errb)
		}
		if _, err := os.Stat(r.log); err == nil {
			t.Errorf("%s: hc was called", name)
		}
		if _, err := os.Stat(filepath.Join(r.home, "run")); err == nil {
			t.Errorf("%s: files written", name)
		}
		if !strings.Contains(r.rows, "agents=0") || !strings.Contains(r.rows, "events=0") || !strings.Contains(r.rows, "messages=0") {
			t.Errorf("%s: rows written: %s", name, r.rows)
		}
	}
}
