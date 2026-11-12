package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// G8: where Python's \d, \s and \w differ from Go's ASCII-only RE2 classes. Expected values are python3 output.
func TestTaskNonASCIIClasses(t *testing.T) {
	for in, want := range map[string]bool{
		"x [SC١]": true, "[SC1,　SC٢]": true, "[SC1 ,SC2]": true, "[SC1,\x1cSC2]": true,
		"[SC²]": false, "[SC1,​SC2]": false,
	} {
		if got := taskSCTagRE.MatchString(in); got != want {
			t.Errorf("SC_TAG_RE on %q = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]bool{"T-١٢": true, "T-１": true, "t-5": true, "T-²": false} {
		if got := taskIDRe.MatchString(in); got != want {
			t.Errorf("TASK_ID_RE on %q = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string][]string{
		"a²/b.go": {"a²/b.go"}, "١/x.go": {"١/x.go"}, "áb.go": {"a", "b.go"}, "Ⅰ/x.go": {"Ⅰ/x.go"},
	} {
		got := specTokens(in)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("specTokens(%q) = %q, want %q", in, got, want)
		}
	}
}

// G7: no panic; an accepted argv is one of the S2k forms, everything else delegates.
func FuzzParseTaskAdd(f *testing.F) {
	f.Add("--goal", "G-1", "--title", "t")
	f.Add("--spec", "-", "--goal", "G-1")
	f.Add("--tit", "x", "", "")
	f.Fuzz(func(t *testing.T, a, b, c, d string) {
		rest := []string{a, b, c, d}
		for n := 0; n <= 4; n++ {
			p, ok := parseTaskAdd(rest[:n])
			if !ok {
				continue
			}
			got := p.(taskAddCmd)
			if got.goal == "" || got.title == "" || n%2 != 0 {
				t.Fatalf("add accepted %q", rest[:n])
			}
			for i := 1; i < n; i += 2 {
				if rest[i] == "" || (rest[i][0] == '-' && rest[i] != "-") || !strings.HasPrefix(rest[i-1], "--") {
					t.Fatalf("add accepted %q", rest[:n])
				}
			}
		}
	})
}

func FuzzParseTaskNote(f *testing.F) {
	f.Add("T-1", "text", "")
	f.Add("-h", "", "")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		rest := []string{a, b, c}
		for n := 0; n <= 3; n++ {
			p, ok := parseTaskNote(rest[:n])
			if !ok {
				continue
			}
			got := p.(taskNoteCmd)
			for i := 0; i < n; i++ {
				if strings.HasPrefix(rest[i], "-") && rest[i] != "-" {
					t.Fatalf("note accepted %q", rest[:n])
				}
			}
			if (got.id != nil) != (n >= 1) || (got.extra != nil) != (n == 3) {
				t.Fatalf("note positionals wrong for %q", rest[:n])
			}
		}
	})
}

func FuzzParseTaskReopen(f *testing.F) {
	f.Add("T-1", "--reason", "why")
	f.Add("--reason", "-x", "T-1")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		rest := []string{a, b, c}
		for n := 0; n <= 3; n++ {
			p, ok := parseTaskReopen(rest[:n])
			if !ok {
				continue
			}
			got := p.(taskReopenCmd)
			if got.id == "" || strings.HasPrefix(got.id, "-") || strings.HasPrefix(got.reason, "-") && got.reason != "-" {
				t.Fatalf("reopen accepted %q", rest[:n])
			}
		}
	})
}

func FuzzSpecTokens(f *testing.F) {
	f.Add("edit ./a/b.go now")
	f.Add("a//b.go key:val/a.go ١/x")
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		for _, tok := range specTokens(s) {
			if tok == "" || strings.HasPrefix(tok, "/") || strings.HasSuffix(tok, "/") || strings.Contains(tok, "//") {
				t.Fatalf("specTokens(%q) gave %q", s, tok)
			}
			if !strings.Contains(s, tok) {
				t.Fatalf("token %q not in %q", tok, s)
			}
		}
		specFiles(s, newTrackedSet([]string{"go/a.go", "README.md"}))
	})
}

func FuzzSplitAfter(f *testing.F) {
	f.Add(" T-2 , T-1,T-2 ,,")
	f.Fuzz(func(t *testing.T, s string) {
		seen := map[string]bool{}
		for _, d := range splitAfter(s) {
			if d == "" || d != pyStrip(d) || strings.Contains(d, ",") || seen[d] {
				t.Fatalf("splitAfter(%q) gave %q", s, d)
			}
			seen[d] = true
		}
	})
}
