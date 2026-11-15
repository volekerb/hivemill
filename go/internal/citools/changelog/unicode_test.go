package changelog

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unicode/utf8"
)

// G8: where Python's \s, \d, \w and \b differ from Go's ASCII-only RE2 classes. The subjects mix ASCII with
// non-ASCII whitespace, digits and letters; Go must classify and clean them exactly like ci/changelog.py.
var unicodeSubjects = []string{
	"Fix the thing (#12)",
	"éfix accent (#1)",
	"xfix inside word (#2)",
	"Ünew feature (#3)",
	"newé trailing accent (#4)",
	"add١ arabic digit (#5)",
	"Add thing (#6)",
	"Add thing　(#7)",
	"Add thing (#8)",
	"Add 2026-01-02 date (#9)",
	"Add ٢٠٢٦-٠١-٠٢ arabic date (#10)",
	"Add (2026-01-02) paren date (#11)",
	"Add x2026-01-02 glued (#12)",
	"Add 2026-01-02x glued after (#13)",
	"Add 2026-01-02é glued accent (#14)",
	"Add 2026-01-02　(#15)",
	"Add ２０２６-０１-０２ fullwidth date (#16)",
	"Deny list  (#17)",
	"Scrub the _fix_ (#18)",
	"_bug (#19)",
	"bug² (#20)",
	"harden ​ guard​ian (#21)",
	"plain change (#22)",
	"\x1cAdd sep\x1d(#23)",
	"Ǆ new ǆ (#24)",
	"ſecret scrub (#25)",
	"KKeep (#26)",
}

func pyClassify(t *testing.T) (map[string]string, map[string]string) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found: parity with ci/changelog.py not checked")
	}
	ciDir, err := filepath.Abs("../../../../ci")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ciDir, "changelog.py")); err != nil {
		t.Skip("ci/changelog.py not found: parity not checked")
	}
	in, _ := json.Marshal(unicodeSubjects)
	script := `
import json, sys
sys.path.insert(0, sys.argv[1])
import changelog as c
out = {}
for s in json.loads(sys.argv[2]):
    sec = next((n for n, r in c.RULES if r.search(s)), "Changed")
    clean = c.DATE.sub("", c.PR_SUFFIX.sub("", s)).strip() if c.PR_SUFFIX.search(s) else None
    out[s] = [sec, clean]
print(json.dumps(out))
`
	cmd := exec.Command(py, "-I", "-c", script, ciDir, string(in))
	var o []byte
	if o, err = cmd.Output(); err != nil {
		t.Fatalf("python: %v", err)
	}
	var raw map[string][2]*string
	if err := json.Unmarshal(o, &raw); err != nil {
		t.Fatal(err)
	}
	sec, clean := map[string]string{}, map[string]string{}
	for s, v := range raw {
		sec[s] = *v[0]
		if v[1] != nil {
			clean[s] = *v[1]
		} else {
			clean[s] = "\x00none"
		}
	}
	return sec, clean
}

func TestUnicodeClassesMatchPython(t *testing.T) {
	sec, clean := pyClassify(t)
	for _, s := range unicodeSubjects {
		if got := sectionOf(s); got != sec[s] {
			t.Errorf("sectionOf(%q) = %s, python %s", s, got, sec[s])
		}
		if !prSuffix.MatchString(s) {
			if clean[s] != "\x00none" {
				t.Errorf("prSuffix on %q: go no match, python matched", s)
			}
			continue
		}
		if got := cleanSubject(s); got != clean[s] {
			t.Errorf("cleanSubject(%q) = %q, python %q", s, got, clean[s])
		}
	}
}

// Fixed expectations (python3 output), so the checks hold when python3 is absent.
func TestUnicodeFixedExpectations(t *testing.T) {
	for in, want := range map[string]string{
		"éfix accent (#1)":          "Changed",
		"xfix inside word (#2)":     "Changed",
		"Ünew feature (#3)":         "Changed",
		"newé trailing accent (#4)": "Changed",
		"Fix the thing (#12)":       "Fixed",
		"_bug (#19)":                "Changed",
		"Deny list (#17)":           "Security",
		"add new thing":             "Added",
		"Scrub the _fix_ (#18)":     "Security",
		"bug² (#20)":                "Fixed",
		"harden ​ guard​ian (#21)":  "Security",
	} {
		if got := sectionOf(in); got != want {
			t.Errorf("sectionOf(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Add thing (#6)":              "Add thing",
		"Add 2026-01-02 date (#9)":    "Add date",
		"Add x2026-01-02 glued (#12)": "Add x2026-01-02 glued",
		"Add 2026-01-02é glued (#14)": "Add 2026-01-02é glued",
		"Add ٢٠٢٦-٠١-٠٢ d (#10)":      "Add d",
	} {
		if got := cleanSubject(in); got != want {
			t.Errorf("cleanSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpgradeLineUnicodeWhitespace(t *testing.T) {
	for in, want := range map[string]bool{
		"### Upgrade":        true,
		"### Upgrade ":       true,
		"### Upgrade　\n":     true,
		"### Upgrade x":      false,
		"x\n### Upgrade\r\n": true,
		"### Upgradeé":       false,
	} {
		if got := upgradeLine.MatchString(in); got != want {
			t.Errorf("upgradeLine on %q = %v, want %v", in, got, want)
		}
	}
}

// FuzzSubject: no input panics, the cleaned subject is valid UTF-8 when the input is, and sectionOf returns a known section.
func FuzzSubject(f *testing.F) {
	for _, s := range unicodeSubjects {
		f.Add(s)
	}
	f.Add("")
	f.Add("(#1)")
	f.Add("\xff\xfe (#1)")
	f.Fuzz(func(t *testing.T, s string) {
		sec := sectionOf(s)
		known := false
		for _, n := range append(append([]string{}, sections...), "Changed") {
			known = known || sec == n
		}
		if !known {
			t.Fatalf("sectionOf(%q) = %q", s, sec)
		}
		if prSuffix.MatchString(s) {
			c := cleanSubject(s)
			if utf8.ValidString(s) && !utf8.ValidString(c) {
				t.Fatalf("cleanSubject(%q) = %q is not valid UTF-8", s, c)
			}
		}
	})
}
