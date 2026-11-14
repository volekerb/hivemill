package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"factory/go/internal/cli/pydefaults"
	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
	"factory/go/internal/identity"
)

type execCall struct {
	path string
	argv []string
	env  []string
}

type rig struct {
	t      *testing.T
	home   string
	repo   string // fake checkout: <repo>/bin/factory + flags.json
	pw     string
	env    map[string]string
	out    bytes.Buffer
	errb   bytes.Buffer
	execs  []execCall
	dbPath string
}

func newRig(t *testing.T, env map[string]string) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{t: t, home: filepath.Join(root, "fhome"), repo: filepath.Join(root, "checkout"), pw: filepath.Join(root, "pw"), env: env}
	os.MkdirAll(filepath.Join(r.repo, "bin"), 0o755)
	os.MkdirAll(r.pw, 0o755)
	os.WriteFile(filepath.Join(r.repo, "bin", "factory"), []byte("#!/usr/bin/env python3\n"), 0o755)
	flags, err := os.ReadFile(repoFile(t, "bin/flags.json"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(r.repo, "bin", "flags.json"), flags, 0o644)
	r.env["FACTORY_HOME"] = r.home
	r.env["HOME"] = r.pw
	r.dbPath = dbtest.New(t, r.home, factorydb.SchemaVersion)
	return r
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	wd, _ := os.Getwd() // go/internal/cli
	return filepath.Join(wd, "..", "..", "..", filepath.FromSlash(rel))
}

func (r *rig) sql(q string, args ...any) {
	r.t.Helper()
	d := dbtest.Raw(r.t, r.dbPath)
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) run(args ...string) int {
	r.out.Reset()
	r.errb.Reset()
	r.execs = nil
	var environ []string
	for k, v := range r.env {
		environ = append(environ, k+"="+v)
	}
	environ = append(environ, "FACTORY_CLI_IMPL=go") // must be replaced on delegation
	return Run(Deps{
		Args: args, Argv0: filepath.Join(r.repo, "bin", "factory"), Environ: environ,
		Env: identity.Env{
			Getenv:    func(k string) string { return r.env[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := r.env[k]; return v, ok },
			Uid:       os.Getuid(),
			PwDir:     func(int) (string, error) { return r.pw, nil },
			Now:       func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) },
		},
		Stdout: &r.out, Stderr: &r.errb,
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec: func(path string, argv, env []string) error {
			r.execs = append(r.execs, execCall{path, argv, env})
			return nil
		},
	})
}

