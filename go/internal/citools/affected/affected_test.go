package affected

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const binSrc = "def cmd_other():\n    return 1\n\n\ndef cmd_judge():\n    return 1\n\n\ndef judge_pick():\n    return 1\n\n\ndef gate_x():\n    return 1\n\n\ndef hub():\n    return 1\n\n\nSCHEMA = 1\n"

func baseFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.write("bin/factory_cli.py", binSrc+"\n\ndef caller():\n"+strings.Repeat("    hub()\n", 3))
	f.write("tests/test_alpha.py", "import unittest\n\nclass A(unittest.TestCase):\n    def test_a(self):\n        pass\n")
	f.write("tests/test_db_layout.py", "")
	f.write("tests/test_inbox.py", "")
	f.write("tests/test_judge.py", "")
	f.write("tests/test_shard.py", "")
	f.write("tests/test_bin.py", "# cmd_other is tested; the word judge too\nx = 'cmd_other'\n")
	f.write("tests/test_web.py", "# web/app.py\n")
	f.write("tests/test_imp.py", "from web import (\n    other,\n    app,\n)\n")
	f.write("tests/test_foo.py", "")
	f.write("tests/test_roles.py", "# roles/ role.md\n")
	f.write("tests/test_docs.py", "# README.md\n")
	f.write("tests/test_names.py", "x = 'hubby'\ny = 'hub()'\n")
	for i := 0; i < 10; i++ {
		f.write(fmt.Sprintf("tests/test_pad%d.py", i), "")
	}
	f.write("web/app.py", "x = 1\n")
	f.write("web/other.py", "y = 1\n")
	f.write("ci/foo.py", "z = 1\n")
	f.write("ci/shard_timings.json", "{}\n")
	f.write("roles/role.md", "# role\n")
	f.write("docs/a.md", "# a\n")
	f.write("README.md", "hi\n")
	f.write("misc/zzz.txt", "z\n")
	f.commit("base")
	return f
}

type scenario struct {
	name   string
	mutate func(f *fixture)
	loose  bool // the Python and Go parse-error texts differ: compare up to "does not parse"
}

var scenarios = []scenario{
	{name: "test module edit", mutate: func(f *fixture) { f.write("tests/test_alpha.py", "import unittest\n# changed\n") }},
	{name: "docs only", mutate: func(f *fixture) { f.write("docs/a.md", "# a2\n") }},
	{name: "readme mentioned by a test", mutate: func(f *fixture) { f.write("README.md", "hello\n") }},
	{name: "bin def edit mentioned by test", mutate: func(f *fixture) {
		f.write("bin/factory_cli.py", strings.Replace(binSrc, "def cmd_other():\n    return 1", "def cmd_other():\n    return 2", 1)+"\n\ndef caller():\n"+strings.Repeat("    hub()\n", 3))
	}},
	{name: "bin cmd_judge edit", mutate: func(f *fixture) {
		f.write("bin/factory_cli.py", strings.Replace(binSrc, "def cmd_judge():\n    return 1", "def cmd_judge():\n    return 3", 1)+"\n\ndef caller():\n"+strings.Repeat("    hub()\n", 3))
	}},
	{name: "bin SCHEMA edit is FULL", mutate: func(f *fixture) {
		f.write("bin/factory_cli.py", strings.Replace(binSrc, "SCHEMA = 1", "SCHEMA = 2", 1)+"\n\ndef caller():\n"+strings.Repeat("    hub()\n", 3))
	}},
	{name: "bin new top-level statement is FULL", mutate: func(f *fixture) {
		f.write("bin/factory_cli.py", "import os\n"+binSrc+"\n\ndef caller():\n"+strings.Repeat("    hub()\n", 3))
	}},
	{name: "bin hub with many call sites is FULL", mutate: func(f *fixture) {
		f.write("bin/factory_cli.py", strings.Replace(binSrc, "def hub():\n    return 1", "def hub():\n    return 9", 1)+"\n\ndef caller():\n"+strings.Repeat("    hub()\n", 16))
	}},
	{name: "bin hub below the call-site cap", mutate: func(f *fixture) {
		f.write("bin/factory_cli.py", strings.Replace(binSrc, "def hub():\n    return 1", "def hub():\n    return 9", 1)+"\n\ndef caller():\n"+strings.Repeat("    hub()\n", 3))
	}},
	{name: "bin does not parse", loose: true, mutate: func(f *fixture) { f.write("bin/factory_cli.py", "x = 'abc\n") }},
	{name: "bin deleted", mutate: func(f *fixture) { f.remove("bin/factory_cli.py") }},
	{name: "ci script edit", mutate: func(f *fixture) { f.write("ci/foo.py", "z = 2\n") }},
	{name: "roles file edit", mutate: func(f *fixture) { f.write("roles/role.md", "# role2\n") }},
	{name: "python module mentioned and imported", mutate: func(f *fixture) { f.write("web/app.py", "x = 2\n") }},
	{name: "python module only imported by name list", mutate: func(f *fixture) { f.write("web/other.py", "y = 2\n") }},
	{name: "unmentioned file is FULL", mutate: func(f *fixture) { f.write("misc/zzz.txt", "zz\n") }},
	{name: "new unmentioned file", mutate: func(f *fixture) { f.write("misc/new.txt", "n\n") }},
	{name: "deleted test module selects test_shard", mutate: func(f *fixture) { f.remove("tests/test_pad0.py") }},
	{name: "harness change is FULL", mutate: func(f *fixture) { f.write("tests/harness.py", "x = 1\n") }},
	{name: "stubs change is FULL", mutate: func(f *fixture) { f.write("tests/stubs/herdr", "x\n") }},
	{name: "timings change selects test_shard", mutate: func(f *fixture) { f.write("ci/shard_timings.json", "{\"a\": 1}\n") }},
	{name: "many modules trips the 40% cap", mutate: func(f *fixture) {
		for _, m := range []string{"alpha", "bin", "web", "imp", "foo", "roles", "docs", "names", "pad0"} {
			f.write("tests/test_"+m+".py", "# edit\n")
		}
	}},
	{name: "test-tree lints are selected by a tests/ change", mutate: func(f *fixture) {
		f.write("tests/test_repo_hygiene.py", "")
		f.write("tests/test_scrub_clean.py", "")
		f.write("tests/test_alpha.py", "# e\n")
	}},
	{name: "rename is delete plus add", mutate: func(f *fixture) {
		f.git("mv", "web/app.py", "web/app2.py")
	}},
	{name: "empty diff", mutate: func(f *fixture) {}},
}

