package shard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func pyModule(n int, failing bool) string {
	s := "import unittest\n\nclass T(unittest.TestCase):\n"
	for i := 0; i < n; i++ {
		s += fmt.Sprintf("    def test_%02d(self):\n        pass\n", i)
	}
	if failing {
		s += "    def test_fail(self):\n        self.fail('boom')\n"
	}
	return s
}

// shardFixture is a tree with ci/shard.py (copied from the real checkout), tests and a timing file.
func shardFixture(t *testing.T, timings map[string]float64, mods map[string]string) (root, script string) {
	script = needPython(t, "ci/shard.py")
	isolate(t)
	root = t.TempDir()
	src, err := os.ReadFile(script)
	if err != nil {
		t.Skip(err)
	}
	write := func(rel, text string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ci/shard.py", string(src))
	for name, text := range mods {
		write("tests/"+name, text)
	}
	if timings != nil {
		b, _ := json.Marshal(timings)
		write("ci/shard_timings.json", string(b))
	}
	return root, filepath.Join(root, "ci", "shard.py")
}

func defaultMods() map[string]string {
	m := map[string]string{}
	for i := 0; i < 9; i++ {
		m[fmt.Sprintf("test_m%d.py", i)] = pyModule(i%4+1, false)
	}
	m["test_guard_diff.py"] = pyModule(7, false)
	return m
}

func defaultTimings() map[string]float64 {
	return map[string]float64{"test_m0.py": 3.5, "test_m1.py": 0.1, "test_m2.py": 12.0, "test_m3.py": 7.25, "test_m4.py": 7.25,
		"test_m5.py": 1.0, "test_m6.py": 2.2, "test_guard_diff.py": 60.0, "test_gone.py": 5.0}
}

func TestShardParityList(t *testing.T) {
	for name, timings := range map[string]map[string]float64{"with split module": defaultTimings(), "no timing file": nil, "all equal": {"test_m0.py": 1, "test_m1.py": 1, "test_m2.py": 1, "test_m3.py": 1, "test_m4.py": 1, "test_m5.py": 1, "test_m6.py": 1, "test_m7.py": 1, "test_m8.py": 1}} {
		t.Run(name, func(t *testing.T) {
			root, script := shardFixture(t, timings, defaultMods())
			pso, _, pcode := pyRun(t, root, script, "list")
			gso, _, gcode := runGo(Main, "list", "--root", root)
			if pso != gso || pcode != gcode {
				t.Errorf("list: python %q, go %q", pso, gso)
			}
			for total := 1; total <= 5; total++ {
				for sh := 1; sh <= total; sh++ {
					args := []string{"list", "--shard", fmt.Sprint(sh), "--total", fmt.Sprint(total)}
					pso, pse, pcode := pyRun(t, root, append([]string{script}, args...)...)
					gso, gse, gcode := runGo(Main, append(args, "--root", root)...)
					if pso != gso || pcode != gcode || pse != gse {
						t.Errorf("%v: python exit %d %q %q; go exit %d %q %q", args, pcode, pso, pse, gcode, gso, gse)
					}
				}
			}
		})
	}
}

func TestShardParityListRealRepo(t *testing.T) {
	script := needPython(t, "ci/shard.py")
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "tests")); err != nil {
		t.Skip("no tests/ dir in the checkout")
	}
	for _, total := range []int{1, 2, 6, 8, 13} {
		for sh := 1; sh <= total; sh++ {
			args := []string{"list", "--shard", fmt.Sprint(sh), "--total", fmt.Sprint(total)}
			pso, _, pcode := pyRun(t, root, append([]string{script}, args...)...)
			gso, _, gcode := runGo(Main, append(args, "--root", root)...)
			if pso != gso || pcode != gcode {
				t.Fatalf("%v differ: python exit %d, go exit %d\npython: %.200q\ngo:     %.200q", args, pcode, gcode, pso, gso)
			}
		}
	}
}

func TestShardParityListErrors(t *testing.T) {
	root, script := shardFixture(t, defaultTimings(), defaultMods())
	for _, args := range [][]string{{"list", "--shard", "0", "--total", "3"}, {"list", "--shard", "4", "--total", "3"}, {"list", "--shard", "2", "--total", "0"}} {
		pso, pse, pcode := pyRun(t, root, append([]string{script}, args...)...)
		gso, gse, gcode := runGo(Main, append(args, "--root", root)...)
		if pso != gso || pcode != gcode || pse != gse || gcode != 1 {
			t.Errorf("%v: python exit %d %q %q; go exit %d %q %q", args, pcode, pso, pse, gcode, gso, gse)
		}
	}
}

func readReport(t *testing.T, p string) map[string]any {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	delete(r, "seconds") // wall time
	return r
}

