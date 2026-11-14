package data

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// The expected values below were produced by the Python originals (Python 3.13, web/queries.py regexes).

func TestUnicodeRegexParity(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a  b", "a b"},        // NBSP + EM SPACE collapse like re.sub(r"\s+", " ")
		{" [SC1]　x y ", "x y"}, // ideographic space and LINE SEPARATOR are \s
		{"a\x1fb", "a b"},      // U+001F is \s in Python
		{"​z", "​z"},           // ZERO WIDTH SPACE is not
	} {
		if got := stripTags(c.in); got != c.want {
			t.Errorf("stripTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := tags("[SC١٢, SC٣] x"); !reflect.DeepEqual(got, map[int64]bool{12: true, 3: true}) {
		t.Errorf("tags with Arabic-Indic digits = %v", got)
	}
	got := ParseCriteria(" ١. x\n٣) y\n  more")
	want := []CriterionText{{1, "x"}, {3, "y more"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseCriteria = %v, want %v", got, want)
	}
	if m := waitsRe.FindStringSubmatch("x waits=Té y"); m == nil || m[1] != "Té" {
		t.Errorf("waits=(\\w+) on a non-ASCII letter = %v", m)
	}
	for _, c := range []struct {
		s    string
		end  bool
		want bool
	}{
		{"status=blocked", true, true},
		{"éstatus=blocked", true, false}, // a letter before: no \b
		{"status=blockedé", true, false}, // a letter after: no \b
		{"status=blocked.", true, true},
		{"x status=blocked y", true, true},
		{"status=blocked status=blocked٣", true, true}, // the first occurrence still matches
	} {
		if got := hasWordAt(blockedRe, c.s, c.end); got != c.want {
			t.Errorf("blocked %q = %v, want %v", c.s, got, c.want)
		}
	}
	if hasWordAt(anyStatus, "xstatus=a", false) || !hasWordAt(anyStatus, "status=é", false) || hasWordAt(anyStatus, "status=", false) {
		t.Error("anyStatus boundary/word class differs from Python")
	}
	if got := wholeWordTaskIDs("T-١٢ ÉT-3 T-4é T-5"); !reflect.DeepEqual(got, []string{"T-١٢", "T-5"}) {
		t.Errorf("wholeWordTaskIDs = %q", got)
	}
	if m := agentTaskRe.FindStringSubmatch("/x/G-٤٥-T-٦"); m == nil || m[1] != "G-٤٥-T-٦" {
		t.Errorf("agentTaskRe = %v", m)
	}
	if pyTrim("\x1f a \x1f") != "a" {
		t.Error("pyTrim must strip U+001F like str.strip")
	}
	if _, ok := ParseTS("2026-11-14T11:00:00 "); !ok {
		t.Error("ParseTS must strip NBSP like str.strip")
	}
	if n, ok := pyDigits("١٢\U0001d7d6"); !ok || n != 128 { // MATHEMATICAL DOUBLE-STRUCK DIGIT EIGHT
		t.Errorf("pyDigits = %d %v", n, ok)
	}
	if _, ok := pyDigits("99999999999999999999"); ok {
		t.Error("pyDigits must report overflow")
	}
}

func FuzzParseTS(f *testing.F) {
	for _, s := range []string{"", "2026-11-14T11:00:00Z", "2026-11-14 11:00:00.25+01:00", "2026-02-30", "2026-11-14T11:00:00+25:00", "٢026-11-14", "2026-11-14T11:00:00 "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		tm, ok := ParseTS(s)
		if !ok {
			return
		}
		// A parsed instant is a real calendar date that survives a format/parse round trip.
		if y := tm.Year(); y < 0 || y > 9999 {
			t.Fatalf("year %d out of range for %q", y, s)
		}
		again, ok2 := ParseTS(tm.Format("2006-01-02T15:04:05.000000Z07:00"))
		if !ok2 || again.Unix() != tm.Unix() {
			t.Fatalf("round trip of %q: %v vs %v (%v)", s, tm, again, ok2)
		}
	})
}

func FuzzParseCriteria(f *testing.F) {
	for _, s := range []string{"", "1. a\n2) b", "no numbers\nat all", "١. x\n 2) y", "9999999999999999999999. big", "\r\n\v\f\x1c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, c := range ParseCriteria(s) {
			if !utf8.ValidString(c.Text) && utf8.ValidString(s) {
				t.Fatalf("invalid UTF-8 produced from valid input %q", s)
			}
			if c.Text != pyTrim(c.Text) {
				t.Fatalf("criterion text %q is not trimmed", c.Text)
			}
		}
		_ = stripTags(s)
		_ = tags(s)
	})
}

func FuzzParseDayPositive(f *testing.F) {
	for _, s := range []string{"", "2026-11-14", "0", "5", "-1", "٥", "99999999999999999999"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if d, err := ParseDay(s); err == nil && d != s {
			t.Fatalf("ParseDay(%q) = %q: must round-trip", s, d)
		}
		if n, err := ParsePositive(s); err == nil && n < 1 {
			t.Fatalf("ParsePositive(%q) = %d", s, n)
		}
	})
}

func FuzzWholeWordTaskIDs(f *testing.F) {
	for _, s := range []string{"", "T-1", "xT-1 T-2y", "T-١"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, id := range wholeWordTaskIDs(s) {
			if len(id) < 3 || id[:2] != "T-" {
				t.Fatalf("bad id %q from %q", id, s)
			}
		}
	})
}
