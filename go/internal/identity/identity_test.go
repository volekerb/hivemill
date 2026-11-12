package identity

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
)

type fixture struct {
	t      *testing.T
	home   string
	pw     string
	stderr *bytes.Buffer
	env    map[string]string
	e      Env
}

func newFixture(t *testing.T, env map[string]string) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, home: filepath.Join(root, "fhome"), pw: filepath.Join(root, "pw"), stderr: &bytes.Buffer{}, env: env}
	os.MkdirAll(f.pw, 0o755)
	f.env["FACTORY_HOME"] = f.home
	f.e = Env{
		Getenv:    func(k string) string { return f.env[k] },
		LookupEnv: func(k string) (string, bool) { v, ok := f.env[k]; return v, ok },
		Uid:       os.Getuid(),
		PwDir:     func(int) (string, error) { return f.pw, nil },
		Now:       func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.FixedZone("CET", 3600)) },
		Stderr:    f.stderr,
	}
	return f
}

func (f *fixture) seed(rows ...[3]string) string {
	path := dbtest.New(f.t, f.home, factorydb.SchemaVersion)
	d := dbtest.Raw(f.t, path)
	defer d.Close()
	for _, r := range rows {
		if _, err := d.Exec("INSERT INTO agents(name,role,status,pane_id) VALUES(?,?,?,?)", r[0], "coder", r[1], r[2]); err != nil {
			f.t.Fatal(err)
		}
	}
	return path
}

func (f *fixture) resolve() (string, error) {
	return f.e.Resolve(func() (*factorydb.DB, error) { return factorydb.Open(factorydb.ResolvePath(f.home)) })
}

func TestIdentity(t *testing.T) {
	type tc struct {
		name   string
		env    map[string]string
		agents [][3]string // name, status, pane
		want   string
		hint   string
		code   int
	}
	cases := []tc{
		{name: "agent name set", env: map[string]string{"FACTORY_AGENT": "claude-coder-t-1"}, want: "claude-coder-t-1"},
		{name: "unset outside agent context is owner", env: map[string]string{}, want: "owner"},
		{name: "CLAUDECODE and unset is unknown", env: map[string]string{"CLAUDECODE": "1"}, want: Unknown},
		{name: "owner outside a pane is owner", env: map[string]string{"FACTORY_AGENT": "owner", "CLAUDECODE": "1"}, want: "owner"},
		{name: "pane recovers the single live agent", env: map[string]string{"HERDR_PANE_ID": "p-7"},
			agents: [][3]string{{"a-one", "live", "p-7"}, {"a-old", "exited", "p-7"}}, want: "a-one",
			hint: "factory: FACTORY_AGENT unset; recovered identity a-one from pane p-7. export FACTORY_AGENT=a-one\n"},
		{name: "pane with two live agents is unknown", env: map[string]string{"HERDR_PANE_ID": "p-7"},
			agents: [][3]string{{"a-one", "live", "p-7"}, {"a-two", "live", "p-7"}}, want: Unknown},
		{name: "pane of a done agent is unknown", env: map[string]string{"HERDR_PANE_ID": "p-7"},
			agents: [][3]string{{"a-one", "done", "p-7"}}, want: Unknown},
		{name: "pane matching nothing is unknown", env: map[string]string{"HERDR_PANE_ID": "p-9"},
			agents: [][3]string{{"a-one", "live", "p-7"}}, want: Unknown},
		{name: "owner on an unregistered pane is owner", env: map[string]string{"FACTORY_AGENT": "owner", "HERDR_PANE_ID": "p-9"},
			agents: [][3]string{{"a-one", "live", "p-7"}}, want: "owner"},
		{name: "owner on an agent pane is a spoof", env: map[string]string{"FACTORY_AGENT": "owner", "HERDR_PANE_ID": "p-7"},
			agents: [][3]string{{"a-one", "live", "p-7"}, {"a-two", "done", "p-7"}}, code: 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.env)
			path := f.seed(c.agents...)
			got, err := f.resolve()
			if c.code != 0 {
				var ee *ExitError
				if !errors.As(err, &ee) || ee.Code != c.code {
					t.Fatalf("want exit %d, got %q %v", c.code, got, err)
				}
				if want := "pane p-7 belongs to agent a-one,a-two; it may not claim FACTORY_AGENT=owner"; ee.Msg != want {
					t.Fatalf("message %q want %q", ee.Msg, want)
				}
				d := dbtest.Raw(t, path)
				defer d.Close()
				var ts, actor, entity, id, action, detail string
				if err := d.QueryRow("SELECT ts,actor,entity,entity_id,action,detail FROM events").
					Scan(&ts, &actor, &entity, &id, &action, &detail); err != nil {
					t.Fatalf("no events row: %v", err)
				}
				if ts != "2026-11-12T01:02:03+01:00" || actor != "pane:p-7" || entity != "agent" || id != "a-one,a-two" ||
					action != "identity-spoof" || detail != "pane p-7 claims FACTORY_AGENT=owner" {
					t.Fatalf("events row = %v", []string{ts, actor, entity, id, action, detail})
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q err %v, want %q", got, err, c.want)
			}
			if f.stderr.String() != c.hint {
				t.Fatalf("stderr %q want %q", f.stderr.String(), c.hint)
			}
		})
	}
}

