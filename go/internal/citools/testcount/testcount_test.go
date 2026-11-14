package testcount

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// treeWith writes a tree with tests/ modules (name -> source) and returns its root.
func treeWith(t *testing.T, mods map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range mods {
		if err := os.WriteFile(filepath.Join(dir, "tests", name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func pyTests(n int) string {
	s := "import unittest\n\nclass T(unittest.TestCase):\n"
	for i := 0; i < n; i++ {
		s += fmt.Sprintf("    def test_%d(self):\n        pass\n", i)
	}
	return s
}

func TestCountParity(t *testing.T) {
	script := needPython(t, "ci/test_count_gate.py")
	cases := []struct {
		name string
		mods map[string]string
	}{
		{"three modules", map[string]string{"test_a.py": pyTests(3), "test_b.py": pyTests(1), "test_c.py": pyTests(5)}},
		{"no tests", map[string]string{"helper.py": "x = 1\n"}},
		{"sub test case classes", map[string]string{"test_a.py": "import unittest\n\nclass A(unittest.TestCase):\n    def test_x(self): pass\n\nclass B(A):\n    def test_y(self): pass\n"}},
		{"module imports a sibling", map[string]string{"helper.py": "VALUE = 2\n", "test_a.py": "import unittest, helper\n\nclass T(unittest.TestCase):\n    def test_v(self):\n        self.assertEqual(helper.VALUE, 2)\n"}},
		{"load error", map[string]string{"test_bad.py": "import no_such_module_xyz\n"}},
		{"syntax error", map[string]string{"test_bad.py": "def (:\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			tree := treeWith(t, c.mods)
			pso, pse, pcode := pyRun(t, tree, script, "count", tree)
			gso, gse, gcode := runGo(Main, "count", tree, "--python-only")
			if pso != gso || pcode != gcode {
				t.Errorf("python exit %d %q %q; go exit %d %q %q", pcode, pso, pse, gcode, gso, gse)
			}
			if pcode == 2 && (!strings.Contains(gse, "test discovery failed") || gso != "") {
				t.Errorf("a load error must print nothing on stdout and say what failed: %q %q", gso, gse)
			}
		})
	}
}

func TestCountParityNoTestsDir(t *testing.T) {
	script := needPython(t, "ci/test_count_gate.py")
	isolate(t)
	tree := t.TempDir()
	pso, _, pcode := pyRun(t, tree, script, "count", tree)
	gso, _, gcode := runGo(Main, "count", tree, "--python-only")
	if pso != gso || pcode != gcode || gso != "0\n" {
		t.Errorf("python exit %d %q; go exit %d %q; want 0\\n", pcode, pso, gcode, gso)
	}
}

func goTree(t *testing.T, pyN int, goSrc string) string {
	tree := treeWith(t, map[string]string{"test_a.py": pyTests(pyN)})
	gd := filepath.Join(tree, "go")
	if err := os.MkdirAll(gd, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(gd, "go.mod"), []byte("module fixture\n\ngo 1.21\n"), 0o644)
	os.WriteFile(filepath.Join(gd, "x_test.go"), []byte(goSrc), 0o644)
	return tree
}

const goTests = `package fixture

import "testing"

func TestOne(t *testing.T)   {}
func TestTwo(t *testing.T)   {}
func TestThree(t *testing.T) {}
func FuzzParse(f *testing.F) { f.Add("a"); f.Fuzz(func(t *testing.T, s string) {}) }
func BenchmarkX(b *testing.B) {}
func helperTestNot(t *testing.T) {}
`

func TestCountParityReportsGoTests(t *testing.T) {
	script := needPython(t, "ci/test_count_gate.py")
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not found: the Go test count needs `go test -list`")
	}
	isolate(t)
	tree := goTree(t, 2, goTests)
	pso, _, _ := pyRun(t, tree, script, "count", tree)
	gso, gse, gcode := runGo(Main, "count", tree)
	if gcode != 0 {
		t.Fatalf("exit %d: %s", gcode, gse)
	}
	want := pso + "go-tests: 4\n" // 3 tests and 1 fuzz target; the benchmark and the helper do not count
	if gso != want {
		t.Errorf("output %q, want %q", gso, want)
	}
	// without a go/ directory the extra line says 0
	plain := treeWith(t, map[string]string{"test_a.py": pyTests(2)})
	if out, _, _ := runGo(Main, "count", plain); out != "2\ngo-tests: 0\n" {
		t.Errorf("no go dir: %q", out)
	}
}

func TestCountGoFailsOnBrokenModule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not found")
	}
	isolate(t)
	tree := goTree(t, 1, "package fixture\n\nfunc broken( {\n")
	out, se, code := runGo(Main, "count", tree)
	if code != 2 || out != "" || !strings.Contains(se, "go test -list failed") {
		t.Errorf("a Go module that does not build must fail the count: exit %d %q %q", code, out, se)
	}
}

func TestCheckParity(t *testing.T) {
	script := needPython(t, "ci/test_count_gate.py")
	isolate(t)
	dir := t.TempDir()
	bodies := map[string]string{
		"none":        "nothing special\n",
		"bypass":      "text\ntests-removed: moved to go\nmore\n",
		"indented":    "  TESTS-REMOVED:   spaced reason  \n",
		"empty":       "tests-removed:\n",
		"inline only": "see tests-removed: not at line start\n",
	}
	for name, body := range bodies {
		for _, nums := range [][2]string{{"10", "10"}, {"10", "12"}, {"10", "9"}, {"0", "0"}} {
			t.Run(name+"/"+nums[0]+"-"+nums[1], func(t *testing.T) {
				bf := filepath.Join(dir, "body.txt")
				os.WriteFile(bf, []byte(body), 0o644)
				pso, _, pcode := pyRun(t, dir, script, "check", "--base", nums[0], "--pr", nums[1], "--body-file", bf)
				gso, _, gcode := runGo(Main, "check", "--base", nums[0], "--pr", nums[1], "--body-file", bf)
				if pso != gso || pcode != gcode {
					t.Errorf("python exit %d %q; go exit %d %q", pcode, pso, gcode, gso)
				}
			})
		}
	}
	// an unreadable body file counts as an empty body
	pso, _, pcode := pyRun(t, dir, script, "check", "--base", "5", "--pr", "4", "--body-file", filepath.Join(dir, "absent"))
	gso, _, gcode := runGo(Main, "check", "--base", "5", "--pr", "4", "--body-file", filepath.Join(dir, "absent"))
	if pso != gso || pcode != gcode || gcode != 1 {
		t.Errorf("absent body: python exit %d %q; go exit %d %q", pcode, pso, gcode, gso)
	}
}

// A drop in Go tests fails the gate on its own, and the bypass line covers it (r2).
func TestCheckGoCount(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	bf := filepath.Join(dir, "body.txt")
	os.WriteFile(bf, []byte("no bypass\n"), 0o644)
	out, _, code := runGo(Main, "check", "--base", "10", "--pr", "10", "--base-go", "8", "--pr-go", "7", "--body-file", bf)
	if code != 1 || !strings.Contains(out, "go test count dropped from 8 to 7") {
		t.Errorf("go drop: exit %d %q, want 1", code, out)
	}
	out, _, code = runGo(Main, "check", "--base", "10", "--pr", "10", "--base-go", "8", "--pr-go", "9", "--body-file", bf)
	if code != 0 || !strings.Contains(out, "go-base=8 go-pr=9") {
		t.Errorf("go growth: exit %d %q, want 0", code, out)
	}
	os.WriteFile(bf, []byte("tests-removed: ported\n"), 0o644)
	if _, _, code = runGo(Main, "check", "--base", "10", "--pr", "10", "--base-go", "8", "--pr-go", "7", "--body-file", bf); code != 0 {
		t.Errorf("bypass line must allow the go drop: exit %d", code)
	}
	if _, _, code = runGo(Main, "check", "--base", "10", "--pr", "10", "--base-go", "8", "--body-file", bf); code != 2 {
		t.Errorf("--base-go without --pr-go: exit %d, want 2", code)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"nope"}, {"count"}, {"check", "--base", "1"}} {
		if _, _, code := runGo(Main, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
