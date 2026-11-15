package cli

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/identity"
)

// cliEnv is a live-home factory (FACTORY_HOME = <pw>/factory) on testdata/golden/base.sql plus testdata/done/seed.sql,
// driven through Run. The Exec seam records a hand-over to Python instead of replacing the process.
type cliEnv struct {
	t     *testing.T
	pw    string
	home  string
	env   map[string]string
	cwd   string
	execs int
}

func newCliEnv(t *testing.T) *cliEnv { return newCliEnvLive(t, true) }

// newCliEnvLive: live=false puts FACTORY_HOME outside <pw>/factory, so git may use the file transport (a test origin).
func newCliEnvLive(t *testing.T, live bool) *cliEnv {
	t.Helper()
	root := t.TempDir()
	e := &cliEnv{t: t, pw: filepath.Join(root, "pw")}
	e.home = filepath.Join(e.pw, "factory")
	if !live {
		e.home = filepath.Join(root, "fhome")
	}
	if err := os.MkdirAll(filepath.Join(e.home, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile(filepath.Join(goldenDir, "base.sql"))
	if err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("testdata/done/seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	d, err := openRaw(filepath.Join(e.home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{"PRAGMA journal_mode=WAL", string(base), string(seed), "PRAGMA user_version = " + itoaInt(factorydb.SchemaVersion)} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	e.env = map[string]string{"FACTORY_HOME": e.home, "HOME": e.pw, "FACTORY_PROC_DIR": t.TempDir()}
	return e
}

func itoaInt(n int) string { return strconv.Itoa(n) }

func (e *cliEnv) db() *sql.DB {
	e.t.Helper()
	d, err := openRaw(filepath.Join(e.home, "db", "factory.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { d.Close() })
	return d
}

func (e *cliEnv) sql(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db().Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *cliEnv) str(q string, args ...any) string {
	e.t.Helper()
	var s sql.NullString
	if err := e.db().QueryRow(q, args...).Scan(&s); err != nil && err != sql.ErrNoRows {
		e.t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

// run runs `factory args...` as agent and returns stdout, stderr and the exit code; e.execs counts hand-overs.
func (e *cliEnv) run(agent, stdin string, args ...string) (string, string, int) {
	e.t.Helper()
	env := map[string]string{}
	for k, v := range e.env {
		env[k] = v
	}
	env["FACTORY_AGENT"] = agent
	var out, errb bytes.Buffer
	e.execs = 0
	code := Run(Deps{Args: args, Argv0: filepath.Join(e.pw, "checkout", "bin", "factory"), Environ: os.Environ(),
		Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(stdin),
		Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		Getwd:    func() (string, error) { return e.cwd, nil },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec:     func(string, []string, []string) error { e.execs++; return nil },
		Env: identity.Env{Getenv: func(k string) string { return env[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			Uid:       os.Getuid(), PwDir: func(int) (string, error) { return e.pw, nil },
			Now: func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) }}})
	return out.String(), errb.String(), code
}

// TestDonePerms: done is an agent command. The owner has no agents row and is refused as Python does
// ("not a factory agent", exit 1); a worker finishes; a persistent role stays live.
func TestDonePerms(t *testing.T) {
	e := newCliEnv(t)
	for _, who := range []string{"owner", "nobody"} {
		out, errs, code := e.run(who, "", "done", "x")
		if code != 1 || out != "" || errs != "factory: not a factory agent\n" || e.execs != 0 {
			t.Errorf("%s: exit %d out %q err %q execs %d", who, code, out, errs, e.execs)
		}
	}
	out, errs, code := e.run("claude-gone", "", "done", "x")
	if code != 2 || errs != "factory: claude-gone is 'done', not live: nothing to finish (no second done/report)\n" {
		t.Errorf("not live: exit %d out %q err %q", code, out, errs)
	}
	out, errs, code = e.run("claude-pm", "", "done", "wrote the spec")
	if code != 0 || out != "done; summary sent to claude-lead. Your pane will be closed shortly — stop working now.\n" || errs != "" {
		t.Fatalf("worker: exit %d out %q err %q", code, out, errs)
	}
	if st := e.str("SELECT status FROM agents WHERE name='claude-pm'"); st != "done" {
		t.Errorf("agent status %q, want done", st)
	}
	if st := e.str("SELECT status FROM tasks WHERE id='T-0010'"); st != "done" {
		t.Errorf("task status %q, want done", st)
	}
	if s := e.str("SELECT subject||'|'||body||'|'||kind||'|'||recipient FROM messages WHERE sender='claude-pm'"); s != "claude-pm finished T-0010|wrote the spec|report|claude-lead" {
		t.Errorf("report row %q", s)
	}
	if d := e.str("SELECT detail FROM events WHERE action='done' AND entity='task'"); d != `{"by": "claude-pm"}` {
		t.Errorf("task done event detail %q", d)
	}
	if d := e.str("SELECT detail FROM events WHERE action='done' AND entity='agent'"); d != "wrote the spec" {
		t.Errorf("agent done event detail %q", d)
	}
	for _, who := range []string{"claude-boss", "claude-director"} {
		out, _, code := e.run(who, "", "done", "still here")
		if code != 0 || out != "summary sent to director\n" {
			t.Errorf("%s: exit %d out %q", who, code, out)
		}
		if st := e.str("SELECT status FROM agents WHERE name=?", who); st != "live" {
			t.Errorf("%s: persistent role status %q, want live", who, st)
		}
	}
}

func TestDoneNoLeadAndStdin(t *testing.T) {
	e := newCliEnv(t)
	out, _, code := e.run("claude-pm-nolead", "from stdin\r\nsecond\n", "done", "-")
	if code != 0 || !strings.HasPrefix(out, "done; summary sent to director.") {
		t.Fatalf("exit %d out %q", code, out)
	}
	if s := e.str("SELECT summary FROM agents WHERE name='claude-pm-nolead'"); s != "from stdin\nsecond" {
		t.Errorf("summary %q", s)
	}
	// cancelled tasks stay cancelled: the agent 'done' event is the record, plus done-ignored
	if st := e.str("SELECT status FROM tasks WHERE id='T-0013'"); st != "cancelled" {
		t.Errorf("task status %q", st)
	}
	if d := e.str("SELECT detail FROM events WHERE action='done-ignored'"); d != `{"by": "claude-pm-nolead", "status": "cancelled"}` {
		t.Errorf("done-ignored detail %q", d)
	}
}

func TestDoneQABlocked(t *testing.T) {
	e := newCliEnv(t)
	_, _, code := e.run("claude-qa-blocked", "", "done", "  BLOCKED: no worktree")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if st := e.str("SELECT status FROM tasks WHERE id='T-0011'"); st != "todo" {
		t.Errorf("task %q, want todo (reopened)", st)
	}
	if s := e.str("SELECT summary FROM agents WHERE name='claude-qa-blocked'"); !strings.Contains(s, "NOTE: T-0011 is NOT closed") {
		t.Errorf("summary %q lacks the NOTE", s)
	}
	if n := e.str("SELECT count(*) FROM events WHERE action='done-blocked-reopened'"); n != "1" {
		t.Errorf("done-blocked-reopened events %s", n)
	}
	// a QA task that already has a verdict is closed even when the summary says BLOCKED
	e.run("claude-qa-verdict", "", "done", "BLOCKED later")
	if st := e.str("SELECT status FROM tasks WHERE id='T-0014'"); st != "done" {
		t.Errorf("task with verdict %q, want done", st)
	}
}

func TestDoneUnreadOrders(t *testing.T) {
	e := newCliEnv(t)
	out, errs, code := e.run("claude-pm-orders", "", "done", "finished")
	want := "── M1 [request] from claude-lead @ 2026-11-12T00:10:00+00:00 goal=G-1 task=T-0012\n   do the thing\n   line one\n\n   line three\n"
	if code != 3 || out != want || errs != "factory: unread orders: read and act on them, then run factory done again\n" {
		t.Fatalf("exit %d\nout %q\nerr %q", code, out, errs)
	}
	if st := e.str("SELECT status FROM agents WHERE name='claude-pm-orders'"); st != "live" {
		t.Errorf("a refused done changed the agent: %q", st)
	}
	if r := e.str("SELECT read_at FROM messages WHERE id=1"); r == "" {
		t.Errorf("the shown order was not marked read")
	}
	if _, _, code := e.run("claude-pm-orders", "", "done", "finished"); code != 0 {
		t.Errorf("second done: exit %d", code)
	}
	e2 := newCliEnv(t)
	_, _, code = e2.run("claude-pm-orders", "", "done", "--force", "finished")
	if code != 0 {
		t.Fatalf("--force: exit %d", code)
	}
	if d := e2.str("SELECT detail FROM events WHERE action='done-forced'"); d != "unread decision/request messages: M1" {
		t.Errorf("done-forced detail %q", d)
	}
	if s := e2.str("SELECT subject FROM messages WHERE sender='claude-pm-orders' AND kind='report' AND body IS NULL"); s != "claude-pm-orders closed with --force; unread orders: M1" {
		t.Errorf("forced report subject %q", s)
	}
}

// TestDoneHandsOver: coder and judge keep the git/gh/CI gates in Python; nothing is read from stdin or written.
func TestDoneHandsOver(t *testing.T) {
	for _, who := range []string{"claude-coder", "claude-judge"} {
		e := newCliEnv(t)
		out, errs, code := e.run(who, "summary\n", "done", "-")
		if e.execs != 1 || out != "" || errs != "" || code != 0 {
			t.Errorf("%s: execs %d exit %d out %q err %q", who, e.execs, code, out, errs)
		}
		if st := e.str("SELECT status FROM agents WHERE name=?", who); st != "live" {
			t.Errorf("%s: row changed to %q before the hand-over", who, st)
		}
	}
}

func TestParseDone(t *testing.T) {
	good := map[string]doneCmd{"x": {summary: "x"}, "--force x": {summary: "x", force: true}, "x --force": {summary: "x", force: true}, "-": {summary: "-"}}
	for in, want := range good {
		got, ok := parseDone(strings.Fields(in))
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("parseDone(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "--force", "a b", "--force --force x", "-x", "--forc x", "--", "x --", "--force=1 x"} {
		if _, ok := parseDone(strings.Fields(in)); ok {
			t.Errorf("parseDone(%q) should hand over", in)
		}
	}
	if _, ok := parseDone([]string{"\xff"}); ok {
		t.Errorf("invalid UTF-8 should hand over")
	}
}

func FuzzParseDone(f *testing.F) {
	for _, s := range []string{"x", "--force", "-", "--", "-x", "é", "\xff"} {
		f.Add(s, "--force")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		parseDone([]string{a, b})
		parseDone([]string{a})
	})
}