func TestIdentityNoEventWithoutSpoof(t *testing.T) {
	f := newFixture(t, map[string]string{"HERDR_PANE_ID": "p-7"})
	path := f.seed([3]string{"a-one", "live", "p-7"})
	if _, err := f.resolve(); err != nil {
		t.Fatal(err)
	}
	d := dbtest.Raw(t, path)
	defer d.Close()
	var n int
	d.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
	if n != 0 {
		t.Fatalf("%d events rows after a plain recovery", n)
	}
}

func TestIdentityPaneWithoutDatabase(t *testing.T) {
	f := newFixture(t, map[string]string{"HERDR_PANE_ID": "p-7"}) // no DB file: nothing can recover the pane
	if got, err := f.resolve(); err != nil || got != Unknown {
		t.Fatalf("got %q %v", got, err)
	}
	f = newFixture(t, map[string]string{"HERDR_PANE_ID": "p-7", "FACTORY_AGENT": "owner"})
	if got, err := f.resolve(); err != nil || got != "owner" {
		t.Fatalf("owner without DB: got %q %v", got, err)
	}
}

func TestIdentityBrokenDatabaseDelegates(t *testing.T) {
	f := newFixture(t, map[string]string{"HERDR_PANE_ID": "p-7"})
	f.seed()
	os.WriteFile(factorydb.ResolvePath(f.home), []byte("not a database"), 0o600)
	if _, err := f.resolve(); !errors.Is(err, ErrDelegate) {
		t.Fatalf("want ErrDelegate, got %v", err)
	}
}

func TestRequireKnown(t *testing.T) {
	val := "x"
	type tc struct {
		c    Cmd
		fail bool
	}
	cases := []tc{
		{Cmd{Name: "whoami"}, false}, {Cmd{Name: "agents"}, false}, {Cmd{Name: "flags"}, false},
		{Cmd{Name: "config"}, false},             // config read
		{Cmd{Name: "config", Value: &val}, true}, // config write
		{Cmd{Name: "task", Action: "show"}, false}, {Cmd{Name: "task", Action: "update"}, true},
		{Cmd{Name: "msg"}, true}, {Cmd{Name: "digest"}, false}, {Cmd{Name: "digest", Send: true}, true},
		{Cmd{Name: "gate", Action: "run"}, true}, {Cmd{Name: "gate", Action: "list"}, false},
	}
	for _, c := range cases {
		err := RequireKnown(Unknown, c.c)
		var ee *ExitError
		if c.fail && (!errors.As(err, &ee) || ee.Code != 3 || !strings.HasPrefix(ee.Msg, "identity unknown: this looks like an agent pane")) {
			t.Errorf("%+v: want exit 3, got %v", c.c, err)
		}
		if !c.fail && err != nil {
			t.Errorf("%+v: want nil, got %v", c.c, err)
		}
		if err := RequireKnown("owner", c.c); err != nil {
			t.Errorf("%+v: a known identity is never refused: %v", c.c, err)
		}
	}
}