func TestDelegate(t *testing.T) {
	cases := []struct {
		name string
		args []string
		prep func(r *rig)
	}{
		{"--help", []string{"--help"}, nil},
		{"whoami -h", []string{"whoami", "-h"}, nil},
		{"config write", []string{"config", "k", "v"}, nil},
		{"task tier", []string{"task", "tier", "T-1"}, nil},
		{"agents --bogus", []string{"agents", "--bogus"}, nil},
		{"no args", nil, nil},
		{"FACTORY_CLI_IMPL=python forces python", []string{"whoami"}, func(r *rig) { r.env["FACTORY_CLI_IMPL"] = "python" }},
		{"whoami extra arg", []string{"whoami", "x"}, nil},
		{"config --changed with key", []string{"config", "--changed", "k"}, nil},
		{"config --changed --check", []string{"config", "--changed", "--check"}, nil},
		{"flags abbreviation", []string{"flags", "--js"}, nil},
		{"user_version mismatch", []string{"whoami"}, func(r *rig) { r.sql("PRAGMA user_version = 99") }},
		{"user_version zero (Python never stamped it)", []string{"agents"}, func(r *rig) { r.sql("PRAGMA user_version = 0") }},
		{"agents --all on a DB missing agents.stop_requested_at", []string{"agents", "--all"}, func(r *rig) {
			r.sql("ALTER TABLE agents DROP COLUMN stop_requested_at")
		}},
		{"no database", []string{"whoami"}, func(r *rig) { os.Remove(r.dbPath) }},
		{"flags.json missing", []string{"flags"}, func(r *rig) { os.Remove(filepath.Join(r.repo, "bin", "flags.json")) }},
		{"config --check of a validated key", []string{"config", "--check"}, func(r *rig) {
			r.sql("INSERT INTO config(key,value) VALUES('guard_impl','nonsense')")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, map[string]string{"FACTORY_AGENT": "someone"})
			if c.prep != nil {
				c.prep(r)
			}
			r.run(c.args...)
			if len(r.execs) != 1 {
				t.Fatalf("want exactly one exec, got %d (stdout %q stderr %q)", len(r.execs), r.out.String(), r.errb.String())
			}
			x := r.execs[0]
			wantArgv := append([]string{"python3", filepath.Join(r.repo, "bin", "factory")}, c.args...)
			if x.path != "/usr/bin/python3" || !reflect.DeepEqual(x.argv, wantArgv) {
				t.Fatalf("exec %s %q, want python3 %q", x.path, x.argv, wantArgv)
			}
			impl := 0
			for _, kv := range x.env {
				if strings.HasPrefix(kv, "FACTORY_CLI_IMPL=") {
					impl++
					if kv != "FACTORY_CLI_IMPL=python" {
						t.Fatalf("env %q", kv)
					}
				}
			}
			if impl != 1 {
				t.Fatalf("FACTORY_CLI_IMPL appears %d times in %q", impl, x.env)
			}
			if r.out.Len() != 0 || r.errb.Len() != 0 {
				t.Fatalf("a delegated command must print nothing itself: %q %q", r.out.String(), r.errb.String())
			}
		})
	}
}

func TestDelegateResolvesSymlinkedEntry(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "x"})
	link := filepath.Join(t.TempDir(), "factory")
	os.Symlink(filepath.Join(r.repo, "bin", "factory"), link)
	d := Deps{Argv0: link}
	if got, want := d.pyEntry(), identity.Realpath(filepath.Join(r.repo, "bin", "factory")); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestDelegateExecFailureIsReported(t *testing.T) {
	var errb bytes.Buffer
	d := Deps{Args: []string{"task"}, Argv0: "/x/bin/factory", Stderr: &errb,
		LookPath: func(string) (string, error) { return "/usr/bin/python3", nil },
		Exec:     func(string, []string, []string) error { return errors.New("boom") }}
	if code := d.delegate(); code != 1 || !strings.Contains(errb.String(), "exec python3 /x/bin/factory failed: boom") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}

func TestWhoami(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "claude-coder-t-1335"})
	r.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,model,status) VALUES('claude-coder-t-1335','coder','G-041',NULL,'T-1335','claude-sonnet-5-5','live')")
	if code := r.run("whoami"); code != 0 {
		t.Fatalf("exit %d %q", code, r.errb.String())
	}
	want := "claude-coder-t-1335  role=coder  goal=G-041  team=None  task=T-1335  model=claude-sonnet-5-5\n"
	if r.out.String() != want || len(r.execs) != 0 {
		t.Fatalf("got %q want %q (execs %d)", r.out.String(), want, len(r.execs))
	}
	delete(r.env, "FACTORY_AGENT")
	r.run("whoami")
	if r.out.String() != "owner (owner — not a factory agent)\n" {
		t.Fatalf("owner: %q", r.out.String())
	}
	r.env["CLAUDECODE"] = "1"
	if code := r.run("whoami"); code != 0 || r.out.String() != "unknown (agent pane, identity lost — no privileges; export FACTORY_AGENT=<your agent name>)\n" {
		t.Fatalf("unknown: %d %q", code, r.out.String())
	}
}

