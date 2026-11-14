package stateload

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"factory/go/internal/citools/freshexport"
	_ "modernc.org/sqlite"
)

const schema = `BEGIN TRANSACTION;
CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT, entity TEXT, entity_id TEXT, action TEXT NOT NULL, detail TEXT);
CREATE TABLE goals (id TEXT PRIMARY KEY, title TEXT);
INSERT INTO "goals" VALUES('G-001','one');
INSERT INTO "goals" VALUES('G-002','two');
COMMIT;
`

const events = `{"id": 1, "ts": "2026-01-01T00:00:01", "actor": "a", "entity": "goal", "entity_id": "G-001", "action": "x", "detail": null}
{"id": 2, "ts": "2026-01-01T00:00:02", "actor": "a", "entity": "goal", "entity_id": "G-001", "action": "x", "detail": "d"}

{"id": 3, "ts": "2026-01-01T00:00:03", "actor": "a", "entity": "goal", "entity_id": "G-001", "action": "x", "detail": "d"}
`

type fixture struct{ state, home string }

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{filepath.Join(root, "state"), filepath.Join(root, "home")}
	for _, d := range []string{f.state, f.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	freshexport.HomeOverride = filepath.Join(root, "passwd-home") // never the live home
	t.Cleanup(func() { freshexport.HomeOverride = "" })
	write(t, filepath.Join(f.state, "factory.sql"), schema)
	write(t, filepath.Join(f.state, "events.jsonl"), events)
	return f
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGo(t *testing.T, home, state string) (int, string, string) {
	t.Helper()
	t.Setenv("FACTORY_HOME", home)
	var o, e bytes.Buffer
	rc := Main([]string{state}, &o, &e)
	return rc, o.String(), e.String()
}

func TestLoadSucceedsAndPrintsCounts(t *testing.T) {
	f := newFixture(t)
	rc, out, errText := runGo(t, f.home, f.state)
	if rc != 0 {
		t.Fatalf("rc %d stderr %s", rc, errText)
	}
	for _, want := range []string{"events\t3\n", "goals\t2\n", "loaded " + filepath.Join(f.home, "db", "factory.db")} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q: %q", want, out)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var detail sql.NullString
	if err := db.QueryRow("SELECT detail FROM events WHERE id = 1").Scan(&detail); err != nil || detail.Valid {
		t.Fatalf("event 1 detail should be NULL: %v %v", detail, err)
	}
	if _, err := os.Stat(filepath.Join(f.home, "db", "factory.db.tmp")); err == nil {
		t.Error("tmp db left behind")
	}
}

func TestRefusals(t *testing.T) {
	f := newFixture(t)
	t.Run("unset home", func(t *testing.T) {
		rc, _, e := runGo(t, "", f.state)
		if rc != 1 || !strings.Contains(e, "FACTORY_HOME is unset") {
			t.Fatalf("rc %d %q", rc, e)
		}
	})
	t.Run("live home", func(t *testing.T) {
		live := filepath.Join(freshexport.HomeOverride, "factory")
		rc, _, e := runGo(t, live, f.state)
		if rc != 1 || !strings.Contains(e, "live factory home") {
			t.Fatalf("rc %d %q", rc, e)
		}
		if _, err := os.Stat(live); err == nil {
			t.Fatal("the live home was created")
		}
	})
	t.Run("existing db", func(t *testing.T) {
		if rc, _, e := runGo(t, f.home, f.state); rc != 0 {
			t.Fatal(e)
		}
		rc, _, e := runGo(t, f.home, f.state)
		if rc != 1 || !strings.Contains(e, "already exists") {
			t.Fatalf("rc %d %q", rc, e)
		}
	})
	t.Run("usage", func(t *testing.T) {
		var o, e bytes.Buffer
		if rc := Main(nil, &o, &e); rc != 2 {
			t.Fatalf("rc %d", rc)
		}
	})
}

func TestBadInputWritesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		file, content, want string
	}{
		"bad sql":        {"factory.sql", "THIS IS NOT SQL;", "nothing was written"},
		"unknown column": {"events.jsonl", `{"id": 1, "ts": "t", "action": "x", "bogus": 1}` + "\n", "unknown column"},
		"not an object":  {"events.jsonl", "[1, 2]\n", "not a JSON object"},
		"nested value":   {"events.jsonl", `{"id": 1, "ts": "t", "action": "x", "detail": {"a": 1}}` + "\n", "cannot be stored"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			write(t, filepath.Join(f.state, tc.file), tc.content)
			rc, _, e := runGo(t, f.home, f.state)
			if rc != 1 || !strings.Contains(e, tc.want) {
				t.Fatalf("rc %d stderr %q, want %q", rc, e, tc.want)
			}
			if _, err := os.Stat(filepath.Join(f.home, "db", "factory.db")); err == nil {
				t.Error("a db was written")
			}
			if _, err := os.Stat(filepath.Join(f.home, "db", "factory.db.tmp")); err == nil {
				t.Error("tmp db left behind")
			}
		})
	}
}

func TestMissingStateFiles(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.state, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	rc, _, e := runGo(t, f.home, f.state)
	if rc != 1 || !strings.Contains(e, "not found") {
		t.Fatalf("rc %d %q", rc, e)
	}
}

