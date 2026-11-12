package cli

import (
	"bytes"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/identity"
)

// Golden parity harness (G-041 S2a). A case is a directory testdata/golden/<group>/<case>/ with
//
//	args      one argv element per line (an empty file = no arguments)
//	env       optional KEY=VALUE lines added to the clean base environment
//	stdout, stderr, code   what Python printed and returned
//	delegate  optional empty file: Go must hand the command to Python (only the exec argv/env is checked)
//
// Write-mode extension (S2 rules W1-W3, W6), all optional:
//
//	stdin     bytes fed to stdin on both sides (absent: empty)
//	cwd/      copied to a temp dir that is the process cwd on both sides (for @path args)
//	bin/      shell stubs prepended to PATH on both sides; each appends its NUL-joined argv line to $HOME/stub-calls.log
//	writes    empty marker: the case may write; db.diff is then the expected DB change (a delegate case never writes in Go)
//	db.diff   the +/- line diff of the canonical DB dump before and after (see dbdump_test.go)
//	calls     the expected stub-calls.log (absent: no stub may be called)
//	gonly     empty marker: a test-only handler with no Python behind it; update mode leaves the expectations alone
//
// and a group has seed.sql, applied on top of testdata/golden/base.sql (the schema and rows `factory init` makes).
// Normal mode runs Go on that database and compares byte for byte, then checks nothing was written. Update mode
// regenerates base.sql and every expectation from the repo's bin/ running under python3:
//
//	cd go && go test -count=1 -run TestGolden ./internal/cli/ -args -update-golden
var updateGolden = flag.Bool("update-golden", false, "regenerate testdata/golden from python3 (bin/ copied to a temp checkout)")

const goldenDir = "testdata/golden"

type goldenCase struct {
	group, name, dir string
	args, env        []string
	delegate         bool
	writes, gonly    bool
	stdin            []byte
}

// sink is the part of testing.T the case runner needs, so a meta-test can watch it fail.
type sink interface {
	Helper()
	TempDir() string
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"), nil
}

func writeLines(path string, lines []string) error {
	s := strings.Join(lines, "\n")
	if len(lines) > 0 {
		s += "\n"
	}
	return os.WriteFile(path, []byte(s), 0o644)
}

func loadCase(group, dir string) (goldenCase, error) {
	c := goldenCase{group: group, name: filepath.Base(dir), dir: dir}
	if _, err := os.Stat(filepath.Join(dir, "args")); err != nil {
		return c, fmt.Errorf("golden case %s has no args file", dir)
	}
	var err error
	if c.args, err = readLines(filepath.Join(dir, "args")); err != nil {
		return c, err
	}
	if c.env, err = readLines(filepath.Join(dir, "env")); err != nil {
		return c, err
	}
	has := func(f string) bool { _, e := os.Stat(filepath.Join(dir, f)); return e == nil }
	c.delegate, c.writes, c.gonly = has("delegate"), has("writes"), has("gonly")
	if b, err := os.ReadFile(filepath.Join(dir, "stdin")); err == nil {
		c.stdin = b
	} else if !errors.Is(err, fs.ErrNotExist) {
		return c, err
	}
	return c, nil
}

func goldenCases(t *testing.T) []goldenCase { return goldenCasesIn(t, goldenDir) }

