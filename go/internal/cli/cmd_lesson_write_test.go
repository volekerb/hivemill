package cli

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"factory/go/internal/identity"
)

// lessonRig is a rig with the lesson, task and verdict tables and one lesson per state.
func lessonRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner", "FACTORY_PROC_DIR": t.TempDir()})
	for _, q := range []string{
		"CREATE TABLE IF NOT EXISTS lessons (id TEXT PRIMARY KEY, title TEXT NOT NULL, roles TEXT NOT NULL, what TEXT, rule TEXT NOT NULL, sources TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'open', recurrences INTEGER NOT NULL DEFAULT 0, author TEXT, created TEXT NOT NULL, updated TEXT NOT NULL, applied_at TEXT, verified_at TEXT, verified_by TEXT, evidence TEXT)",
		"CREATE TABLE IF NOT EXISTS lesson_links (id INTEGER PRIMARY KEY AUTOINCREMENT, lesson_id TEXT NOT NULL, task_id TEXT, pr_url TEXT, via TEXT, merged_at TEXT, author TEXT, created TEXT NOT NULL)",
		"CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, goal_id TEXT, title TEXT, role TEXT, status TEXT, pr_url TEXT, created TEXT, updated TEXT)",
		"CREATE TABLE IF NOT EXISTS verdicts (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, pr_url TEXT, head_sha TEXT, role TEXT NOT NULL, verdict TEXT NOT NULL, summary TEXT, author TEXT, created TEXT NOT NULL)",
		"INSERT INTO tasks(id,goal_id,title,role,status,pr_url,created,updated) VALUES('T-0001','G','t','coder','done','https://github.com/o/r/pull/1','x','x'),('T-0002','G','t','coder','todo',NULL,'x','x')",
		"INSERT INTO verdicts(task_id,pr_url,role,verdict,created) VALUES('T-0001','https://github.com/o/r/pull/1','judge','merge','2026-11-02T09:00:00+01:00')",
		"INSERT INTO lessons(id,title,roles,rule,status,created,updated) VALUES('L-001','one','coder','r','open','x','x'),('L-002','two','coder','r','open','x','x'),('L-003','three','coder','r','applied','x','x')",
		"INSERT INTO lesson_links(lesson_id,task_id,created) VALUES('L-002','T-0001','x'),('L-002','T-0002','x')",
	} {
		r.sql(q)
	}
	return r
}

// runTraced is r.run with the statement recorder on, and stdin fed.
func (r *rig) runTraced(stdin string, args ...string) (int, []Stmt) {
	r.out.Reset()
	r.errb.Reset()
	r.execs = nil
	var trace []Stmt
	var environ []string
	for k, v := range r.env {
		environ = append(environ, k+"="+v)
	}
	environ = append(environ, "FACTORY_CLI_IMPL=go")
	code := Run(Deps{
		Args: args, Argv0: r.repo + "/bin/factory", Environ: environ,
		Env: identity.Env{
			Getenv:    func(k string) string { return r.env[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := r.env[k]; return v, ok },
			Uid:       os.Getuid(),
			PwDir:     func(int) (string, error) { return r.pw, nil },
			Now:       func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) },
		},
		Stdout: &r.out, Stderr: &r.errb, Stdin: bytes.NewReader([]byte(stdin)), Trace: &trace,
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec: func(path string, argv, env []string) error {
			r.execs = append(r.execs, execCall{path, argv, env})
			return nil
		},
	})
	return code, trace
}

