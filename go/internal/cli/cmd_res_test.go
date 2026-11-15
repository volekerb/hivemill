package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/identity"
)

// `factory res` parity cases: testdata/res/<case>/ holds
//
//	seed.sql      rows applied on top of testdata/golden/base.sql
//	env           KEY=VALUE lines (the loadavg, /tmp statvfs and clock pins)
//	herdr.out     what the fake `herdr agent list` prints; absent: herdr exits 1 (unreachable)
//	usage.json    optional $HOME/.claude/usage-limits.json
//	delegate      optional marker: Go must hand the command to Python (nothing is printed or written)
//	stdout        what Python printed, the machine-dependent numbers masked (see maskRes)
//
// plus testdata/res/ps.out (the fake `ps`). Regenerate stdout from the repo's Python:
//
//	cd go && go test -count=1 -run TestResParity ./internal/cli/ -args -update-res
var updateRes = flag.Bool("update-res", false, "regenerate testdata/res/*/stdout from python3")

const resDir = "testdata/res"

// The host's own numbers are not pinned by any environment variable (memory and swap come from /proc/meminfo, the
// CPU count from the machine), so both sides print them and the comparison masks them.
var resMasks = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?m)^(load gate: load1 [0-9.]+ vs cap )[0-9.]+( \([0-9.]+ x )[0-9]+( CPUs\): )`), "${1}<cap>${2}<n>${3}"},
	{regexp.MustCompile(`(?m)^mem available [0-9]+ MB`), "mem available <n> MB"},
	{regexp.MustCompile(`(?m)swap used [0-9]+ MB · load ([0-9.]+)/[0-9]+$`), "swap used <n> MB · load ${1}/<n>"},
	{regexp.MustCompile(`(?m)^(tmp used .*%\)) on [^ ]+( \(gate closes)`), "${1}${2}"},
}

func maskRes(s string) string {
	for _, m := range resMasks {
		s = m.re.ReplaceAllString(s, m.with)
	}
	return s
}

type resCase struct {
	name, dir string
	env       []string
	delegate  bool
}

func resCases(t *testing.T) []resCase {
	t.Helper()
	ents, err := os.ReadDir(resDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []resCase
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		c := resCase{name: e.Name(), dir: filepath.Join(resDir, e.Name())}
		b, err := os.ReadFile(filepath.Join(c.dir, "env"))
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "" {
				c.env = append(c.env, l)
			}
		}
		_, derr := os.Stat(filepath.Join(c.dir, "delegate"))
		c.delegate = derr == nil
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// resHome builds the case's home (database, usage file) and its stub bin dir (herdr, ps).
func resHome(t *testing.T, c resCase) (home, binDir string) {
	t.Helper()
	home, binDir = t.TempDir(), t.TempDir()
	base, err := os.ReadFile(baseSQLPath())
	if err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile(filepath.Join(c.dir, "seed.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := openRaw(filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"PRAGMA journal_mode=WAL", string(base), string(seed), fmt.Sprintf("PRAGMA user_version = %d", factorydb.SchemaVersion)} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("seeding %s: %v", c.name, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if u, err := os.ReadFile(filepath.Join(c.dir, "usage.json")); err == nil {
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".claude", "usage-limits.json"), u, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	herdr := "#!/bin/sh\necho 'herdr: server not running' >&2\nexit 1\n"
	if out, err := filepath.Abs(filepath.Join(c.dir, "herdr.out")); err == nil {
		if _, serr := os.Stat(out); serr == nil {
			herdr = "#!/bin/sh\ncat '" + out + "'\n"
		}
	}
	ps, err := filepath.Abs(filepath.Join(resDir, "ps.out"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"herdr": herdr, "ps": "#!/bin/sh\ncat '" + ps + "'\n"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(c.dir, "hc")); err == nil { // marker: an `hc` binary is on PATH
		if err := os.WriteFile(filepath.Join(binDir, "hc"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home, binDir
}

type resRun struct {
	stdout, stderr string
	code, execs    int
	fingerprint    string
}

func runResGo(t *testing.T, c resCase) resRun {
	t.Helper()
	home, binDir := resHome(t, c)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH")) // the herdr client and ps run from the process PATH
	procDir := t.TempDir()
	env := goldenEnv(home, procDir, binDir, c.env)
	m := envMap(env)
	before, err := goldenFingerprint(filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	var r resRun
	argv0, err := filepath.Abs(filepath.Join("..", "..", "..", "bin", "factory"))
	if err != nil {
		t.Fatal(err)
	}
	r.code = Run(Deps{
		Args: []string{"res"}, Argv0: argv0, Environ: append(append([]string{}, env...), "FACTORY_CLI_IMPL=go"),
		Env: identity.Env{
			Getenv:    func(k string) string { return m[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := m[k]; return v, ok },
			Uid:       os.Getuid(),
			PwDir:     func(int) (string, error) { return home, nil },
			Now:       func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) },
		},
		Stdout: &out, Stderr: &errb,
		Getpid: func() int { return 4242 }, Getppid: func() int { return 4241 },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec:     func(string, []string, []string) error { r.execs++; return nil },
	})
	r.stdout, r.stderr = out.String(), errb.String()
	after, err := goldenFingerprint(filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	r.fingerprint = before + " -> " + after
	if before != after {
		t.Errorf("%s: res changed the database: %s", c.name, r.fingerprint)
	}
	return r
}

func TestResParity(t *testing.T) {
	cases := resCases(t)
	if *updateRes {
		checkout, procDir := pythonCheckout(t)
		for _, c := range cases {
			if c.delegate {
				continue
			}
			home, binDir := resHome(t, c)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			run, err := runPython(checkout, home, procDir, []string{"res"}, c.env, sandbox{binDir: binDir}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if run.code != 0 {
				t.Fatalf("%s: python res exit %d: %s", c.name, run.code, run.stderr)
			}
			if err := os.WriteFile(filepath.Join(c.dir, "stdout"), []byte(maskRes(run.stdout)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runResGo(t, c)
			if c.delegate {
				if r.execs != 1 || r.stdout != "" {
					t.Fatalf("want a hand-over to Python, got execs=%d stdout=%q stderr=%q", r.execs, r.stdout, r.stderr)
				}
				return
			}
			if r.code != 0 || r.execs != 0 {
				t.Fatalf("exit %d execs %d stderr %q", r.code, r.execs, r.stderr)
			}
			want, err := os.ReadFile(filepath.Join(c.dir, "stdout"))
			if err != nil {
				t.Fatalf("%v; create it with: cd go && go test -count=1 -run TestResParity ./internal/cli/ -args -update-res", err)
			}
			if got := maskRes(r.stdout); got != string(want) {
				t.Fatalf("output differs from Python\n--- got\n%s--- want (Python)\n%s", got, want)
			}
		})
	}
}

// TestResNoWrites: res leaves the database untouched on every case, including the one it hands to Python.
func TestResNoWrites(t *testing.T) {
	for _, c := range resCases(t) {
		t.Run(c.name, func(t *testing.T) {
			r := runResGo(t, c) // runResGo fails the test when the fingerprint (mtime, size, data_version, events) moves
			if r.fingerprint == "" {
				t.Fatal("no fingerprint")
			}
		})
	}
}

// TestResGateOutcomes pins the first line through the real entry point, in the text Python prints.
func TestResGateOutcomes(t *testing.T) {
	cases := map[string]string{
		"cap":        "spawn gate: CLOSED — 2/2 agent panes live\n",
		"herdr_down": "spawn gate: CLOSED — herdr unreachable\n",
		"weekly":     "spawn gate: CLOSED — weekly usage 99% ≥ 92%\n",
		"empty":      "spawn gate: OPEN\n",
		"tmpfull":    "spawn gate: CLOSED — /tmp 905/953 MB used (95.0%) ≥ 90% — clean finished tasks' scratchpads\n",
		"busy":       "spawn gate: OPEN\n",
	}
	for _, c := range resCases(t) {
		want, ok := cases[c.name]
		if !ok || c.delegate {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			r := runResGo(t, c)
			if !strings.HasPrefix(r.stdout, strings.TrimSuffix(want, "\n")) {
				t.Fatalf("first line = %q, want prefix %q", strings.SplitN(r.stdout, "\n", 2)[0], want)
			}
		})
	}
}

func TestResRejectsArguments(t *testing.T) {
	if _, ok := parseRes([]string{"--json"}); ok {
		t.Fatal("res takes no arguments: an argument must go to Python")
	}
	if _, ok := parseRes(nil); !ok {
		t.Fatal("bare res must run in Go")
	}
}

func FuzzResParse(f *testing.F) {
	f.Add("", "--help")
	f.Fuzz(func(t *testing.T, a, b string) {
		parseRes([]string{a, b})
		resDeps(a)
	})
}