func runBoth(t *testing.T, f *fixture, base, head string, extra ...string) (py, goOut [3]string) {
	t.Helper()
	script := needPython(t, "ci/affected.py")
	args := append([]string{script, "--root", f.dir, "--base", base, "--head", head}, extra...)
	so, se, code := pyRun(t, f.dir, args...)
	py = [3]string{so, se, fmt.Sprint(code)}
	gargs := append([]string{"--root", f.dir, "--base", base, "--head", head}, extra...)
	gso, gse, gcode := runGo(Main, gargs...)
	goOut = [3]string{gso, gse, fmt.Sprint(gcode)}
	return
}

func TestAffectedParity(t *testing.T) {
	needPython(t, "ci/affected.py")
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := baseFixture(t)
			base := f.git("rev-parse", "HEAD")
			sc.mutate(f)
			head := f.commit("head")
			for _, extra := range [][]string{{"--list"}, {"--quick"}} {
				py, g := runBoth(t, f, base, head, extra...)
				if sc.loose {
					py[0] = cutAt(py[0], "does not parse")
					g[0] = cutAt(g[0], "does not parse")
				}
				if py != g {
					t.Errorf("%v differs\npython: %q\ngo:     %q", extra, py, g)
				}
			}
		})
	}
}

func cutAt(s, marker string) string {
	if i := strings.Index(s, marker); i >= 0 {
		return s[:i+len(marker)]
	}
	return s
}

func TestAffectedParityMissingBase(t *testing.T) {
	f := baseFixture(t)
	py, g := runBoth(t, f, "no-such-ref", "HEAD")
	if py != g {
		t.Errorf("differs\npython: %q\ngo:     %q", py, g)
	}
	if g[2] != "2" {
		t.Errorf("exit code = %s, want 2 (git failure)", g[2])
	}
}

func TestAffectedParityBadArgs(t *testing.T) {
	f := baseFixture(t)
	_, _, code := runGo(Main, "--root", f.dir)
	if code != 2 {
		t.Errorf("missing --base: exit %d, want 2", code)
	}
	_, _, code = runGo(Main, "--root", f.dir, "--base", "HEAD", "--list", "--run")
	if code != 2 {
		t.Errorf("--list with --run: exit %d, want 2", code)
	}
	_, _, code = runGo(Main, "--root", f.dir, "--base", "HEAD", "--budget", "0")
	if code != 2 {
		t.Errorf("--budget 0: exit %d, want 2", code)
	}
}