// TestStmtOrderLesson: the ordered (verb, table) writes of every argv form, transcribed from cmd_lesson in
// bin/factory_cli.py: add 17878-17898, edit 17899-17931, link 17932-17958, apply 17959-17973 with _link_unmerged
// 17744-17766, verify 17974-17986, reopen 17987-18002 (line numbers of this base).
func TestStmtOrderLesson(t *testing.T) {
	ins, upd := func(tb string) Stmt { return Stmt{"INSERT", tb} }, func(tb string) Stmt { return Stmt{"UPDATE", tb} }
	for _, tc := range []struct {
		name string
		args []string
		code int
		want []Stmt
	}{
		{"add", []string{"lesson", "add", "--title", "T", "--roles", "all", "--rule", "r"}, 0, []Stmt{ins("lessons"), ins("events")}},
		{"add refused before any write", []string{"lesson", "add", "--title", "T", "--roles", "nope", "--rule", "r"}, 2, nil},
		{"edit", []string{"lesson", "edit", "L-001", "--title", "new"}, 0, []Stmt{upd("lessons"), ins("events")}},
		{"edit nothing to change", []string{"lesson", "edit", "L-001", "--title", "one"}, 2, nil},
		{"link task", []string{"lesson", "link", "L-001", "--task", "T-0001"}, 0, []Stmt{ins("lesson_links"), ins("events")}},
		{"link twice refused", []string{"lesson", "link", "L-002", "--task", "T-0001"}, 2, nil},
		// apply: merged_at is stored for T-0001, then die on the unmerged T-0002 (no lessons update, no event).
		{"apply partial", []string{"lesson", "apply", "L-002"}, 2, []Stmt{upd("lesson_links")}},
		{"apply no links", []string{"lesson", "apply", "L-001"}, 2, nil},
		{"verify", []string{"lesson", "verify", "L-003", "--evidence", "e"}, 0, []Stmt{upd("lessons"), ins("events")}},
		{"reopen", []string{"lesson", "reopen", "L-003", "--reason", "again"}, 0, []Stmt{upd("lessons"), ins("events")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := lessonRig(t)
			code, trace := r.runTraced("", tc.args...)
			if len(r.execs) != 0 {
				t.Fatalf("handed over to python: %q", r.execs[0].argv)
			}
			if code != tc.code || !reflect.DeepEqual(trace, tc.want) {
				t.Fatalf("code %d trace %v, want %d %v (stderr %q)", code, trace, tc.code, tc.want, r.errb.String())
			}
		})
	}
	t.Run("apply ok", func(t *testing.T) {
		r := lessonRig(t)
		r.sql("DELETE FROM lesson_links WHERE task_id='T-0002'")
		code, trace := r.runTraced("", "lesson", "apply", "L-002")
		want := []Stmt{upd("lesson_links"), upd("lessons"), ins("events")}
		if code != 0 || !reflect.DeepEqual(trace, want) {
			t.Fatalf("code %d trace %v, want %v (stderr %q)", code, trace, want, r.errb.String())
		}
	})
}

func TestLessonWriteDelegates(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"link --pr", []string{"lesson", "link", "L-001", "--pr", "https://github.com/o/r/pull/1"}, nil},
		{"apply on a PR-only link", []string{"lesson", "apply", "L-002"}, map[string]string{"x": "y"}},
		{"worker role from env", []string{"lesson", "add", "--title", "T", "--roles", "all", "--rule", "r"}, map[string]string{"FACTORY_ROLE": "coder"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := lessonRig(t)
			if tc.name == "apply on a PR-only link" {
				r.sql("INSERT INTO lesson_links(lesson_id,task_id,pr_url,created) VALUES('L-002',NULL,'https://github.com/o/r/pull/9','x')")
				r.sql("DELETE FROM lesson_links WHERE task_id='T-0002'")
			}
			for k, v := range tc.env {
				r.env[k] = v
			}
			code, trace := r.runTraced("", tc.args...)
			if len(r.execs) != 1 || len(trace) != 0 || code != 0 {
				t.Fatalf("want one exec and no write, got %d execs, trace %v", len(r.execs), trace)
			}
		})
	}
}

func TestParseLessonWrite(t *testing.T) {
	for _, tc := range []struct {
		action string
		rest   []string
		ok     bool
	}{
		{"add", []string{"--title", "T", "--roles", "all", "--rule", "r"}, true},
		{"add", []string{"--title=T", "--rule=-"}, true},
		{"add", []string{"--title", "-"}, true},
		{"add", nil, false},                    // bare usage form
		{"add", []string{"--tit", "T"}, false}, // abbreviation
		{"add", []string{"--title", "T", "--title", "U"}, false},
		{"add", []string{"--title", "-x"}, false},
		{"add", []string{"--title"}, false},
		{"add", []string{"--", "x"}, false},
		{"add", []string{"-h"}, false},
		{"add", []string{"--title", "T", "L-001"}, false}, // positional after a flag
		{"add", []string{"--title", ""}, true},            // empty value is a value
		{"add", []string{"--title", "\xff"}, false},       // invalid UTF-8
		{"edit", []string{"L-001", "--title", "T"}, true},
		{"edit", []string{"", "--title", "T"}, false},
		{"edit", []string{"--title", "T", "L-001"}, false},
		{"link", []string{"L-001", "--task", "T-0001"}, true},
		{"link", []string{"L-001", "--pr", "u"}, false}, // gh
		{"apply", []string{"L-001"}, true},
		{"apply", []string{"L-001", "--status", "open"}, true}, // shared parser: accepted and ignored
		{"verify", []string{"L-001", "--evidence", "-"}, true},
		{"reopen", []string{"L-001", "--reason", "r", "--source=M1"}, true},
	} {
		if _, ok := lessonWriteParser(tc.action)(tc.rest); ok != tc.ok {
			t.Errorf("parse %s %q ok=%v, want %v", tc.action, tc.rest, ok, tc.ok)
		}
	}
}