func TestSpoofExitsThree(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner", "HERDR_PANE_ID": "p-1"})
	r.sql("INSERT INTO agents(name,role,status,pane_id) VALUES('a-one','coder','live','p-1')")
	if code := r.run("agents"); code != 3 {
		t.Fatalf("exit %d", code)
	}
	if r.errb.String() != "factory: pane p-1 belongs to agent a-one; it may not claim FACTORY_AGENT=owner\n" || r.out.Len() != 0 {
		t.Fatalf("stderr %q stdout %q", r.errb.String(), r.out.String())
	}
	d := dbtest.Raw(t, r.dbPath)
	defer d.Close()
	var n int
	d.QueryRow("SELECT COUNT(*) FROM events WHERE action='identity-spoof' AND actor='pane:p-1'").Scan(&n)
	if n != 1 {
		t.Fatalf("identity-spoof rows: %d", n)
	}
}

func TestAgents(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
	r.sql(`INSERT INTO agents(name,role,status,spawn_args,flagless,started,pane_id,model) VALUES('é-live','coder','live','{"runtime":"cursor"}',1,'2026-11-12T00:00:01+01:00','p-1','日本')`)
	r.sql("INSERT INTO agents(name,role,status,org,relaunch_requested_at,started) VALUES('queued-1','pm','queued','claude','2026-11-12T00:00:00+01:00','2026-11-12T00:00:02+01:00')")
	r.sql("INSERT INTO agents(name,role,status) VALUES('gone','qa','exited')")
	r.run("agents")
	want := "NAME      ORG     ROLE   STATUS  RUNTIME  FLAGLESS  RELAUNCH          GOAL_ID  TEAM_ID  TASK_ID  PANE_ID  MODEL  STARTED                    BACKEND\n" +
		"é-live    cursor  coder  live    cursor   FLAGLESS                                               p-1      日本     2026-11-12T00:00:01+01:00  herdr  \n" +
		"queued-1  claude  pm     queued  claude             RELAUNCH-PENDING                                             2026-11-12T00:00:02+01:00  herdr  \n"
	if r.out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.out.String(), want)
	}
	r.run("agents", "--all")
	if !strings.Contains(r.out.String(), "gone") {
		t.Fatalf("--all must list exited agents:\n%s", r.out.String())
	}
	r.sql("DELETE FROM agents")
	r.run("agents")
	if r.out.String() != "(none)\n" {
		t.Fatalf("empty: %q", r.out.String())
	}
}

func TestConfigReads(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
	r.pw = t.TempDir() // no guard binary installed under this home
	r.env["HOME"] = r.pw
	r.sql("INSERT INTO config(key,value) VALUES('max_live_agents','9')")
	r.run("config", "max_live_agents")
	if r.out.String() != "9\n" {
		t.Fatalf("key read %q", r.out.String())
	}
	r.run("config", "model_cleaner")
	if r.out.String() != "\n" {
		t.Fatalf("default read %q", r.out.String())
	}
	if code := r.run("config", "no_such_key"); code != 2 || !strings.HasPrefix(r.errb.String(), "factory: unknown key no_such_key; known: guard_impl, model_cleaner,") {
		t.Fatalf("unknown key: %d %q", code, r.errb.String())
	}
	r.run("config", "--changed")
	if r.out.String() != "max_live_agents = 9 (default: 6)\n" {
		t.Fatalf("--changed %q", r.out.String())
	}
	r.sql("DELETE FROM config")
	r.run("config", "--changed")
	if r.out.String() != "no changes from defaults\n" {
		t.Fatalf("--changed clean %q", r.out.String())
	}
	if code := r.run("config", "--check"); code != 0 || r.out.Len() != 0 {
		t.Fatalf("--check clean: %d %q", code, r.out.String())
	}
	r.sql("INSERT INTO config(key,value) VALUES('max_live_agents','9')")
	if code := r.run("config", "--check"); code != 0 || len(r.execs) != 0 {
		t.Fatalf("--check on an unvalidated key must stay in Go: %d", code)
	}
	r.run("config")
	lines := strings.Split(strings.TrimRight(r.out.String(), "\n"), "\n")
	if len(lines) != len(DefaultConfig)+1 || lines[0] != "guard_impl = python" || lines[len(lines)-1] != "guard binary: missing" {
		t.Fatalf("config listing: %d lines, first %q last %q", len(lines), lines[0], lines[len(lines)-1])
	}
	found := false
	for _, l := range lines {
		found = found || l == "max_live_agents = 9"
	}
	if !found {
		t.Fatalf("stored value missing from the listing")
	}
}