func TestShardParityRunAndVerify(t *testing.T) {
	root, script := shardFixture(t, defaultTimings(), defaultMods())
	const total = 3
	pdir, gdir := t.TempDir(), t.TempDir()
	for sh := 1; sh <= total; sh++ {
		pf, gf := filepath.Join(pdir, fmt.Sprintf("s%d.json", sh)), filepath.Join(gdir, fmt.Sprintf("s%d.json", sh))
		args := []string{"run", "--shard", fmt.Sprint(sh), "--total", fmt.Sprint(total), "--out"}
		_, _, pcode := pyRun(t, root, append(append([]string{script}, args...), pf)...)
		_, _, gcode := runGo(Main, append(append(args, gf), "--root", root)...)
		if pcode != gcode || pcode != 0 {
			t.Fatalf("shard %d: python exit %d, go exit %d", sh, pcode, gcode)
		}
		if p, g := readReport(t, pf), readReport(t, gf); !reflect.DeepEqual(p, g) {
			t.Errorf("shard %d report: python %v, go %v", sh, p, g)
		}
	}
	// the split module's test ids are spread over all shards
	var r struct {
		TestIDs map[string][]string `json:"test_ids"`
	}
	b, _ := os.ReadFile(filepath.Join(gdir, "s1.json"))
	json.Unmarshal(b, &r)
	if len(r.TestIDs["test_guard_diff.py"]) == 0 {
		t.Errorf("shard 1 report has no test ids of the split module: %s", b)
	}
	for _, dir := range []string{pdir, gdir} {
		pso, pse, pcode := pyRun(t, root, script, "verify", "--total", fmt.Sprint(total), "--dir", dir)
		gso, gse, gcode := runGo(Main, "verify", "--total", fmt.Sprint(total), "--dir", dir, "--root", root)
		pso, gso = stripSeconds(pso), stripSeconds(gso)
		if pso != gso || pcode != gcode || pse != gse || gcode != 0 {
			t.Errorf("verify %s: python exit %d %q %q; go exit %d %q %q", dir, pcode, pso, pse, gcode, gso, gse)
		}
	}
}

func stripSeconds(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if i := strings.LastIndex(l, ", "); i >= 0 && strings.HasSuffix(l, " s") {
			l = l[:i]
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func TestShardParityRunOnlyAndFailures(t *testing.T) {
	mods := defaultMods()
	mods["test_m1.py"] = pyModule(2, true) // one failing test
	root, script := shardFixture(t, defaultTimings(), mods)
	only := filepath.Join(t.TempDir(), "only.txt")
	os.WriteFile(only, []byte("test_m1.py\ntest_m2.py\n\n"), 0o644)
	for sh := 1; sh <= 2; sh++ {
		pf, gf := filepath.Join(t.TempDir(), "p.json"), filepath.Join(t.TempDir(), "g.json")
		args := []string{"run", "--shard", fmt.Sprint(sh), "--total", "2", "--only", only, "--out"}
		_, pse, pcode := pyRun(t, root, append(append([]string{script}, args...), pf)...)
		_, gse, gcode := runGo(Main, append(append(args, gf), "--root", root)...)
		if pcode != gcode {
			t.Errorf("shard %d: python exit %d, go exit %d", sh, pcode, gcode)
		}
		if p, g := readReport(t, pf), readReport(t, gf); !reflect.DeepEqual(p, g) {
			t.Errorf("shard %d report: python %v, go %v", sh, p, g)
		}
		_ = pse
		_ = gse
	}
	// a failing module must fail the shard that runs it (exit 1), in both
	var failed bool
	for sh := 1; sh <= 2; sh++ {
		gf := filepath.Join(t.TempDir(), "g.json")
		if _, _, code := runGo(Main, "run", "--shard", fmt.Sprint(sh), "--total", "2", "--out", gf, "--root", root); code == 1 {
			failed = true
		}
	}
	if !failed {
		t.Errorf("no shard failed although test_m1.py has a failing test")
	}
}

func TestShardParityVerifyErrors(t *testing.T) {
	root, script := shardFixture(t, defaultTimings(), defaultMods())
	good := t.TempDir()
	for sh := 1; sh <= 2; sh++ {
		runGo(Main, "run", "--shard", fmt.Sprint(sh), "--total", "2", "--out", filepath.Join(good, fmt.Sprintf("s%d.json", sh)), "--root", root)
	}
	cases := map[string]func(dir string){
		"missing shard": func(dir string) { os.Remove(filepath.Join(dir, "s2.json")) },
		"module twice": func(dir string) {
			r := readRaw(t, filepath.Join(dir, "s1.json"))
			r["modules"] = append(r["modules"].([]any), "test_m0.py", "test_m0.py")
			writeRaw(t, filepath.Join(dir, "s2.json"), mergeModules(t, dir, r))
		},
		"unknown module": func(dir string) {
			r := readRaw(t, filepath.Join(dir, "s1.json"))
			r["modules"] = append(r["modules"].([]any), "test_ghost.py")
			writeRaw(t, filepath.Join(dir, "s1.json"), r)
		},
		"wrong total": func(dir string) {
			r := readRaw(t, filepath.Join(dir, "s1.json"))
			r["total"] = 5
			writeRaw(t, filepath.Join(dir, "s1.json"), r)
		},
		"unreadable report": func(dir string) { os.WriteFile(filepath.Join(dir, "s1.json"), []byte("{nope"), 0o644) },
		"split module missing ids": func(dir string) {
			r := readRaw(t, filepath.Join(dir, "s1.json"))
			delete(r, "test_ids")
			writeRaw(t, filepath.Join(dir, "s1.json"), r)
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ents, _ := os.ReadDir(good)
			for _, e := range ents {
				b, _ := os.ReadFile(filepath.Join(good, e.Name()))
				os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644)
			}
			mut(dir)
			pso, pse, pcode := pyRun(t, root, script, "verify", "--total", "2", "--dir", dir)
			gso, gse, gcode := runGo(Main, "verify", "--total", "2", "--dir", dir, "--root", root)
			if pcode != 1 || gcode != 1 || pso != gso {
				t.Errorf("python exit %d %q %q; go exit %d %q %q", pcode, pso, pse, gcode, gso, gse)
			}
			if name != "unreadable report" && pse != gse { // the JSON error text differs between the two parsers
				t.Errorf("stderr: python %q, go %q", pse, gse)
			}
		})
	}
}

