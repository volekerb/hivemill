package tier

import (
	"strings"
	"testing"
)

// FuzzStripFences: the fence stripper must not panic or hang, may only remove text, and is deterministic (G7).
// Run: go test ./internal/citools/tier -run XXX -fuzz FuzzStripFences
func FuzzStripFences(f *testing.F) {
	for _, s := range []string{"a ```b``` c", "~~~\nx\n~~~", "```", "``` ~~~ ``` ~~~", "[a](b.md)\n```\n[c](d.md)\n```\n", "\u00a0```\n", "`` ` ``", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := stripFences(s)
		if len(out) > len(s) {
			t.Fatalf("stripFences grew %q to %q", s, out)
		}
		if out != stripFences(s) {
			t.Fatalf("stripFences is not deterministic on %q", s)
		}
		if !strings.Contains(s, "```") && !strings.Contains(s, "~~~") && out != s {
			t.Fatalf("no fence marker but the text changed: %q -> %q", s, out)
		}
		for _, m := range linkRe.FindAllStringSubmatch(out, -1) {
			if strings.ContainsAny(m[1], ") \t\n") {
				t.Fatalf("link target %q contains a terminator", m[1])
			}
		}
	})
}