// goldenCasesIn loads the cases under root (goldenDir, or a temp copy of it).
func goldenCasesIn(t *testing.T, root string) []goldenCase {
	t.Helper()
	groups, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []goldenCase
	for _, g := range groups {
		if !g.IsDir() {
			continue
		}
		cases, err := os.ReadDir(filepath.Join(root, g.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			if !c.IsDir() {
				continue
			}
			gc, err := loadCase(g.Name(), filepath.Join(root, g.Name(), c.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, gc)
		}
	}
	return out
}

// goldenNow is the fixed clock of both implementations (S2 rule W1); it equals the Go Now below, TZ=UTC.
const goldenNow = "2026-11-12T01:02:03+00:00"

// goldenEnv is the clean environment of both implementations: nothing leaks in from the coder's own shell, and
// FACTORY_PROC_DIR points at an empty directory so the real process ancestry never looks like a worker.
// binDir (may be empty) is the case's stub directory, put first on PATH.
func goldenEnv(home, procDir, binDir string, extra []string) []string {
	path := os.Getenv("PATH")
	if binDir != "" {
		path = binDir + string(os.PathListSeparator) + path
	}
	env := []string{
		"PATH=" + path, "HOME=" + home, "FACTORY_HOME=" + home, "FACTORY_PROC_DIR=" + procDir,
		"LANG=C.UTF-8", "TZ=UTC", "FACTORY_TEST_NOW=" + goldenNow,
	}
	return append(env, extra...)
}

// sandbox is the per-run copy of a case's cwd/ and bin/ (either may be empty).
type sandbox struct{ cwd, binDir string }

func caseSandbox(t sink, c goldenCase) (sandbox, error) {
	t.Helper()
	var sb sandbox
	for _, d := range []struct {
		name string
		dst  *string
	}{{"cwd", &sb.cwd}, {"bin", &sb.binDir}} {
		src := filepath.Join(c.dir, d.name)
		if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
			continue
		}
		*d.dst = filepath.Join(t.TempDir(), d.name)
		if err := os.MkdirAll(*d.dst, 0o755); err != nil {
			return sb, err
		}
		if err := copyTree(src, *d.dst); err != nil {
			return sb, err
		}
	}
	return sb, nil
}

// stubCalls is $HOME/stub-calls.log ("" when no stub ran).
func stubCalls(home string) (string, error) {
	b, err := os.ReadFile(filepath.Join(home, "stub-calls.log"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func baseSQLPath() string { return filepath.Join(goldenDir, "base.sql") }

// buildGoldenDB creates <home>/db/factory.db from base.sql plus the group's seed.sql.
func buildGoldenDB(t sink, home, group string) (string, error) {
	return buildGoldenDBIn(t, goldenDir, home, group)
}

// buildGoldenDBIn is buildGoldenDB reading base.sql and seed.sql from root.
func buildGoldenDBIn(t sink, root, home, group string) (string, error) {
	t.Helper()
	base, err := os.ReadFile(filepath.Join(root, "base.sql"))
	if err != nil {
		return "", fmt.Errorf("%w; create it with: cd go && go test -count=1 -run TestGolden ./internal/cli/ -args -update-golden", err)
	}
	seed, err := os.ReadFile(filepath.Join(root, group, "seed.sql"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	dir := filepath.Join(home, "db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "factory.db")
	d, err := openRaw(path)
	if err != nil {
		return "", err
	}
	defer d.Close()
	for _, q := range []string{"PRAGMA journal_mode=WAL", string(base), string(seed),
		fmt.Sprintf("PRAGMA user_version = %d", factorydb.SchemaVersion)} {
		if _, err := d.Exec(q); err != nil {
			return "", fmt.Errorf("building %s: %w", path, err)
		}
	}
	return path, nil
}

func openRaw(path string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	return d, nil
}

// goldenFingerprint is dbFingerprint plus the row count of every table: a write that stays in the WAL leaves the
// main file's mtime and size alone, but never the row counts of an INSERT or DELETE.
func goldenFingerprint(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	db, err := factorydb.Open(path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	dv, err := db.DataVersion()
	if err != nil {
		return "", err
	}
	rs, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return "", err
	}
	var names []string
	for rs.Next() {
		var n string
		if err := rs.Scan(&n); err != nil {
			rs.Close()
			return "", err
		}
		names = append(names, n)
	}
	rs.Close()
	parts := []string{fi.ModTime().UTC().Format(time.RFC3339Nano), strconv.FormatInt(fi.Size(), 10), strconv.FormatInt(dv, 10)}
	for _, n := range names {
		var cnt int64
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&cnt); err != nil {
			return "", err
		}
		parts = append(parts, n+"="+strconv.FormatInt(cnt, 10))
	}
	return strings.Join(parts, "|"), nil
}

type goOutcome struct {
	stdout, stderr string
	code           int
	exec           *execCall
	execs          int
	dbDiff, calls  string
	writes         bool
}

// runGo runs the case through cli.Run on a fresh database and reports whether the database changed.
func runGo(t sink, c goldenCase) (goOutcome, error) {
	t.Helper()
	var o goOutcome
	home, procDir := t.TempDir(), t.TempDir()
	path, err := buildGoldenDB(t, home, c.group)
	if err != nil {
		return o, err
	}
	before, err := goldenFingerprint(path)
	if err != nil {
		return o, err
	}
	dumpBefore, err := dumpDB(path)
	if err != nil {
		return o, err
	}
	sb, err := caseSandbox(t, c)
	if err != nil {
		return o, err
	}
	env := goldenEnv(home, procDir, sb.binDir, c.env)
	m := envMap(env)
	var out, errb bytes.Buffer
	argv0, err := filepath.Abs(filepath.Join("..", "..", "..", "bin", "factory"))
	if err != nil {
		return o, err
	}
	o.code = Run(Deps{
		Args: c.args, Argv0: argv0, Environ: append(append([]string{}, env...), "FACTORY_CLI_IMPL=go"),
		Env: identity.Env{
			Getenv:    func(k string) string { return m[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := m[k]; return v, ok },
			Uid:       os.Getuid(),
			PwDir:     func(int) (string, error) { return home, nil },
			Now:       func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) },
		},
		Stdout: &out, Stderr: &errb, Stdin: bytes.NewReader(c.stdin),
		Getwd: func() (string, error) {
			if sb.cwd != "" {
				return sb.cwd, nil
			}
			return home, nil
		},
		Getpid: func() int { return 4242 }, Getppid: func() int { return 4241 },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec: func(p string, a, e []string) error {
			o.execs++
			o.exec = &execCall{p, a, e}
			return nil
		},
	})
	o.stdout, o.stderr = out.String(), errb.String()
	after, err := goldenFingerprint(path)
	if err != nil {
		return o, err
	}
	o.writes = c.writes && !c.delegate
	if !o.writes {
		if after != before {
			return o, fmt.Errorf("%s %v changed the database: %s -> %s", c.name, c.args, before, after)
		}
	} else {
		dumpAfter, err := dumpDB(path)
		if err != nil {
			return o, err
		}
		o.dbDiff = diffDump(dumpBefore, dumpAfter)
	}
	if o.calls, err = stubCalls(home); err != nil {
		return o, err
	}
	return o, nil
}

func checkGoldenCase(t sink, c goldenCase) error {
	t.Helper()
	o, err := runGo(t, c)
	if err != nil {
		return err
	}
	if c.delegate {
		if o.execs != 1 {
			return fmt.Errorf("%s: want a hand-over to python (1 exec), got %d execs; stdout %q stderr %q", c.name, o.execs, o.stdout, o.stderr)
		}
		wantArgv := append([]string{"python3", ""}, c.args...)
		got := o.exec.argv
		if len(got) != len(wantArgv) || got[0] != "python3" || filepath.Base(got[1]) != "factory" ||
			strings.Join(got[2:], "\x00") != strings.Join(wantArgv[2:], "\x00") {
			return fmt.Errorf("%s: exec argv %q, want python3 <bin>/factory %q", c.name, got, c.args)
		}
		impl := 0
		for _, kv := range o.exec.env {
			if kv == "FACTORY_CLI_IMPL=python" {
				impl++
			} else if strings.HasPrefix(kv, "FACTORY_CLI_IMPL=") {
				return fmt.Errorf("%s: exec env still has %s", c.name, kv)
			}
		}
		if impl != 1 {
			return fmt.Errorf("%s: exec env must set FACTORY_CLI_IMPL=python once, got %d", c.name, impl)
		}
		return nil
	}
	if o.execs != 0 {
		return fmt.Errorf("%s: handed over to python (%q), want Go to answer; create a `delegate` file if that is intended", c.name, o.exec.argv)
	}
	files := []struct{ file, got string }{{"stdout", o.stdout}, {"stderr", o.stderr}, {"code", strconv.Itoa(o.code) + "\n"}}
	if o.writes {
		files = append(files, struct{ file, got string }{"db.diff", o.dbDiff})
	}
	for _, f := range files {
		want, err := os.ReadFile(filepath.Join(c.dir, f.file))
		if err != nil {
			return fmt.Errorf("%s: %w; regenerate with -update-golden", c.name, err)
		}
		if f.got != string(want) {
			if f.file == "db.diff" {
				return fmt.Errorf("%s %v: db.diff differs from the Python golden (tables: %s):\n got:\n%swant:\n%s",
					c.name, c.args, strings.Join(diffTables(f.got, string(want)), ", "), f.got, string(want))
			}
			return fmt.Errorf("%s %v: %s differs from the Python golden:\n got %q\nwant %q", c.name, c.args, f.file, f.got, string(want))
		}
	}
	wantCalls, err := os.ReadFile(filepath.Join(c.dir, "calls"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if o.calls != string(wantCalls) {
		return fmt.Errorf("%s %v: stub calls differ from the Python golden (calls):\n got %q\nwant %q", c.name, c.args, o.calls, string(wantCalls))
	}
	return nil
}

// ---- update mode: python3 on a copy of bin/ ----

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		fi, err := in.Stat()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(dst, rel), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

type pyRun struct {
	stdout, stderr string
	code           int
}

// runPython runs the checkout's CLI; sb and stdin are the case's (zero values for a bare command).
func runPython(checkout, home, procDir string, args, extra []string, sb sandbox, stdin []byte) (pyRun, error) {
	cmd := exec.Command("python3", append([]string{filepath.Join(checkout, "bin", "factory")}, args...)...)
	cmd.Dir = home
	if sb.cwd != "" {
		cmd.Dir = sb.cwd
	}
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(goldenEnv(home, procDir, sb.binDir, extra), "FACTORY_CLI_IMPL=python")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := pyRun{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		return r, fmt.Errorf("python3 %v: %w; is python3 on PATH?", args, err)
	}
	return r, nil
}

// pythonBaseSQL runs `factory init` from the checkout in a fresh home and dumps the result as SQL: the DDL of
// every table, then every row (INSERT per row, rowid order), then the other objects (indexes, triggers, views) so
// loading the rows cannot fire a trigger.
func pythonBaseSQL(checkout, home, procDir string) (string, error) {
	r, err := runPython(checkout, home, procDir, []string{"init"}, nil, sandbox{}, nil)
	if err != nil {
		return "", err
	}
	if r.code != 0 {
		return "", fmt.Errorf("factory init exited %d: %s", r.code, r.stderr)
	}
	d, err := openRaw(filepath.Join(home, "db", "factory.db"))
	if err != nil {
		return "", err
	}
	defer d.Close()
	type obj struct{ typ, name, sql string }
	rs, err := d.Query("SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid")
	if err != nil {
		return "", err
	}
	var objs []obj
	for rs.Next() {
		var o obj
		if err := rs.Scan(&o.typ, &o.name, &o.sql); err != nil {
			rs.Close()
			return "", err
		}
		objs = append(objs, o)
	}
	rs.Close()
	var b strings.Builder
	b.WriteString("-- Generated by: cd go && go test -count=1 -run TestGolden ./internal/cli/ -args -update-golden\n")
	b.WriteString("-- The schema and rows of `python3 bin/factory init`. Do not edit.\n")
	for _, o := range objs {
		if o.typ == "table" {
			b.WriteString(o.sql + ";\n")
		}
	}
	for _, o := range objs {
		if o.typ != "table" {
			continue
		}
		cr, err := d.Query(fmt.Sprintf(`SELECT name FROM pragma_table_info('%s') ORDER BY cid`, strings.ReplaceAll(o.name, "'", "''")))
		if err != nil {
			return "", err
		}
		var cols, quoted []string
		for cr.Next() {
			var n string
			if err := cr.Scan(&n); err != nil {
				cr.Close()
				return "", err
			}
			cols = append(cols, `"`+n+`"`)
			quoted = append(quoted, `quote("`+n+`")`)
		}
		cr.Close()
		q := fmt.Sprintf(`SELECT 'INSERT INTO "%s"(%s) VALUES(' || %s || ');' FROM "%s" ORDER BY rowid`,
			o.name, strings.Join(cols, ","), strings.Join(quoted, ` || ',' || `), o.name)
		ir, err := d.Query(q)
		if err != nil {
			return "", fmt.Errorf("dumping %s: %w", o.name, err)
		}
		for ir.Next() {
			var line string
			if err := ir.Scan(&line); err != nil {
				ir.Close()
				return "", err
			}
			b.WriteString(line + "\n")
		}
		ir.Close()
	}
	for _, o := range objs {
		if o.typ != "table" {
			b.WriteString(o.sql + ";\n")
		}
	}
	return b.String(), nil
}

// pythonTree is what the Python CLI needs next to bin/. roles/ is deliberately absent: without it `init` cannot
// render settings into the checkout.
var pythonTree = []string{"bin", "web", "scout", "retro"}

// pythonCheckout copies pythonTree into a temp checkout.
func pythonCheckout(t *testing.T) (checkout, procDir string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH: cannot regenerate or compare the Python goldens")
	}
	root := t.TempDir()
	checkout, procDir = filepath.Join(root, "checkout"), filepath.Join(root, "proc")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range pythonTree { // the Python CLI imports web/, scout/ and retro/ from the repo root
		if err := copyTree(repoFile(t, d), filepath.Join(checkout, d)); err != nil {
			t.Fatal(err)
		}
	}
	return checkout, procDir
}

func generatedBase(t *testing.T, checkout, procDir string) string {
	t.Helper()
	home := t.TempDir()
	s, err := pythonBaseSQL(checkout, home, procDir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// regenPyClock matches the wall-clock timestamps Python stamps into rows (its now() ignores FACTORY_TEST_NOW);
// update mode rewrites them to goldenNow so regeneration is reproducible. TZ=UTC makes the offset +00:00.
var regenPyClock = regexp.MustCompile(`"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\+00:00"`)

// updateCase regenerates the expectations of c in place (c.dir); base.sql and seeds are read from root.
func updateCase(t *testing.T, root, checkout, procDir string, c goldenCase) {
	t.Helper()
	if c.gonly || (c.delegate && !c.writes) {
		return
	}
	home := t.TempDir()
	path, err := buildGoldenDBIn(t, root, home, c.group)
	if err != nil {
		t.Fatal(err)
	}
	before, err := dumpDB(path)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := caseSandbox(t, c)
	if err != nil {
		t.Fatal(err)
	}
	r, err := runPython(checkout, home, procDir, c.args, c.env, sb, c.stdin)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{"stdout": r.stdout, "stderr": r.stderr, "code": strconv.Itoa(r.code) + "\n"}
	if c.writes {
		after, err := dumpDB(path)
		if err != nil {
			t.Fatal(err)
		}
		out["db.diff"] = regenPyClock.ReplaceAllString(diffDump(before, after), `"`+goldenNow+`"`)
	}
	for f, s := range out {
		if err := os.WriteFile(filepath.Join(c.dir, f), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	calls, err := stubCalls(home)
	if err != nil {
		t.Fatal(err)
	}
	if calls == "" {
		if err := os.Remove(filepath.Join(c.dir, "calls")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(filepath.Join(c.dir, "calls"), []byte(calls), 0o644); err != nil {
		t.Fatal(err)
	}
}

// regenGolden is update mode against root: python3 on a copy of bin/ rewrites root/base.sql and every case.
func regenGolden(t *testing.T, root string) {
	t.Helper()
	checkout, procDir := pythonCheckout(t)
	if err := os.WriteFile(filepath.Join(root, "base.sql"), []byte(generatedBase(t, checkout, procDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range goldenCasesIn(t, root) {
		updateCase(t, root, checkout, procDir, c)
	}
}

func TestGolden(t *testing.T) {
	if *updateGolden {
		regenGolden(t, goldenDir)
	}
	cases := goldenCases(t)
	if len(cases) == 0 {
		t.Fatal("no golden cases found under " + goldenDir)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].dir < cases[j].dir })
	for _, c := range cases {
		t.Run(c.group+"/"+c.name, func(t *testing.T) {
			if err := checkGoldenCase(t, c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGoldenBaseMatchesPython(t *testing.T) {
	checkout, procDir := pythonCheckout(t) // skips without python3
	want, err := os.ReadFile(baseSQLPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := generatedBase(t, checkout, procDir); got != string(want) {
		t.Fatalf("%s is out of date with bin/factory init; regenerate: cd go && go test -count=1 -run TestGolden ./internal/cli/ -args -update-golden", baseSQLPath())
	}
}

// TestGoldenDetectsWrites: a command that writes must make the runner fail, or "read-only" goldens prove nothing.
func TestGoldenDetectsWrites(t *testing.T) {
	withHandler(t, "zz-write", "", handler{parse: okParse, run: func(c *ctx, _ any) (int, error) {
		_, err := c.db.Exec("INSERT INTO events(ts,action) VALUES('2026-01-01T00:00:00Z','test-write')")
		return 0, err
	}})
	dir := filepath.Join(t.TempDir(), "writer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeLines(filepath.Join(dir, "args"), []string{"zz-write"}); err != nil {
		t.Fatal(err)
	}
	if err := writeLines(filepath.Join(dir, "env"), []string{"FACTORY_AGENT=owner"}); err != nil {
		t.Fatal(err)
	}
	c, err := loadCase("s1", dir)
	if err != nil {
		t.Fatal(err)
	}
	err = checkGoldenCase(t, c)
	if err == nil || !strings.Contains(err.Error(), "changed the database") {
		t.Fatalf("want a 'changed the database' failure, got %v", err)
	}
}

// ---- test-only write handlers (never in the binary) and the meta tests of the write extension ----

func init() {
	// zz-w0-insert writes one events row stamped with the run's clock; $1 (optional) is the detail.
	register("zz-w0-insert", "", handler{parse: okParse, run: func(c *ctx, p any) (int, error) {
		detail := "w0"
		if a := p.([]string); len(a) > 0 {
			detail = a[0]
		}
		if _, err := c.exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
			c.env.Now().Format("2006-01-02T15:04:05-07:00"), c.who, "w0", "W-1", "test-write", detail); err != nil {
			return 0, err
		}
		c.out.WriteString("inserted\n")
		return 0, nil
	}})
	// zz-w0-stdin stores the whole of stdin in an events row.
	register("zz-w0-stdin", "", handler{parse: okParse, run: func(c *ctx, _ any) (int, error) {
		b, err := io.ReadAll(c.deps.stdin())
		if err != nil {
			return 0, err
		}
		_, err = c.exec("INSERT INTO events(ts,action,detail) VALUES(?,?,?)", "2026-11-12T01:02:03+00:00", "stdin", string(b))
		return 0, err
	}})
	// zz-w0-stub runs the gh stub once, in the process cwd, with the harness environment.
	register("zz-w0-stub", "", handler{parse: okParse, run: func(c *ctx, p any) (int, error) {
		wd, err := c.deps.getwd()
		if err != nil {
			return 0, err
		}
		out, _, code, err := c.deps.runCmd("gh", p.([]string), wd, c.deps.Environ, 10*time.Second)
		if err != nil {
			return 0, err
		}
		c.out.WriteString(out)
		return code, nil
	}})
}

// mkCase writes a throwaway golden case (group w0) from name -> content; "bin/gh" is made executable.
func mkCase(t *testing.T, files map[string]string) goldenCase {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "case")
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	c, err := loadCase("w0", dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const ghStub = "#!/bin/sh\n{ printf '%s' \"${0##*/}\"; for a in \"$@\"; do printf '\\0%s' \"$a\"; done; printf '\\n'; } >> \"$HOME/stub-calls.log\"\necho stub-out\n"

// insertDiff is what zz-w0-insert writes into a fresh w0 database.
func insertDiff(t *testing.T, detail string) string {
	t.Helper()
	o, err := runGo(t, mkCase(t, map[string]string{"args": "zz-w0-insert\n" + detail + "\n", "env": "FACTORY_AGENT=owner\n", "writes": ""}))
	if err != nil {
		t.Fatal(err)
	}
	return o.dbDiff
}

func TestGoldenWriteDiff(t *testing.T) {
	base := map[string]string{"args": "zz-w0-insert\nhello\n", "env": "FACTORY_AGENT=owner\n", "writes": "",
		"stdout": "inserted\n", "stderr": "", "code": "0\n"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	good := insertDiff(t, "hello")
	if !strings.Contains(good, "+ events ") || !strings.Contains(good, `ts="2026-11-12T01:02:03+00:00"`) ||
		!strings.Contains(good, `detail="hello"`) {
		t.Fatalf("the insert diff lacks the fixed-clock events row:\n%s", good)
	}
	wantErr := func(t *testing.T, c goldenCase, parts ...string) {
		t.Helper()
		err := checkGoldenCase(t, c)
		if err == nil {
			t.Fatal("want a failure, got nil")
		}
		for _, p := range parts {
			if !strings.Contains(err.Error(), p) {
				t.Fatalf("error lacks %q: %v", p, err)
			}
		}
	}
	t.Run("matching insert passes", func(t *testing.T) {
		if err := checkGoldenCase(t, mkCase(t, with(map[string]string{"db.diff": good}))); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("detail differs", func(t *testing.T) {
		wantErr(t, mkCase(t, with(map[string]string{"db.diff": strings.Replace(good, `detail="hello"`, `detail="other"`, 1)})),
			"db.diff differs")
	})
	t.Run("extra events row", func(t *testing.T) {
		extra := good + "+ events 99 id=99 ts=\"x\" actor=NULL entity=NULL entity_id=NULL action=\"x\" detail=NULL\n"
		wantErr(t, mkCase(t, with(map[string]string{"db.diff": extra})), "db.diff differs", "tables: events")
	})
	t.Run("writes case with empty db.diff and a write", func(t *testing.T) {
		wantErr(t, mkCase(t, with(map[string]string{"db.diff": ""})), "db.diff differs")
	})
	t.Run("a case without writes keeps the no-write rule", func(t *testing.T) {
		c := mkCase(t, with(nil))
		c.writes = false
		wantErr(t, c, "changed the database")
	})
	t.Run("a writes case that writes nothing matches an empty db.diff", func(t *testing.T) {
		c := mkCase(t, map[string]string{"args": "whoami\n", "env": "FACTORY_AGENT=owner\n", "writes": "", "db.diff": "", "code": "0\n", "stderr": "",
			"stdout": "owner (owner — not a factory agent)\n"})
		if err := checkGoldenCase(t, c); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("stdin reaches the handler", func(t *testing.T) {
		c := mkCase(t, map[string]string{"args": "zz-w0-stdin\n", "env": "FACTORY_AGENT=owner\n", "writes": "", "stdin": "piped text\n",
			"stdout": "", "stderr": "", "code": "0\n"})
		o, err := runGo(t, c)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(o.dbDiff, `detail="piped text\n"`) {
			t.Fatalf("stdin not stored:\n%s", o.dbDiff)
		}
	})
	t.Run("stub calls: matching", func(t *testing.T) {
		c := mkCase(t, map[string]string{"args": "zz-w0-stub\npr\nview\n", "env": "FACTORY_AGENT=owner\n", "bin/gh": ghStub,
			"stdout": "stub-out\n", "stderr": "", "code": "0\n", "calls": "gh\x00pr\x00view\n"})
		if err := checkGoldenCase(t, c); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("stub calls: log differs", func(t *testing.T) {
		c := mkCase(t, map[string]string{"args": "zz-w0-stub\npr\nview\n", "env": "FACTORY_AGENT=owner\n", "bin/gh": ghStub,
			"stdout": "stub-out\n", "stderr": "", "code": "0\n", "calls": "gh\x00pr\x00list\n"})
		wantErr(t, c, "calls")
	})
	t.Run("stub called but no calls file", func(t *testing.T) {
		c := mkCase(t, map[string]string{"args": "zz-w0-stub\npr\nview\n", "env": "FACTORY_AGENT=owner\n", "bin/gh": ghStub,
			"stdout": "stub-out\n", "stderr": "", "code": "0\n"})
		wantErr(t, c, "calls")
	})
	t.Run("cwd is the process cwd", func(t *testing.T) {
		c := mkCase(t, map[string]string{"args": "zz-w0-stub\n", "env": "FACTORY_AGENT=owner\n", "cwd/f.txt": "x", "bin/gh": "#!/bin/sh\nls\n",
			"stdout": "f.txt\n", "stderr": "", "code": "0\n"})
		if err := checkGoldenCase(t, c); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTrace(t *testing.T) {
	var trace []Stmt
	h := handler{parse: okParse, run: func(c *ctx, _ any) (int, error) {
		for _, q := range []string{
			"INSERT INTO events(ts,action) VALUES('2026-11-12T01:02:03+00:00','a')",
			"UPDATE tasks SET status='x' WHERE id='none'",
			"  delete from events where id=-1",
			"INSERT OR IGNORE INTO config(key,value) VALUES('k','v')",
		} {
			if _, err := c.exec(q); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}}
	withHandler(t, "zz-trace", "", h)
	c := mkCase(t, map[string]string{"args": "zz-trace\n", "env": "FACTORY_AGENT=owner\n", "writes": "", "stdout": "", "stderr": "", "code": "0\n"})
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, c.group); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_AGENT": "owner", "FACTORY_PROC_DIR": t.TempDir()}
	var out, errb bytes.Buffer
	code := Run(Deps{Args: []string{"zz-trace"}, Environ: nil, Stdout: &out, Stderr: &errb, Trace: &trace, Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			PwDir: func(int) (string, error) { return home, nil }, Now: time.Now}})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := []Stmt{{"INSERT", "events"}, {"UPDATE", "tasks"}, {"DELETE", "events"}, {"INSERT", "config"}}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
	if got := parseStmt("BEGIN IMMEDIATE"); got != (Stmt{Verb: "BEGIN"}) {
		t.Fatalf("BEGIN parsed as %v", got)
	}
}