func TestAffectedParitySmokeFromDB(t *testing.T) {
	script := needPython(t, "ci/affected.py")
	f := baseFixture(t)
	base := f.git("rev-parse", "HEAD")
	f.write("tests/test_alpha.py", "# e\n")
	head := f.commit("head")
	home := os.Getenv("FACTORY_HOME")
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	mk := "import sqlite3,sys\nc=sqlite3.connect(sys.argv[1])\nc.execute('create table config(key text, value text)')\nc.execute(\"insert into config values('affected_smoke','test_foo.py test_docs.py nope.py')\")\nc.commit()\n"
	if _, se, code := pyRun(t, f.dir, "-c", mk, filepath.Join(home, "db", "factory.db")); code != 0 {
		t.Fatalf("cannot build the config DB: %s", se)
	}
	so, _, _ := pyRun(t, f.dir, script, "--root", f.dir, "--base", base, "--head", head)
	gso, _, _ := runGo(Main, "--root", f.dir, "--base", base, "--head", head)
	if so != gso {
		t.Fatalf("python %q, go %q", so, gso)
	}
	if !strings.Contains(gso, "test_foo.py") || strings.Contains(gso, "test_inbox.py") {
		t.Errorf("config affected_smoke not used: %q", gso)
	}
}

// The selection must never say less than Python: a FULL-by-rule change stays FULL in Go (r1).
func TestAffectedHarnessIsFull(t *testing.T) {
	f := baseFixture(t)
	base := f.git("rev-parse", "HEAD")
	f.write("tests/harness.py", "x = 1\n")
	head := f.commit("head")
	so, _, code := runGo(Main, "--root", f.dir, "--base", base, "--head", head)
	if code != 0 || !strings.HasPrefix(so, "FULL (rule 1: tests/harness.py changed)") {
		t.Errorf("got exit %d %q", code, so)
	}
	so, _, _ = runGo(Main, "--root", f.dir, "--base", base, "--head", head, "--quick")
	if !strings.HasPrefix(so, "FULL needed: rely on CI") {
		t.Errorf("quick mode must refuse FULL: %q", so)
	}
}

func TestAffectedParityRunQuick(t *testing.T) {
	script := needPython(t, "ci/affected.py")
	root := repoRoot(t)
	f := newFixture(t)
	for _, rel := range []string{"ci/affected.py", "tests/_slot.py"} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Skipf("reference file %s missing: %v", rel, err)
		}
		f.write(rel, string(b))
	}
	f.write("tests/test_db_layout.py", "import unittest\n\nclass T(unittest.TestCase):\n    def test_one(self):\n        pass\n")
	f.write("tests/test_inbox.py", "import unittest\n\nclass T(unittest.TestCase):\n    def test_two(self):\n        pass\n    def test_three(self):\n        pass\n")
	f.write("tests/test_judge.py", "")
	for i := 0; i < 20; i++ {
		f.write(fmt.Sprintf("tests/test_pad%d.py", i), "")
	}
	f.write("x/y.txt", "1\n")
	base := f.commit("base")
	f.write("tests/test_judge.py", "import unittest\n\nclass T(unittest.TestCase):\n    def test_j(self):\n        pass\n")
	head := f.commit("head")
	for _, extra := range [][]string{{"--run"}, {"--run", "--quick"}} {
		pso, _, pcode := pyRun(t, f.dir, append([]string{script, "--root", f.dir, "--base", base, "--head", head}, extra...)...)
		gso, _, gcode := runGo(Main, append([]string{"--root", f.dir, "--base", base, "--head", head}, extra...)...)
		strip := func(s string) string { // the elapsed line carries wall time
			var keep []string
			for _, l := range strings.Split(s, "\n") {
				if !strings.Contains(l, "elapsed") {
					keep = append(keep, l)
				}
			}
			return strings.Join(keep, "\n")
		}
		if pcode != gcode || strip(pso) != strip(gso) {
			t.Errorf("%v: python exit %d %q, go exit %d %q", extra, pcode, pso, gcode, gso)
		}
		if !strings.Contains(gso, "Ran 4 tests in 3 modules: OK") {
			t.Errorf("%v: unexpected run output %q", extra, gso)
		}
	}
}