func TestLessonSourceAndRoles(t *testing.T) {
	for s, ok := range map[string]bool{"M12": true, "T-1234": true, "T-123": false, "G-001": true, "#7": true,
		"https://github.com/o/r/pull/3": true, "https://github.com/o/r/issues/3": false, "x": false} {
		if lessonSourceRE.MatchString(s) != ok {
			t.Errorf("source %q: want %v", s, ok)
		}
	}
	got, err := lwRoles(strp(" coder, qa ,coder"))
	if err != nil || strings.Join(got, ",") != "coder,qa" {
		t.Fatalf("roles %v %v", got, err)
	}
}

func TestLessonViasMatchPython(t *testing.T) {
	src, err := os.ReadFile(repoFile(t, "bin/factory_cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `LESSON_VIAS = ("`+strings.Join(lessonVias, `", "`)+`")`) {
		t.Fatalf("lessonVias %v is not LESSON_VIAS of bin/factory_cli.py", lessonVias)
	}
	if !strings.Contains(string(src), `LESSON_SOURCE_RE = re.compile(r"^(M\d+|T-\d{4}|G-\d{3}|#\d+|https://github\.com/[^/\s]+/[^/\s]+/pull/\d+)$")`) {
		t.Fatal("LESSON_SOURCE_RE changed in bin/factory_cli.py; update lessonSourceRE")
	}
}

// TestLessonSourceUnicode: Python's re \d / \s are Unicode; Go must accept and reject the same strings.
func TestLessonSourceUnicode(t *testing.T) {
	for s, ok := range map[string]bool{
		"M١٢":                                  true, // Arabic-Indic digits
		"T-１２３４":                               true, // fullwidth digits
		"G-١٢٣":                                true,
		"#१":                                   true, // Devanagari
		"https://github.com/o/r/pull/١":        true,
		"https://github.com/o x/r/pull/1":      false, // NBSP is \s
		"https://github.com/o x/r/pull/1":      false, // LINE SEPARATOR is \s
		"https://github.com/o/r　x/pull/1":      false, // ideographic space
		"https://github.com/o\u0085x/r/pull/1": false, // NEL
		"M²":                                   false, // superscript two is not Nd
		"https://github.com/oé/r/pull/1":       true,  // non-space letters are fine
	} {
		if lessonSourceRE.MatchString(s) != ok {
			t.Errorf("source %q: want %v", s, ok)
		}
	}
}

// FuzzLessonWriteParser: no panic, and an accepted parse only ever holds argv forms Go handles (no --pr for link,
// no empty positional id, every flag a declared one, no flag value that looks like a flag).
func FuzzLessonWriteParser(f *testing.F) {
	f.Add("add", "--title\x00T\x00--roles\x00all")
	f.Add("link", "L-001\x00--pr\x00u")
	f.Add("edit", "L-001\x00--title=\xff")
	f.Add("apply", "\x00--note\x00-x")
	f.Add("verify", "L-1\x00--evidence\x00-")
	f.Fuzz(func(t *testing.T, action, joined string) {
		rest := strings.Split(joined, "\x00")
		v, ok := lessonWriteParser(action)(rest)
		if !ok {
			return
		}
		c := v.(lessonWriteCmd)
		for _, x := range rest {
			if !utf8.ValidString(x) {
				t.Fatalf("accepted invalid UTF-8 %q", x)
			}
		}
		if c.id != nil && *c.id == "" {
			t.Fatal("accepted an empty id")
		}
		if action == "link" && c.flags["--pr"] != nil {
			t.Fatal("accepted link --pr (gh)")
		}
		for n, p := range c.flags {
			if !lessonFlagNames[n] || p == nil {
				t.Fatalf("bad flag %q", n)
			}
			if *p != "-" && strings.HasPrefix(*p, "-") {
				t.Fatalf("accepted flag-like value %q for %s", *p, n)
			}
		}
	})
}