// pyWrap runs a ci/*.py script with the passwd home faked, so the reference never reads the live home.
const pyWrap = `import sys, runpy, pwd
fake, script = sys.argv[1], sys.argv[2]
sys.argv = [script] + sys.argv[3:]
orig = pwd.getpwuid
def getpwuid(uid):
    e = orig(uid)
    return pwd.struct_passwd((e.pw_name, e.pw_passwd, e.pw_uid, e.pw_gid, e.pw_gecos, fake, e.pw_shell))
pwd.getpwuid = getpwuid
runpy.run_path(script, run_name="__main__")
`

func TestParityWithPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found: parity with ci/state_load.py not checked")
	}
	script, err := filepath.Abs("../../../../ci/state_load.py")
	if err != nil || !fileExists(script) {
		t.Skip("ci/state_load.py not found: parity not checked")
	}
	f := newFixture(t)
	pyHome := filepath.Join(filepath.Dir(f.home), "pyhome")
	cmd := exec.Command(py, "-I", "-c", pyWrap, freshexport.HomeOverride, script, f.state)
	cmd.Env = append(os.Environ(), "FACTORY_HOME="+pyHome)
	var po bytes.Buffer
	cmd.Stdout = &po
	if err := cmd.Run(); err != nil {
		t.Fatalf("python: %v", err)
	}
	rc, goOut, e := runGo(t, f.home, f.state)
	if rc != 0 {
		t.Fatal(e)
	}
	norm := func(s, home string) string { return strings.ReplaceAll(s, home, "<home>") }
	if norm(po.String(), pyHome) != norm(goOut, f.home) {
		t.Fatalf("output differs:\npython: %q\ngo:     %q", po.String(), goOut)
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// runPy runs ci/state_load.py with the faked passwd home and returns its exit code; it skips when python3 is absent.
func runPy(t *testing.T, home, state string) int {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found: parity with ci/state_load.py not checked")
	}
	script, err := filepath.Abs("../../../../ci/state_load.py")
	if err != nil || !fileExists(script) {
		t.Skip("ci/state_load.py not found: parity not checked")
	}
	cmd := exec.Command(py, "-I", "-c", pyWrap, freshexport.HomeOverride, script, state)
	cmd.Env = append(os.Environ(), "FACTORY_HOME="+home)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("python: %v", err)
	}
	return 0
}

// An empty event cannot be inserted by the Python tool (INSERT INTO events () is a syntax error): exit 1, nothing written.
func TestEmptyEventExitsLikePython(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.state, "events.jsonl"), events+"{}\n")
	rc, _, e := runGo(t, f.home, f.state)
	if rc != 1 || !strings.Contains(e, "empty event") {
		t.Fatalf("rc %d stderr %q, want 1 and an empty-event message", rc, e)
	}
	if fileExists(filepath.Join(f.home, "db", "factory.db")) || fileExists(filepath.Join(f.home, "db", "factory.db.tmp")) {
		t.Error("a db was written")
	}
	if prc := runPy(t, filepath.Join(filepath.Dir(f.home), "pyhome"), f.state); prc != rc {
		t.Fatalf("python exit %d, go %d", prc, rc)
	}
}

// The live_home guard must hold for every line ending Python's text mode and splitlines accept.
func TestLiveHomeLineEndings(t *testing.T) {
	for name, content := range map[string]string{
		"lf": "%s\n", "crlf": "%s\r\n", "cr": "%s\r", "no newline": "%s", "vt": "%s\v", "ff": "%s\f", "nel": "%s\u0085",
		"line separator": "%s ", "fs": "%s\x1c",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			live := filepath.Join(filepath.Dir(f.home), "elsewhere")
			cfg := filepath.Join(freshexport.HomeOverride, ".config", "factory")
			if err := os.MkdirAll(cfg, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(cfg, "live_home"), strings.Replace(content, "%s", live, 1))
			rc, _, e := runGo(t, live, f.state)
			if rc != 1 || !strings.Contains(e, "live factory home") {
				t.Fatalf("go: rc %d %q, want the live-home refusal", rc, e)
			}
			if prc := runPy(t, live, f.state); prc != rc {
				t.Fatalf("python exit %d, go %d", prc, rc)
			}
		})
	}
}

// FuzzLoad: arbitrary events.jsonl and schema never panic; a failed load leaves no db behind.
func FuzzLoad(f *testing.F) {
	f.Add(events, schema)
	f.Add("{}\n", schema)
	f.Add("[1]\r\n{\"id\":1,\"ts\":\"t\",\"action\":\"x\"}\r", schema)
	f.Add("{\"id\":1e999,\"ts\":\"t\",\"action\":\"x\"} x", "")
	f.Fuzz(func(t *testing.T, jsonl, sql string) {
		root := t.TempDir()
		state, home := filepath.Join(root, "state"), filepath.Join(root, "home")
		if err := os.MkdirAll(state, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(state, "factory.sql"), sql)
		write(t, filepath.Join(state, "events.jsonl"), jsonl)
		if _, err := Load(home, state); err != nil && fileExists(filepath.Join(home, "db", "factory.db")) {
			t.Fatalf("load failed (%v) but a db exists", err)
		}
	})
}