func TestIdentityLiveHomes(t *testing.T) {
	f := newFixture(t, map[string]string{})
	def := Realpath(filepath.Join(f.pw, "factory"))
	primary, homes, err := f.e.DeriveLiveHomes()
	if err != nil || primary != def || len(homes) != 1 {
		t.Fatalf("default: %v %v %v", primary, homes, err)
	}
	extra := filepath.Join(f.pw, "elsewhere")
	cfg := filepath.Join(f.pw, ".config", "factory")
	os.MkdirAll(cfg, 0o755)
	lh := filepath.Join(cfg, "live_home")
	check := func(content string, mode os.FileMode, want bool) {
		t.Helper()
		os.Remove(lh)
		os.WriteFile(lh, []byte(content), mode)
		os.Chmod(lh, mode)
		p, h, _ := f.e.DeriveLiveHomes()
		if got := h[extra]; got != want || (want && p != extra) {
			t.Errorf("content %q mode %o: live=%v primary=%s want live=%v", content, mode, got, p, want)
		}
	}
	check(extra+"\n", 0o600, true)
	check(extra, 0o644, true)
	check(extra+"\n", 0o666, false)            // group/other writable: ignored
	check("relative/path\n", 0o600, false)     // not absolute
	check(extra+"\n"+extra+"\n", 0o600, false) // two lines
	check("", 0o600, false)
	// the file as a symlink is ignored
	os.Remove(lh)
	target := filepath.Join(f.pw, "real_live_home")
	os.WriteFile(target, []byte(extra+"\n"), 0o600)
	os.Symlink(target, lh)
	if _, h, _ := f.e.DeriveLiveHomes(); h[extra] {
		t.Error("a symlinked live_home must be ignored")
	}
}

func TestIdentityEvaluatorHomeGuard(t *testing.T) {
	var ee *ExitError
	f := newFixture(t, map[string]string{"FACTORY_ROLE": "evaluator"})
	delete(f.env, "FACTORY_HOME")
	if err := f.e.EvaluatorHomeGuard(); !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("unset FACTORY_HOME: %v", err)
	}
	f.env["FACTORY_HOME"] = "  "
	if err := f.e.EvaluatorHomeGuard(); !errors.As(err, &ee) {
		t.Fatalf("blank FACTORY_HOME: %v", err)
	}
	f.env["FACTORY_HOME"] = filepath.Join(f.pw, "factory") // the live home
	if err := f.e.EvaluatorHomeGuard(); !errors.As(err, &ee) || !strings.Contains(ee.Msg, "may not use the live factory home") {
		t.Fatalf("live FACTORY_HOME: %v", err)
	}
	f.env["FACTORY_HOME"] = filepath.Join(t.TempDir(), "tmp-home")
	if err := f.e.EvaluatorHomeGuard(); err != nil {
		t.Fatalf("temp home must pass: %v", err)
	}
	f.env["FACTORY_ROLE"] = "coder"
	f.env["FACTORY_HOME"] = filepath.Join(f.pw, "factory")
	if err := f.e.EvaluatorHomeGuard(); err != nil {
		t.Fatalf("only the evaluator is guarded: %v", err)
	}
}

func TestRealpath(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link"))
	if got := Realpath(filepath.Join(root, "link", "b", "..", "missing", "x")); got != filepath.Join(root, "a", "missing", "x") {
		t.Fatalf("got %s", got)
	}
}