func TestUnknownIdentityRunsReads(t *testing.T) {
	r := newRig(t, map[string]string{"CLAUDECODE": "1"})
	r.pw = t.TempDir()
	r.env["HOME"] = r.pw
	for _, args := range [][]string{{"whoami"}, {"agents"}, {"flags"}, {"config"}, {"config", "--changed"}} {
		if code := r.run(args...); code != 0 || len(r.execs) != 0 {
			t.Errorf("%v: exit %d, %d execs, stderr %q", args, code, len(r.execs), r.errb.String())
		}
	}
	// a config write is Python's job; the Go binary never sees it
	if r.run("config", "k", "v"); len(r.execs) != 1 {
		t.Errorf("config write must be delegated")
	}
}

func TestFlags(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
	r.sql("INSERT INTO config(key,value) VALUES('coder_ci_handoff','0')")
	if code := r.run("flags"); code != 0 {
		t.Fatalf("exit %d %q", code, r.errb.String())
	}
	lines := strings.Split(strings.TrimRight(r.out.String(), "\n"), "\n")
	if !strings.HasPrefix(lines[0], "KEY") || !strings.Contains(lines[0], "STATE") || len(lines) < 2 {
		t.Fatalf("flags table:\n%s", r.out.String())
	}
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "affected_smoke") {
			t.Fatalf("a plain setting must not show without --all: %q", l)
		}
	}
	r.run("flags", "--all")
	if !strings.Contains(r.out.String(), "affected_smoke") {
		t.Fatalf("--all must list settings")
	}
	r.run("flags", "--json")
	js := r.out.String()
	if !strings.HasPrefix(js, "[\n {\n  \"key\": \"affected_smoke\",\n  \"kind\": \"setting\",\n") || !strings.HasSuffix(js, "\n }\n]\n") {
		t.Fatalf("json shape:\n%.300s", js)
	}
	if !strings.Contains(js, "\"current\": \"0\"") {
		t.Fatalf("current value of coder_ci_handoff missing")
	}
}