func readRaw(t *testing.T, p string) map[string]any {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func writeRaw(t *testing.T, p string, r map[string]any) {
	b, _ := json.Marshal(r)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// mergeModules gives shard 2's report the modules of shard 1 plus its own, so a module is covered twice.
func mergeModules(t *testing.T, dir string, s1 map[string]any) map[string]any {
	r2 := readRaw(t, filepath.Join(dir, "s2.json"))
	r2["modules"] = append(r2["modules"].([]any), s1["modules"].([]any)...)
	return r2
}

func TestShardParityTimings(t *testing.T) {
	root, script := shardFixture(t, defaultTimings(), defaultMods())
	dir := t.TempDir()
	reports := []string{
		`{"shard": 1, "total": 2, "modules": ["a"], "tests": 1, "seconds": {"test_b.py": 1.6, "test_a.py": 0.0, "test_c.py": 12.3}}`,
		`{"shard": 2, "total": 2, "modules": ["b"], "tests": 1, "seconds": {"test_d.py": 0.1, "test_c.py": 0.7}}`,
	}
	for i, r := range reports {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("r%d.json", i)), []byte(r), 0o644)
	}
	pout, gout := filepath.Join(t.TempDir(), "p.json"), filepath.Join(t.TempDir(), "g.json")
	pso, _, pcode := pyRun(t, root, script, "timings", "--dir", dir, "--out", pout)
	gso, _, gcode := runGo(Main, "timings", "--dir", dir, "--out", gout, "--root", root)
	pb, _ := os.ReadFile(pout)
	gb, _ := os.ReadFile(gout)
	pso, gso = strings.ReplaceAll(pso, pout, "OUT"), strings.ReplaceAll(gso, gout, "OUT")
	if pcode != gcode || pso != gso || string(pb) != string(gb) || len(pb) == 0 {
		t.Errorf("python exit %d %q %q; go exit %d %q %q", pcode, pso, pb, gcode, gso, gb)
	}
}

func TestSplitLimitMatchesPython(t *testing.T) {
	root, script := shardFixture(t, defaultTimings(), defaultMods())
	for _, total := range []int{1, 2, 3, 4, 6} {
		snippet := "import importlib.util, json, sys\nspec = importlib.util.spec_from_file_location('s', sys.argv[1])\nm = importlib.util.module_from_spec(spec)\nspec.loader.exec_module(m)\nmods = m.all_modules(sys.argv[2])\nt = m.load_timings(sys.argv[3])\njson.dump(sorted(m.split_modules(mods, t, int(sys.argv[4]))), sys.stdout)\n"
		so, se, code := pyRun(t, root, "-c", snippet, script, filepath.Join(root, "tests"), filepath.Join(root, "ci", "shard_timings.json"), fmt.Sprint(total))
		if code != 0 {
			t.Fatalf("python reference failed: %s", se)
		}
		mods, _ := AllModules(filepath.Join(root, "tests"))
		timings, _ := LoadTimings(filepath.Join(root, "ci", "shard_timings.json"))
		got := []string{}
		for m := range SplitModules(mods, timings, total) {
			got = append(got, m)
		}
		b, _ := json.Marshal(got)
		if string(b) != so {
			t.Errorf("total %d: go %s, python %s", total, b, so)
		}
	}
}

func TestShardUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"nope"}, {"run", "--shard", "1"}, {"verify"}, {"timings"}} {
		if _, _, code := runGo(Main, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
