package cli

import (
	"math"
	"reflect"
	"testing"
)

// Every expected value below was produced by python3 (3.x) on the same input.
func TestPySplitlines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// "a\nb\r\nc\rd\x0be\x0cf\x1cg\x1dh\x1ei\u0085j k l".splitlines() == ['a'..'l']
		{"all breaks", "a\nb\r\nc\rd\x0be\x0cf\x1cg\x1dh\x1ei\u0085j k l",
			[]string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}},
		{"trailing newline", "a\n", []string{"a"}},         // ['a']
		{"lone newline", "\n", []string{""}},               // ['']
		{"empty", "", nil},                                 // []
		{"blank line", "a\n\nb", []string{"a", "", "b"}},   // ['a', '', 'b']
		{"crlf crlf", "a\r\n\r\n", []string{"a", ""}},      // ['a', '']
		{"lf cr", "a\n\r", []string{"a", ""}},              // ['a', '']
		{"u2028 only", "x ", []string{"x"}},                // ['x']
		{"x1c separator", "p\x1cq", []string{"p", "q"}},    // ['p', 'q']
		{"x85 separator", "p\u0085q", []string{"p", "q"}},  // ['p', 'q']
		{"no break", "plain text", []string{"plain text"}}, // ['plain text']
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pySplitlines(c.in)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("pySplitlines(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	// "a\r\nb\u0085".splitlines(True) == ['a\r\n', 'b\u0085']
	if got := pySplitlinesKeep("a\r\nb\u0085"); !reflect.DeepEqual(got, []string{"a\r\n", "b\u0085"}) {
		t.Fatalf("keepends: %q", got)
	}
}

func TestPyStrip(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  x　​", "x　​"},        // python: 'x　​' (U+200B is not whitespace)
		{"\x1f\u0085y ", "y"},   // python: 'y'
		{"\x1c\x1d\x1ez ", "z"}, // x1c-x1e are whitespace to str.strip
		{"   q   ", "q"},
		{"   ", ""},
		{"᠎x", "᠎x"}, // U+180E is not whitespace since Unicode 6.3
	}
	for _, c := range cases {
		if got := pyStrip(c.in); got != c.want {
			t.Errorf("pyStrip(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, r := range []rune{'\t', '\n', 0x1c, 0x1f, 0x85, 0xa0, 0x1680, 0x2005, 0x3000} { // str.isspace is True
		if !pyIsSpace(r) {
			t.Errorf("pyIsSpace(%U) = false", r)
		}
	}
	for _, r := range []rune{'a', 0x200b, 0x180e, 0x00, 0x1b, 0x2010} { // False
		if pyIsSpace(r) {
			t.Errorf("pyIsSpace(%U) = true", r)
		}
	}
}

func TestPyIndent(t *testing.T) {
	cases := []struct{ in, want string }{
		// textwrap.indent("a\n  \nb\r\n\u0085c\n", "> ") == '> a\n  \n> b\r\n\u0085> c\n'
		{"a\n  \nb\r\n\u0085c\n", "> a\n  \n> b\r\n\u0085> c\n"},
		// textwrap.indent("a\n b\x1c", "> ") == '> a\n > b\x1c'
		{"a\n b\x1c", "> a\n > b\x1c"},
		{"", ""},
		{"no newline", "> no newline"},
	}
	for _, c := range cases {
		if got := pyIndent(c.in, "> "); got != c.want {
			t.Errorf("pyIndent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPyTrunc(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"héé😀x", 3, "héé"},  // 'héé😀x'[:3]
		{"héé😀x", 4, "héé😀"}, // [:4]
		{"abc", 5, "abc"},    // [:5]
		{"abc", 0, ""},       // [:0]
		{"abc", -1, "ab"},    // [:-1]
		{"abc", -9, ""},      // [:-9]
	}
	for _, c := range cases {
		if got := pyTrunc(c.in, c.n); got != c.want {
			t.Errorf("pyTrunc(%q,%d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestPyCell(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
		err  bool
	}{{nil, "", false}, {int64(-7), "-7", false}, {"s", "s", false}, {[]byte("b"), "b", false}, {1.5, "", true}} {
		got, err := cell(c.in)
		if got != c.want || (err != nil) != c.err || (err != nil && err != errDelegate) {
			t.Errorf("cell(%v) = %q, %v", c.in, got, err)
		}
	}
}

// NEL is U+0085 (python x85); in Go source write it as \u0085, a raw x85 byte is invalid UTF-8.

// Every expected value is repr(x) from python3.
func TestPyFloatRepr(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{4.0, "4.0"}, {0.1, "0.1"}, {1e-05, "1e-05"}, {0.0001, "0.0001"}, {1e16, "1e+16"},
		{1e15, "1000000000000000.0"}, {123456789012345.6, "123456789012345.6"}, {math.Copysign(0, -1), "-0.0"},
		{0, "0.0"}, {math.Inf(1), "inf"}, {math.Inf(-1), "-inf"}, {math.NaN(), "nan"}, {2.675, "2.675"},
		{1.5e300, "1.5e+300"}, {5e-324, "5e-324"}, {9999999999999998.0, "9999999999999998.0"},
		{123456789.123456789, "123456789.12345679"}, {-2.5e-7, "-2.5e-07"}, {100.0, "100.0"}, {1e22, "1e+22"},
	}
	for _, c := range cases {
		if got := pyFloatRepr(c.in); got != c.want {
			t.Errorf("pyFloatRepr(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
