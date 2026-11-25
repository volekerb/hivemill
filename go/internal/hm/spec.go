package hm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Criterion is one acceptance criterion and the proof it names.
type Criterion struct {
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Kind   string `json:"kind"`  // test | cmd | flow | manual | "" (none)
	Proof  string `json:"proof"` // what follows "kind:"
	Expect string `json:"expect,omitempty"`
}

var (
	itemRe  = regexp.MustCompile(`(?m)(?:^|\s)(?:AC)?(\d+)[.)]\s+`)
	proofRe = regexp.MustCompile(`(?:^|[^\w-])(test|cmd|flow|manual):`)
)

// Criteria parses acceptance text. Numbered items (`1.`, `2)`, `AC3.`) are criteria and text before the first one is a
// preamble; without numbers each `- ` line is one; else the whole text is one criterion.
func Criteria(acceptance string) []Criterion {
	text := strings.TrimSpace(acceptance)
	if text == "" {
		return nil
	}
	type item struct {
		n    int
		body string
	}
	var items []item
	marks := itemMarks(text)
	if len(marks) > 0 {
		for i, m := range marks {
			end := len(text)
			if i+1 < len(marks) {
				end = marks[i+1][0]
			}
			n, _ := strconv.Atoi(text[m[2]:m[3]])
			items = append(items, item{n, text[m[1]:end]})
		}
	} else {
		n := 0
		for _, ln := range strings.Split(text, "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "- ") || strings.HasPrefix(ln, "* ") {
				n++
				items = append(items, item{n, ln[2:]})
			}
		}
		if len(items) == 0 {
			items = []item{{1, text}}
		}
	}
	out := make([]Criterion, 0, len(items))
	for _, it := range items {
		body := strings.Join(strings.Fields(it.body), " ")
		c := Criterion{ID: it.n, Text: body}
		if m := proofRe.FindStringSubmatchIndex(body); m != nil {
			c.Kind = body[m[2]:m[3]]
			c.Proof = strings.TrimSpace(strings.Trim(strings.TrimSpace(body[m[1]:]), "`"))
			if c.Kind == "cmd" {
				if i := strings.LastIndex(c.Proof, "=>"); i >= 0 {
					c.Expect = strings.TrimSpace(strings.Trim(strings.TrimSpace(c.Proof[i+2:]), "`"))
					c.Proof = strings.TrimSpace(strings.Trim(strings.TrimSpace(c.Proof[:i]), "`"))
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// itemMarks finds the criterion numbers. A number at the start of a line always starts a criterion; a number inside
// a line (the inline form `1) a 2) b`) starts one only when it is the next number in sequence, so prose such as
// "(exit 3) naming" or "cache write 2000)" stays part of its criterion.
func itemMarks(text string) [][]int {
	var out [][]int
	prev := 0
	for _, m := range itemRe.FindAllStringSubmatchIndex(text, -1) {
		n, _ := strconv.Atoi(text[m[2]:m[3]])
		lead := strings.TrimSpace(text[strings.LastIndex(text[:m[2]], "\n")+1 : m[2]])
		lineStart := lead == "" || lead == "AC"
		if lineStart || len(out) == 0 || n == prev+1 {
			out = append(out, m)
			prev = n
		}
	}
	return out
}

// LintProblems says why the criteria fail the spec gate (nil = they pass).
func LintProblems(acceptance string) []string {
	crit := Criteria(acceptance)
	if len(crit) == 0 {
		return []string{"no acceptance criteria"}
	}
	var probs []string
	seen := map[int]bool{}
	for _, c := range crit {
		if seen[c.ID] {
			probs = append(probs, fmt.Sprintf("criterion %d is numbered twice", c.ID))
		}
		seen[c.ID] = true
		switch {
		case c.Kind == "":
			probs = append(probs, fmt.Sprintf("criterion %d names no proof: add test:<name>, cmd:<command> => <exit N or text>, "+
				"flow:<flow> or manual:<why only the owner can check>", c.ID))
		case c.Proof == "":
			probs = append(probs, fmt.Sprintf("criterion %d: %s: has nothing after it", c.ID, c.Kind))
		case c.Kind == "cmd" && c.Expect == "":
			probs = append(probs, fmt.Sprintf("criterion %d: cmd: needs `=> <expected exit code or output>`", c.ID))
		case c.Kind == "cmd" && proseExpectRe.MatchString(c.Expect):
			probs = append(probs, fmt.Sprintf("criterion %d: `=> %s` is prose and is matched literally, so it can never pass: "+
				"use an exact value, `exit 0` with grep -q, or `empty`", c.ID, c.Expect))
		case c.Kind == "test" && proseTestProof(c.Proof):
			probs = append(probs, fmt.Sprintf("criterion %d: test:%s has prose after the test name or file: "+
				"use cmd:<the exact test command> => exit 0", c.ID, c.Proof))
		}
	}
	return probs
}

// proseExpectRe matches an expectation that starts with a comparison phrase: ExpectOK would match it literally.
var proseExpectRe = regexp.MustCompile(`(?i)^(?:(?:at\s+least|at\s+most|more\s+than|less\s+than|greater|fewer)\b|[<>]=?\s*\d|[<>]=\s)`)

var testIDRe = regexp.MustCompile(`^[A-Za-z_][\w./]*$`)

// proseTestProof reports a test: proof that cannot run: prose after a file or path, or a name TestArgv cannot resolve.
func proseTestProof(proof string) bool {
	p := strings.TrimRight(strings.TrimSpace(proof), ").,;")
	if f := strings.Fields(p); len(f) > 1 {
		first := f[0]
		if strings.HasSuffix(first, ".py") || strings.HasSuffix(first, ".go") {
			return true
		}
		return strings.Contains(first, "/") && !strings.HasPrefix(first, "./") && !strings.HasPrefix(first, "/") &&
			!strings.HasPrefix(first, "~") && !strings.HasSuffix(first, ".sh") && !strings.HasPrefix(f[1], "-")
	}
	if strings.HasSuffix(p, ".py") {
		return false
	}
	return strings.HasSuffix(p, ".go") || !testIDRe.MatchString(p)
}

var exitRe = regexp.MustCompile(`(?i)^(?:exit\s*|rc\s*=\s*)(\d+)$`)
var numRe = regexp.MustCompile(`^-?\d+$`)

// ExpectOK checks a cmd: proof's result against its expectation: `exit N` (or `rc=N`) is the exit code, `empty` is
// no output, a bare number is the whole output (a count such as `grep -c`), anything else is text the output contains.
func ExpectOK(expect string, rc int, out string) (bool, string) {
	e := strings.TrimSpace(expect)
	if numRe.MatchString(e) {
		got := strings.TrimSpace(out)
		return got == e, fmt.Sprintf("exit %d, output %q, want %s", rc, truncate(got, 40), e)
	}
	if m := exitRe.FindStringSubmatch(e); m != nil {
		want, _ := strconv.Atoi(m[1])
		return rc == want, fmt.Sprintf("exit %d, want %d", rc, want)
	}
	switch strings.ToLower(e) {
	case "empty", "no output", "nothing":
		empty := strings.TrimSpace(out) == ""
		return empty, map[bool]string{true: "output empty", false: "output not empty"}[empty]
	}
	has := strings.Contains(out, e)
	short := e
	if len(short) > 60 {
		short = short[:60]
	}
	return has, fmt.Sprintf("exit %d, output %s %q", rc, map[bool]string{true: "has", false: "lacks"}[has], short)
}
