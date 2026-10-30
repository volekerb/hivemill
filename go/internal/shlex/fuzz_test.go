package shlex

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// FuzzLexer checks only that no input panics or hangs (the fuzz engine kills a
// hung exec) and that Quote/Split round-trip; a differential check against
// CPython is not possible from a Go test.
func FuzzLexer(f *testing.F) {
	for _, s := range []string{"", "a 'b", `a \`, "x>&2", "a|&b", "café 中", "a # b\nc", `"a\"b"`, "$(a;b)&&c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, o := range []Options{DefaultOptions(), ScriptOptions(), {WhitespaceSplit: true}} {
			if _, err := Lex(s, o); err != nil && err != ErrNoClosingQuotation && err != ErrNoEscapedCharacter {
				t.Fatalf("unexpected error %v for %q", err, s)
			}
		}
		if !utf8.ValidString(s) {
			return
		}
		got, err := Split(Quote(s))
		if err != nil || !reflect.DeepEqual(got, []string{s}) {
			t.Fatalf("Split(Quote(%q)) = %q, %v", s, got, err)
		}
	})
}