// pysrcDump prints, for each file named on the command line, the top-level units and call-site counts as the Python
// ast reports them (ci/affected.py top_units and call_sites).
const pysrcDump = `import json, sys, importlib.util
spec = importlib.util.spec_from_file_location("affected_ref", sys.argv[1])
aff = importlib.util.module_from_spec(spec)
spec.loader.exec_module(aff)
out = {}
for p in sys.argv[2:]:
    src = open(p, encoding="utf-8").read()
    try:
        units = aff.top_units(src)
        calls = aff.call_sites(src)
    except (SyntaxError, ValueError):
        out[p] = None
        continue
    out[p] = {"units": {k: [v[0], v[1], v[2]] for k, v in units.items()}, "calls": calls}
json.dump(out, sys.stdout)
`

func TestPysrcParityOnRepoSources(t *testing.T) {
	script := needPython(t, "ci/affected.py")
	root := repoRoot(t)
	var files []string
	for _, dir := range []string{"bin", "ci", "roles/hooks", "web", "scout", "tests", "plugins"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(p, ".py") && info.Size() < 3_000_000 {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("no Python sources found under the repo root")
	}
	sort.Strings(files)
	so, se, code := pyRun(t, root, append([]string{"-c", pysrcDump, script}, files...)...)
	if code != 0 {
		t.Fatalf("python reference failed: %s", se)
	}
	var ref map[string]*struct {
		Units map[string][3]any `json:"units"`
		Calls map[string]int    `json:"calls"`
	}
	if err := json.Unmarshal([]byte(so), &ref); err != nil {
		t.Fatalf("bad reference JSON: %v", err)
	}
	bad := 0
	for _, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		want := ref[p]
		units, uerr := TopUnits(string(raw))
		calls, cerr := CallSites(string(raw))
		if want == nil {
			continue // Python rejects it; the lexer may accept grammar errors, which is documented
		}
		if uerr != nil || cerr != nil {
			t.Errorf("%s: python parses it, go does not: %v %v", p, uerr, cerr)
			bad++
			continue
		}
		if len(units) != len(want.Units) {
			t.Errorf("%s: %d units, python %d", p, len(units), len(want.Units))
			bad++
			continue
		}
		for k, wu := range want.Units {
			u, ok := units[k]
			if !ok || float64(u.Start) != wu[0].(float64) || float64(u.End) != wu[1].(float64) || u.Text != wu[2].(string) {
				t.Errorf("%s: unit %q differs (go %+v, python %v)", p, k, u.Start, wu[:2])
				bad++
				break
			}
		}
		for k, n := range want.Calls {
			if calls[k] != n {
				t.Errorf("%s: call sites of %q: go %d, python %d", p, k, calls[k], n)
				bad++
				break
			}
		}
		for k, n := range calls {
			if want.Calls[k] != n {
				t.Errorf("%s: call sites of %q: go %d, python %d", p, k, n, want.Calls[k])
				bad++
				break
			}
		}
		if bad > 10 {
			t.Fatal("too many differences; stopping")
		}
	}
	t.Logf("compared %d Python files against ast", len(files))
}

func TestFnMatch(t *testing.T) {
	cases := []struct {
		path, pat string
		want      bool
	}{
		{"ci/scrub_scan.py", "ci/scrub_*", true},
		{"ci/x/scrub_scan.py", "ci/scrub_*", false},
		{"roles/hooks/a/b.py", "roles/hooks/*", true}, // `*` crosses `/`, like fnmatch
		{"roles/x-settings.json", "roles/*settings*", true},
		{".github/workflows/t.yml", ".github/*", true},
		{"a.py", "?.py", true},
		{"ab.py", "?.py", false},
		{"a1.py", "a[0-9].py", true},
		{"ax.py", "a[!0-9].py", true},
		{"a1.py", "a[!0-9].py", false},
	}
	for _, c := range cases {
		if got := FnMatch(c.path, c.pat); got != c.want {
			t.Errorf("FnMatch(%q, %q) = %v, want %v", c.path, c.pat, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{"x = 'abc\n", "x = (1,\n", "x = \"\"\"abc\n", "y = 1)\n"} {
		if _, err := TopUnits(src); err == nil {
			t.Errorf("TopUnits(%q) = nil error, want a parse error", src)
		}
	}
}
