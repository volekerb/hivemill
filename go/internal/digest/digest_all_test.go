package digest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoFile is a file of the repository checkout (the merge gate's go proof runs on the whole merge tree).
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", filepath.FromSlash(rel))
}

// TestDigestAllHeadingsMatchPython (risk r1): the section literals are the Python ones, read from the source files.
func TestDigestAllHeadingsMatchPython(t *testing.T) {
	read := func(rel string) string {
		b, err := os.ReadFile(repoFile(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	scout, retro, scoutDB := read("scout/report.py"), read("retro/report.py"), read("scout/db.py")
	for _, want := range []string{
		`HEADING = "` + dgaScoutHeading + `"`,
		`FOOTER = "` + dgaScoutFooter + `"`,
		`MAX_FINDS = 5`,
		`TRIAL_ENDED = ("passed", "failed", "timeout", "over-budget", "error")`,
	} {
		if !strings.Contains(scout, want) {
			t.Errorf("scout/report.py lacks %q", want)
		}
	}
	if !strings.Contains(retro, `HEADING = "`+dgaRetroHeading+`"`) {
		t.Errorf("retro/report.py lacks the heading %q", dgaRetroHeading)
	}
	if !strings.Contains(scoutDB, `CONTROL_KEY = "`+dgaControlKey+`"`) {
		t.Errorf("scout/db.py lacks CONTROL_KEY %q", dgaControlKey)
	}
	if dgaMaxFinds != 5 || strings.Join(dgaTrialEnded, ",") != "passed,failed,timeout,over-budget,error" {
		t.Error("constants drifted from the Python literals")
	}
}

func TestDgaPyStrAndRiskDetail(t *testing.T) {
	num := func(s string) any { return json.Number(s) }
	for in, want := range map[string]any{"None": nil, "x": "x", "True": true, "False": false, "12": num("12"), "0": num("-0"), "-5": num("-5")} {
		got, err := dgaPyStr(want)
		if err != nil || got != in {
			t.Errorf("dgaPyStr(%v) = %q, %v; want %q", want, got, err, in)
		}
	}
	for _, v := range []any{num("1.5"), num("1e3"), []any{}, map[string]any{}, num("1234567890123456789")} {
		if _, err := dgaPyStr(v); !errors.Is(err, errDelegate) {
			t.Errorf("dgaPyStr(%v) must delegate, got %v", v, err)
		}
	}
	for raw, want := range map[string]string{
		"": "", `["a","b","c","d"]`: "a, b, c", `[1, true, null]`: "1, True, None", `"abcdef"`: "a, b, c", `"é✓"`: "é, ✓",
		"not json": "", "null": "", "5": "", "true": "", `["a"] x`: "",
	} {
		got, err := dgaRiskDetail(raw)
		if err != nil || got != want {
			t.Errorf("dgaRiskDetail(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{`{"a": 1}`, `[NaN]`, `[1.5]`, `["\ud800"]`, `[[1]]`} {
		if _, err := dgaRiskDetail(raw); !errors.Is(err, errDelegate) {
			t.Errorf("dgaRiskDetail(%q) must delegate, got %v", raw, err)
		}
	}
}

// TestDgaNonASCII (G8): Python's str.split() and \d accept Unicode spaces and digits; Go must not treat them as ASCII.
func TestDgaNonASCII(t *testing.T) {
	if got := dgaLine("a  b c\n٣"); got != "a b c ٣" {
		t.Errorf("dgaLine collapses Unicode whitespace: %q", got)
	}
	if got := dgaLine("  　 "); got != "-" {
		t.Errorf("a text of only Unicode whitespace is empty: %q", got)
	}
	// an Arabic-Indic digit is a Python \d but not an integer this port prints: it is a string value, kept verbatim
	if dgaIntRE.MatchString("٣") || dgaIntRE.MatchString("1٣") {
		t.Error("dgaIntRE must be ASCII-only")
	}
	if got, err := dgaPyStr("٣"); err != nil || got != "٣" {
		t.Errorf("string value: %q %v", got, err)
	}
	if _, err := dgaPyStr(json.Number("٣")); !errors.Is(err, errDelegate) {
		t.Errorf("a non-ASCII number must delegate, got %v", err)
	}
}

func TestDgaNumAndMedian(t *testing.T) {
	for _, tc := range []struct {
		vals []float64
		want string
	}{{nil, "-"}, {[]float64{3}, "3"}, {[]float64{3, 4}, "3.5"}, {[]float64{1, 9, 5}, "5"}, {[]float64{2, 4}, "3"}, {[]float64{0.25}, "0.2"}} {
		if got := dgaNumStr(dgaMedian(tc.vals)); got != tc.want {
			t.Errorf("median %v = %q, want %q", tc.vals, got, tc.want)
		}
	}
	for _, v := range []any{"x", []byte("1"), float64(1 << 60), int64(1) << 60} {
		if _, _, err := dgaNum(v); !errors.Is(err, errDelegate) {
			t.Errorf("dgaNum(%v) must delegate, got %v", v, err)
		}
	}
	if f, null, err := dgaNum(nil); err != nil || !null || f != 0 {
		t.Errorf("dgaNum(nil) = %v %v %v", f, null, err)
	}
}

func FuzzDgaJSON(f *testing.F) {
	for _, s := range []string{"[]", `["a",1]`, `{"a":1}`, "NaN", `"\ud800"`, "[1] x", "", "null", `[1.5e3]`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		v, ok, err := dgaJSON(raw)
		if err != nil {
			if !errors.Is(err, errDelegate) {
				t.Fatalf("%q: unexpected error %v", raw, err)
			}
			return
		}
		if ok != json.Valid([]byte(raw)) {
			t.Fatalf("%q: ok=%v but json.Valid=%v", raw, ok, json.Valid([]byte(raw)))
		}
		if !ok && v != nil {
			t.Fatalf("%q: a rejected value must be nil", raw)
		}
		// whatever dgaJSON accepts, the helpers either print it or delegate; they never panic
		_, _ = dgaRiskDetail(raw)
		_, _ = dgaPyStr(v)
		_, _ = dgaTruthy(v)
	})
}
