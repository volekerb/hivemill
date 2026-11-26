package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type bodyCase struct {
	Body         string          `json:"body"`
	Problems     []string        `json:"problems"`
	Outside      string          `json:"outside"`
	Section      string          `json:"section"`
	Evidence     *string         `json:"evidence"`
	FencesClosed []bool          `json:"fences_closed"`
	TestsRemoved bool            `json:"tests_removed"`
	Command      map[string]bool `json:"command"`
}

func loadBodyCorpus(t testing.TB) []bodyCase {
	t.Helper()
	b, err := os.ReadFile("testdata/done/body_corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []bodyCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// TestPrBodyParity runs every body of testdata/done/body_corpus.json through the Go scanners. The expectations were
// written by bin/factory_cli.py (pr_body_problems, pr_outside_details, _evidence_section, prcheck_evidence_problem,
// _EVIDENCE_FENCE, _TESTS_REMOVED, _evidence_is_command) on the same bodies; a body Go cannot read like Python's re
// (prBodyNeedsPython) is skipped here and handed over by the command.
func TestPrBodyParity(t *testing.T) {
	ran := 0
	for i, c := range loadBodyCorpus(t) {
		if prBodyNeedsPython(c.Body) {
			continue
		}
		ran++
		if got := prBodyProblems(c.Body, prHeadings); !reflect.DeepEqual(got, c.Problems) && !(len(got) == 0 && len(c.Problems) == 0) {
			t.Errorf("case %d problems:\n got %q\nwant %q\nbody %q", i, got, c.Problems, c.Body)
		}
		outside := prOutsideDetails(c.Body)
		if outside != c.Outside {
			t.Errorf("case %d outside:\n got %q\nwant %q", i, outside, c.Outside)
		}
		sec := evidenceSection(outside)
		if sec != c.Section {
			t.Errorf("case %d section:\n got %q\nwant %q", i, sec, c.Section)
		}
		ev := evidenceProblem(sec)
		if (c.Evidence == nil && ev != "") || (c.Evidence != nil && ev != *c.Evidence) {
			t.Errorf("case %d evidence: got %q want %v", i, ev, c.Evidence)
		}
		var closed []bool
		for _, f := range findFences(outside) {
			closed = append(closed, f.closed)
		}
		if !reflect.DeepEqual(closed, c.FencesClosed) && !(len(closed) == 0 && len(c.FencesClosed) == 0) {
			t.Errorf("case %d fences closed: got %v want %v", i, closed, c.FencesClosed)
		}
		if got := testsRemoved(outside); got != c.TestsRemoved {
			t.Errorf("case %d tests removed: got %v want %v\noutside %q", i, got, c.TestsRemoved, outside)
		}
		for cmd, want := range c.Command {
			if got := evidenceIsCommand(cmd); got != want {
				t.Errorf("evidenceIsCommand(%q) = %v, want %v", cmd, got, want)
			}
		}
	}
	if ran < 300 {
		t.Fatalf("only %d corpus bodies were compared; the corpus or prBodyNeedsPython is off", ran)
	}
}

// TestEvidenceRunnerByPath: a runner given by path counts by its base name (T-1940).
func TestEvidenceRunnerByPath(t *testing.T) {
	for cmd, want := range map[string]bool{
		"cd go && GOTOOLCHAIN=local ~/.local/go/bin/go test ./...": true,
		"/usr/bin/python3 -m unittest x":                           true,
		"~/notes/readme.md here":                                   false,
	} {
		if got := evidenceIsCommand(cmd); got != want {
			t.Errorf("evidenceIsCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// TestPrBodyUnicode: NBSP (U+00A0) and \v are whitespace to Python (str.split, \s, strip) and are handled here with the
// same result; a non-ASCII digit (U+0663) or letter is a \d / \w character to Python and not to Go, so such a body is
// handed to Python. The expectations for the first two were printed by Python 3 on the same bodies:
//
//	len("a b\vc d".split()) -> 4
//	bool(re.search(r"^\W*tests? removed:\s*\S", "x\n Tests removed: y", re.I|re.M)) -> True
//	re.search(r"\bRan \d+", "Ran ٣") -> a match (U+0663 is a decimal digit)
func TestPrBodyUnicode(t *testing.T) {
	if n := len(strings.FieldsFunc("a b\vc d", pyIsSpace)); n != 4 {
		t.Errorf("word count with NBSP and \\v: %d, want 4", n)
	}
	if !testsRemoved("x\n Tests removed: y") {
		t.Errorf("an NBSP-prefixed `Tests removed:` line with an NBSP after the colon must count")
	}
	if testsRemoved("Tests removed:  ") {
		t.Errorf("`Tests removed:` followed only by NBSP is empty, as Python's \\s*\\S says")
	}
	if !testsRemoved("\x0bTests removed: z") {
		t.Errorf("\\v before the line text is a non-word character")
	}
	for _, s := range []string{"Ran ٣ tests", "café OK", "İ", "xſ", "K"} {
		if !prBodyNeedsPython(s) {
			t.Errorf("%q must be handed to Python", s)
		}
	}
	for _, s := range []string{"a b", "x\vy", "→ -> ok", "emoji \U0001F600"} {
		if prBodyNeedsPython(s) {
			t.Errorf("%q is read the same by Go and Python", s)
		}
	}
}

func TestPrTemplateHeadings(t *testing.T) {
	home := t.TempDir()
	if h, py := prTemplateHeadings(home); py || !reflect.DeepEqual(h, prHeadings) {
		t.Errorf("no template: %v %v", h, py)
	}
	os.MkdirAll(filepath.Join(home, ".github"), 0o755)
	tpl := "## Summary\n## Why\n## Extra\n<details>\n## Hidden\n</details>\n## Changes\n<!-- ## Comment -->\n## Test evidence\n## Risk / rollback\n## Last   \n"
	os.WriteFile(filepath.Join(home, ".github", "pull_request_template.md"), []byte(tpl), 0o644)
	got, py := prTemplateHeadings(home)
	want := []string{"## Summary", "## Why", "## Extra", "## Changes", "## Test evidence", "## Risk / rollback", "## Last"}
	if py || !reflect.DeepEqual(got, want) {
		t.Errorf("extra headings: %v %v, want %v", got, py, want)
	}
	os.WriteFile(filepath.Join(home, ".github", "pull_request_template.md"), []byte("## Café\n"), 0o644)
	if _, py := prTemplateHeadings(home); !py {
		t.Errorf("a template with a non-ASCII letter must be handed to Python")
	}
	os.WriteFile(filepath.Join(home, ".github", "pull_request_template.md"), []byte("\xff\xfe"), 0o644)
	if h, py := prTemplateHeadings(home); py || !reflect.DeepEqual(h, prHeadings) {
		t.Errorf("undecodable template keeps the built-ins: %v %v", h, py)
	}
}

func FuzzPrBody(f *testing.F) {
	for _, c := range loadBodyCorpus(f)[:40] {
		f.Add(c.Body)
	}
	f.Add("```\n~~~\n")
	f.Add("<details><!-- </details>")
	f.Add("<!--<details>-->\u0000")
	f.Fuzz(func(t *testing.T, body string) {
		if prBodyNeedsPython(body) {
			return
		}
		prBodyProblems(body, prHeadings)
		outside := prOutsideDetails(body)
		evidenceProblem(evidenceSection(outside))
		testsRemoved(outside)
		findFences(outside)
	})
}
