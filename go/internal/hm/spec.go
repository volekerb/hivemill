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
	marks := itemRe.FindAllStringSubmatchIndex(text, -1)
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
		}
	}
	return probs
}

var exitRe = regexp.MustCompile(`(?i)^(?:exit\s*|rc\s*=?\s*)?(\d+)$`)

// ExpectOK checks a cmd: proof's result against its expectation: an exit code, "empty", or text in the output.
func ExpectOK(expect string, rc int, out string) (bool, string) {
	e := strings.TrimSpace(expect)
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
