package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The PR body checks of `factory prcheck` (bin/factory_cli.py 4750-4818 and 11380-11500): the template headings, the
// acceptance walk, the code-fence scanner and the test-evidence shape. Python's regexes are Unicode and some use
// back-references and look-arounds, which RE2 has not; the scanners below are written by hand. Two classes of input
// are read differently by Go's ASCII \b, \w and \d, so prBodyNeedsPython reports them and the command hands over:
// a non-ASCII letter or digit (Python's \w and \d match them) and the four characters Python's IGNORECASE folds to
// ASCII letters (U+0130, U+0131, U+017F, U+212A).

const (
	acceptanceWalk = "## Acceptance walk"
	prTemplateFile = ".github/pull_request_template.md"
	prWordsMax     = 300
	prWordsTarget  = "150-250"
)

var prHeadings = []string{"## Summary", "## Why", "## Changes", "## Test evidence", "## Risk / rollback"}

// prBodyNeedsPython reports text Go's regexp cannot read the way Python's re does.
func prBodyNeedsPython(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r < 0x80 {
			continue
		}
		if r == 0x130 || r == 0x131 || r == 0x17f || r == 0x212a || unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func asciiLowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 32
	}
	return b
}

// indexFold is the index of the first occurrence of the lower-case ASCII needle in s at or after from, folding ASCII
// case only; -1 for none.
func indexFold(s, needle string, from int) int {
	for i := from; i+len(needle) <= len(s); i++ {
		ok := true
		for j := 0; j < len(needle); j++ {
			if asciiLowerByte(s[i+j]) != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func isASCIIWord(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// stripComments is _PR_COMMENT.sub("", s): <!-- ... --> with the shortest body.
func stripComments(s string) string {
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(s[i:], "<!--")
		if j < 0 {
			break
		}
		j += i
		k := strings.Index(s[j+4:], "-->")
		if k < 0 {
			break
		}
		b.WriteString(s[i:j])
		i = j + 4 + k + 3
	}
	b.WriteString(s[i:])
	return b.String()
}

// detailsBlocks is _PR_DETAILS.finditer: the [start, end) spans of <details\b ... </details> (case-insensitive,
// shortest, no nesting).
func detailsBlocks(s string) [][2]int {
	var out [][2]int
	i := 0
	for {
		j := indexFold(s, "<details", i)
		for j >= 0 && j+8 < len(s) && isASCIIWord(s[j+8]) { // \b after "details"
			j = indexFold(s, "<details", j+1)
		}
		if j < 0 {
			return out
		}
		k := indexFold(s, "</details>", j+8)
		if k < 0 {
			return out
		}
		out = append(out, [2]int{j, k + 10})
		i = k + 10
	}
}

// prOutsideDetails is pr_outside_details: the body without <details> blocks and HTML comments.
func prOutsideDetails(body string) string {
	s := stripComments(body)
	var b strings.Builder
	last := 0
	for _, sp := range detailsBlocks(s) {
		b.WriteString(s[last:sp[0]])
		last = sp[1]
	}
	b.WriteString(s[last:])
	return stripComments(b.String())
}

// fence is one _EVIDENCE_FENCE match: its span, the text between the opener line and the closer, and whether a closer
// was found (an unclosed fence runs to the end of the text).
type fence struct {
	start, end int
	block      string
	closed     bool
}

func isLineStart(s string, p int) bool { return p == 0 || s[p-1] == '\n' }

// fenceOpener reads an opener at line start p: the fence run, and the index just after the opener line's newline.
func fenceOpener(s string, p int) (run string, bodyStart int, ok bool) {
	q := p
	for q < len(s) && (s[q] == ' ' || s[q] == '\t') {
		q++
	}
	if q >= len(s) || (s[q] != '`' && s[q] != '~') {
		return "", 0, false
	}
	c := s[q]
	r := q
	for r < len(s) && s[r] == c {
		r++
	}
	if r-q < 3 {
		return "", 0, false
	}
	t := r
	for t < len(s) && s[t] != '\n' {
		if c == '`' && s[t] == '`' { // the info string of a backtick fence has no backtick
			return "", 0, false
		}
		t++
	}
	if t >= len(s) { // no newline ends the opener line
		return "", 0, false
	}
	return s[q:r], t + 1, true
}

// fenceCloser reports whether the line starting at p closes a fence opened by run: spaces or tabs, a run of the same
// character at least as long, spaces or tabs, an optional \r, then the end of the line.
func fenceCloser(s string, p int, run string) (end int, ok bool) {
	q := p
	for q < len(s) && (s[q] == ' ' || s[q] == '\t') {
		q++
	}
	if !strings.HasPrefix(s[q:], run) {
		return 0, false
	}
	q += len(run)
	for q < len(s) && s[q] == run[0] {
		q++
	}
	for q < len(s) && (s[q] == ' ' || s[q] == '\t') {
		q++
	}
	if q < len(s) && s[q] == '\r' {
		q++
	}
	if q < len(s) && s[q] != '\n' {
		return 0, false
	}
	return q, true
}

// findFences is _EVIDENCE_FENCE.finditer(s) (re.M | re.S).
func findFences(s string) []fence {
	var out []fence
	p := 0
	for p <= len(s) {
		if !isLineStart(s, p) {
			nl := strings.IndexByte(s[p:], '\n')
			if nl < 0 {
				break
			}
			p += nl + 1
			continue
		}
		run, bodyStart, ok := fenceOpener(s, p)
		if !ok {
			nl := strings.IndexByte(s[p:], '\n')
			if nl < 0 {
				break
			}
			p += nl + 1
			continue
		}
		f := fence{start: p, end: len(s), block: s[bodyStart:]}
		for lp := bodyStart; lp <= len(s); {
			if isLineStart(s, lp) {
				if e, ok := fenceCloser(s, lp, run); ok {
					f.end, f.block, f.closed = e, s[bodyStart:lp], true
					break
				}
			}
			nl := strings.IndexByte(s[lp:], '\n')
			if nl < 0 {
				break
			}
			lp += nl + 1
		}
		out = append(out, f)
		if f.end <= p { // cannot happen (the opener is non-empty); never loop
			break
		}
		p = f.end
	}
	return out
}

func inSpans(spans []fence, i int) bool {
	for _, f := range spans {
		if f.start <= i && i < f.end {
			return true
		}
	}
	return false
}

// removeFences is _EVIDENCE_FENCE.sub("", s).
func removeFences(s string, fs []fence) string {
	var b strings.Builder
	last := 0
	for _, f := range fs {
		b.WriteString(s[last:f.start])
		last = f.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// evidenceSection is _evidence_section: the text of "## Test evidence" up to the next "## " line outside a fence.
func evidenceSection(outside string) string {
	spans := findFences(outside)
	start := -1
	for p := 0; p < len(outside); {
		if isLineStart(outside, p) && strings.HasPrefix(outside[p:], "## Test evidence") && !inSpans(spans, p) {
			q := p + len("## Test evidence")
			for q < len(outside) && (outside[q] == ' ' || outside[q] == '\t' || outside[q] == '\r') {
				q++
			}
			if q >= len(outside) {
				start = q
				break
			}
			if outside[q] == '\n' {
				start = q + 1
				break
			}
		}
		nl := strings.IndexByte(outside[p:], '\n')
		if nl < 0 {
			break
		}
		p += nl + 1
	}
	if start < 0 {
		return ""
	}
	end := len(outside)
	for p := start; p < len(outside); {
		if isLineStart(outside, p) && strings.HasPrefix(outside[p:], "## ") && !inSpans(spans, p) {
			end = p
			break
		}
		nl := strings.IndexByte(outside[p:], '\n')
		if nl < 0 {
			break
		}
		p += nl + 1
	}
	return outside[start:end]
}

var evidenceRunners = []string{"python3", "python", "pytest", "go", "bash", "sh", "zsh", "make", "git", "gh", "grep", "factory"}

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// evidenceIsCommand is _evidence_is_command.
func evidenceIsCommand(text string) bool {
	t := pyStrip(strings.TrimLeft(pyStrip(text), "$"))
	for _, seg := range splitSegments(t) {
		toks := strings.FieldsFunc(seg, pyIsSpace)
		for len(toks) > 0 {
			name, _, found := strings.Cut(toks[0], "=")
			if !found || !identRE.MatchString(name) {
				break
			}
			toks = toks[1:]
		}
		if len(toks) >= 2 && (inList(evidenceRunners, toks[0]) || strings.HasPrefix(toks[0], "ci/") || strings.HasPrefix(toks[0], "./")) {
			return true
		}
	}
	return false
}

// splitSegments is re.split(r"&&|;", t).
func splitSegments(t string) []string {
	var out []string
	last := 0
	for i := 0; i < len(t); {
		switch {
		case strings.HasPrefix(t[i:], "&&"):
			out = append(out, t[last:i])
			i += 2
			last = i
		case t[i] == ';':
			out = append(out, t[last:i])
			i++
			last = i
		default:
			i++
		}
	}
	return append(out, t[last:])
}

var (
	evOutSimple = regexp.MustCompile(`\bRan [0-9]+|\bOK\b|\bFAILED\b|\bpass(?:ed|es)?\b|\brc=[0-9]+|\bexit [0-9]+|->|→`)
	evFailed    = regexp.MustCompile(`\b[0-9]+ failed\b`)
	evBacktick  = regexp.MustCompile("`([^`\n]+)`")
)

// failedInTimeRest reports the lookahead of "N failed": spaces or tabs, then not "in <time>", then a lower-case
// letter. When true "N failed" followed by this text is NOT evidence (e.g. "3 failed tests").
func failedBlocked(rest string) bool {
	q := 0
	for q < len(rest) && (rest[q] == ' ' || rest[q] == '\t') {
		q++
	}
	if q == 0 || q >= len(rest) {
		return false
	}
	if inTimeShape(rest[q:]) {
		return false
	}
	return rest[q] >= 'a' && rest[q] <= 'z'
}

// inTimeShape is in[ \t]+\d[\d.]*(?:s|m(?![a-z])).
func inTimeShape(s string) bool {
	if !strings.HasPrefix(s, "in") {
		return false
	}
	q := 2
	n := 0
	for q < len(s) && (s[q] == ' ' || s[q] == '\t') {
		q++
		n++
	}
	if n == 0 || q >= len(s) || s[q] < '0' || s[q] > '9' {
		return false
	}
	for q < len(s) && (s[q] >= '0' && s[q] <= '9' || s[q] == '.') {
		q++
	}
	if q >= len(s) {
		return false
	}
	if s[q] == 's' {
		return true
	}
	return s[q] == 'm' && !(q+1 < len(s) && s[q+1] >= 'a' && s[q+1] <= 'z')
}

// evidenceHasOutput is _EVIDENCE_OUTPUT.search(sec) is not None.
func evidenceHasOutput(sec string) bool {
	if evOutSimple.MatchString(sec) {
		return true
	}
	for _, m := range evFailed.FindAllStringIndex(sec, -1) {
		if !failedBlocked(sec[m[1]:]) {
			return true
		}
	}
	return false
}

// evidenceProblem is prcheck_evidence_problem: "" when the section shows a command and an output line.
func evidenceProblem(sec string) string {
	var cands []string
	for _, m := range evBacktick.FindAllStringSubmatch(removeFences(sec, findFences(sec)), -1) {
		cands = append(cands, m[1])
	}
	for _, f := range findFences(sec) {
		cands = append(cands, pySplitlines(f.block)...)
	}
	found := false
	for _, c := range cands {
		if evidenceIsCommand(c) {
			found = true
			break
		}
	}
	if !found {
		return "`## Test evidence` has no command in backticks: a command line of 2+ words starting with a runner " +
			"(python3, pytest, go, bash, make, git, gh, grep, factory, ci/..., ./...), " +
			"e.g. `python3 -m unittest tests.test_x` -> Ran 7, OK; a bare file or test name is not a command"
	}
	if !evidenceHasOutput(sec) {
		return "`## Test evidence` has a command but no output line: add its key result, " +
			"e.g. `python3 -m unittest tests.test_x` -> Ran 7, OK (markers: Ran N, OK, FAILED, N failed, passed, rc=N, exit N, ->)"
	}
	return ""
}

func rstripPy(s string) string { return strings.TrimRightFunc(s, pyIsSpace) }

// prTemplateHeadings is pr_template_headings: the built-in headings plus the extra "## " headings of the factory home's
// template. An unreadable or non-UTF-8 template leaves the built-ins; no template edit removes a built-in heading.
func prTemplateHeadings(home string) (headings []string, needPython bool) {
	b, err := os.ReadFile(filepath.Join(home, prTemplateFile))
	if err != nil || !utf8.Valid(b) {
		return prHeadings, false
	}
	if prBodyNeedsPython(string(b)) {
		return nil, true
	}
	merged := append([]string(nil), prHeadings...)
	prev := ""
	for _, ln := range pySplitlines(prOutsideDetails(pyNewlines(string(b)))) {
		if !strings.HasPrefix(ln, "## ") {
			continue
		}
		h := rstripPy(ln)
		idx := -1
		for i, m := range merged {
			if m == h {
				idx = i
			}
		}
		if idx < 0 {
			at := 0
			for i, m := range merged {
				if m == prev {
					at = i + 1
					break
				}
			}
			merged = append(merged[:at], append([]string{h}, merged[at:]...)...)
		}
		prev = h
	}
	return merged, false
}

func walkHasItem(block string) bool {
	lines := pySplitlines(block)
	i := -1
	for k, ln := range lines {
		if rstripPy(ln) == acceptanceWalk {
			i = k
			break
		}
	}
	for _, ln := range lines[i+1:] {
		t := pyStrip(ln)
		if t != "" && !strings.HasPrefix(t, "</") {
			return true
		}
	}
	return false
}

// prBodyProblems is pr_body_problems: every way the body departs from the template, as sentences.
func prBodyProblems(body string, headings []string) []string {
	var problems []string
	outside := prOutsideDetails(body)
	spans := findFences(outside)
	var lines []string
	off := 0
	for _, raw := range pySplitlinesKeep(outside) {
		if inSpans(spans, off) {
			lines = append(lines, "")
		} else {
			lines = append(lines, rstripPy(raw))
		}
		off += len(raw)
	}
	var missing []string
	for _, h := range headings {
		if !inList(lines, h) {
			missing = append(missing, "`"+h+"`")
		}
	}
	if len(missing) > 0 {
		problems = append(problems, "missing section heading(s): "+strings.Join(missing, ", "))
	} else {
		sorted := true
		prev := -1
		for _, h := range headings {
			at := 0
			for i, l := range lines {
				if l == h {
					at = i
					break
				}
			}
			if at < prev {
				sorted = false
			}
			prev = at
		}
		if !sorted {
			q := make([]string, len(headings))
			for i, h := range headings {
				q[i] = "`" + h + "`"
			}
			problems = append(problems, "section headings out of order; the template order is "+strings.Join(q, ", "))
		}
	}
	stripped := stripComments(body)
	var walk []string
	for _, sp := range detailsBlocks(stripped) {
		blk := stripped[sp[0]:sp[1]]
		for _, ln := range pySplitlines(blk) {
			if rstripPy(ln) == acceptanceWalk {
				walk = append(walk, blk)
				break
			}
		}
	}
	if len(walk) == 0 {
		problems = append(problems, "no <details> block holding a `"+acceptanceWalk+"` line (the walk goes inside <details>)")
	} else {
		found := false
		for _, blk := range walk {
			if walkHasItem(blk) {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, "`"+acceptanceWalk+"` has no non-empty line under it")
		}
	}
	if n := len(strings.FieldsFunc(outside, pyIsSpace)); n > prWordsMax {
		problems = append(problems, strconv.Itoa(n)+" words outside <details>; the limit is "+strconv.Itoa(prWordsMax)+" (target "+prWordsTarget+
			"): move evidence into <details> or cut")
	}
	return problems
}

// testsRemoved is _TESTS_REMOVED.search(outside): a line start, any non-word characters (line breaks included),
// "test" or "tests", " removed:", spaces, then a non-space (re.I | re.M).
func testsRemoved(outside string) bool {
	for p := 0; p <= len(outside); {
		if isLineStart(outside, p) {
			q := p
			for q < len(outside) && !isASCIIWord(outside[q]) {
				_, w := utf8.DecodeRuneInString(outside[q:])
				q += w
			}
			if foldPrefix(outside[q:], "test") {
				r := q + 4
				if r < len(outside) && (outside[r] == 's' || outside[r] == 'S') {
					if foldPrefix(outside[r+1:], " removed:") {
						if restHasNonSpace(outside[r+1+9:]) {
							return true
						}
					}
				}
				if foldPrefix(outside[r:], " removed:") && restHasNonSpace(outside[r+9:]) {
					return true
				}
			}
		}
		nl := strings.IndexByte(outside[p:], '\n')
		if nl < 0 {
			break
		}
		p += nl + 1
	}
	return false
}

func foldPrefix(s, lower string) bool {
	if len(s) < len(lower) {
		return false
	}
	for i := 0; i < len(lower); i++ {
		if asciiLowerByte(s[i]) != lower[i] {
			return false
		}
	}
	return true
}

// restHasNonSpace is \s*\S at the start of s.
func restHasNonSpace(s string) bool {
	for _, r := range s {
		if !pyIsSpace(r) {
			return true
		}
	}
	return false
}