func TestFlagsJSONMatchesPythonDumps(t *testing.T) {
	// json.dumps(value, indent=1) of the input below, produced by python3 (ensure_ascii, key order kept).
	const want = "[\n {\n  \"key\": \"\\u00e9\\\"\\\\\\n\\u0001\\u65e5\\ud83d\\ude00\",\n  \"n\": 12,\n  \"ok\": true,\n  \"none\": null,\n  \"e\": [],\n  \"o\": {},\n  \"l\": [\n   \"a\",\n   1\n  ]\n }\n]"
	n, err := parseJSON([]byte(`[{"key":"é\"\\\n\u0001日😀","n":12,"ok":true,"none":null,"e":[],"o":{},"l":["a",1]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := parseJSON([]byte(`["\u007f",-0]`)); dumpIndent1(d) != "[\n \"\\u007f\",\n 0\n]" {
		t.Fatalf("DEL / -0: %q", dumpIndent1(d))
	}
	if got := dumpIndent1(n); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	for _, bad := range []string{`[1.5]`, `[1e3]`, `{"a":1,"a":2}`, `[NaN]`, `["\\ud800"]`, "[\"\xff\"]"} {
		if _, err := parseJSON([]byte(bad)); err == nil {
			t.Errorf("%s must be refused so Python answers", bad)
		}
	}
}

func TestTableNonASCII(t *testing.T) {
	cols := []string{"name", "role", "model", "note"}
	rows := [][]string{
		{"café-é", "coder", "日本語-model", ""},
		{"日本", "pm", "claude-opus-5-5", strings.Repeat("x", 70)},
		{"plain", "qa", strings.Repeat("é", 61), strings.Repeat("日本", 40)},
		{"😀emoji", "judge", "m", ""},
	}
	var b bytes.Buffer
	Table(&b, cols, rows)
	Table(&b, []string{"name"}, nil)
	want, err := os.ReadFile("testdata/table_nonascii.golden")
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Fatalf("table differs from the Python golden:\n got %q\nwant %q", b.String(), want)
	}
}

func TestNoWrites(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
	r.pw = t.TempDir()
	r.env["HOME"] = r.pw
	r.sql("INSERT INTO agents(name,role,status) VALUES('a','coder','live')")
	r.sql("INSERT INTO config(key,value) VALUES('max_live_agents','9')")
	before := dbFingerprint(t, r.dbPath)
	for _, args := range [][]string{{"whoami"}, {"agents"}, {"agents", "--all"}, {"flags"}, {"flags", "--all"}, {"flags", "--json"},
		{"config"}, {"config", "max_live_agents"}, {"config", "--changed"}, {"config", "--check"}} {
		if code := r.run(args...); code != 0 || len(r.execs) != 0 {
			t.Fatalf("%v: exit %d execs %d stderr %q", args, code, len(r.execs), r.errb.String())
		}
		if after := dbFingerprint(t, r.dbPath); after != before {
			t.Fatalf("%v changed the database: %s -> %s", args, before, after)
		}
	}
}

// dbFingerprint is the main file's mtime and size, PRAGMA data_version on a fresh connection and the events count.
func dbFingerprint(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := factorydb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var events int64
	db.QueryRow("SELECT COUNT(*) FROM events").Scan(&events)
	dv, err := db.DataVersion()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join([]string{fi.ModTime().UTC().Format(time.RFC3339Nano), strconv.FormatInt(fi.Size(), 10),
		strconv.FormatInt(dv, 10), strconv.FormatInt(events, 10)}, "|")
}

func TestDefaultsMatchFlagsJSON(t *testing.T) {
	src, err := os.ReadFile(repoFile(t, "bin/factory_cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	lit, err := pydefaults.Parse(src)
	if err != nil {
		t.Fatalf("DEFAULT_CONFIG literal: %v", err)
	}
	if len(lit) != len(DefaultConfig) {
		t.Fatalf("defaults_gen.go has %d keys, DEFAULT_CONFIG has %d: run `go generate ./internal/cli`", len(DefaultConfig), len(lit))
	}
	for i, e := range lit {
		if DefaultConfig[i].Key != e.Key || DefaultConfig[i].Value != e.Value {
			t.Fatalf("defaults_gen.go drifted at %s (%q vs %q): run `go generate ./internal/cli`", e.Key, DefaultConfig[i].Value, e.Value)
		}
	}
	flagsPath := repoFile(t, "bin/flags.json")
	if p := os.Getenv("FACTORY_TEST_FLAGS_JSON"); p != "" {
		flagsPath = p // a scratch copy, to prove a changed default fails
	}
	raw, err := os.ReadFile(flagsPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := root.get("keys")
	defaults := defaultMap()
	checked := 0
	for _, e := range keys.arr {
		k, _ := e.get("key")
		d, hasDef := e.get("default")
		want, known := defaults[k.s]
		if !known || !hasDef {
			continue
		}
		checked++
		if d.kind != 's' || d.s != want {
			t.Errorf("flags.json default of %s is %q, DEFAULT_CONFIG says %q", k.s, d.s, want)
		}
	}
	if checked < 20 {
		t.Fatalf("only %d flags.json keys matched DEFAULT_CONFIG; the check is not looking at the right file", checked)
	}
}
