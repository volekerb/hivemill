package affected

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// Fuzz targets for the hand-written Python source lexer and the fnmatch translation (G7): neither may panic or hang
// on any input, and both must be deterministic. Run: go test ./internal/citools/affected -run XXX -fuzz FuzzTopUnits

func FuzzTopUnits(f *testing.F) {
	for _, s := range []string{
		"def a():\n    return 1\n\n\nclass B:\n    def m(self):\n        pass\n",
		"x = '''abc\n''' + \"d\"\n",
		"f'{a!r:>{w}} {{x}}'\n",
		"if x:\n\tpass\n  \n\\\n",
		"a = (1,\n  2)\n; b = 3;\n",
		"def f(:\n",
		"'''unterminated",
		"\x00\xff\u00a0def \u00e9():\n    pass\n",
		"@dec\nasync def g():\n    await h()\n",
		"x = 1 \\\r\ny = 2\r\n",
	} {
		f.Add(s, s+"\n# c\n")
	}
	f.Fuzz(func(t *testing.T, base, head string) {
		u1, e1 := TopUnits(base)
		u2, e2 := TopUnits(base)
		if (e1 == nil) != (e2 == nil) || !reflect.DeepEqual(u1, u2) {
			t.Fatalf("TopUnits is not deterministic on %q", base)
		}
		c1, ce1 := CallSites(base)
		c2, ce2 := CallSites(base)
		if (ce1 == nil) != (ce2 == nil) || !reflect.DeepEqual(c1, c2) {
			t.Fatalf("CallSites is not deterministic on %q", base)
		}
		if d, err := ChangedDefs(base, head); err == nil {
			if d2, _ := ChangedDefs(base, head); !reflect.DeepEqual(d, d2) {
				t.Fatalf("ChangedDefs is not deterministic")
			}
		}
		if ChangedDefsSame, err := ChangedDefs(base, base); err == nil && len(ChangedDefsSame) != 0 {
			t.Fatalf("identical sources report changed defs %v", ChangedDefsSame)
		}
		lines := PySplitLines(base)
		total := 0
		for _, l := range lines {
			total += len(l)
		}
		if total > len(base) {
			t.Fatalf("PySplitLines produced more bytes than the input")
		}
	})
}

func FuzzFnMatch(f *testing.F) {
	for _, p := range [][2]string{{"tests/test_a.py", "tests/test_*.py"}, {"a/b", "a/[!x]?"}, {"x", "["}, {"", ""}, {"a]", "[]]"}, {"\u00e9", "?"}, {"a", "[a-"}, {"a-", "[!-]"}} {
		f.Add(p[0], p[1])
	}
	f.Fuzz(func(t *testing.T, path, pat string) {
		a := FnMatch(path, pat)
		if a != FnMatch(path, pat) {
			t.Fatalf("FnMatch(%q, %q) is not deterministic", path, pat)
		}
		if !utf8.ValidString(pat) {
			return
		}
		if pat == "*" && !a {
			t.Fatalf("* must match %q", path)
		}
	})
}
