package guard

// CI tools (T-1485): the shared core of go/cmd/ci-scrub-scan, ci-public-text-guard and ci-pre-push-public-text.
// It reuses the pattern engine of scrub.go (genRules, extraRule, foldKey, isWord, pyLines, ...) so the CLI tools and
// the worker guard cannot disagree on what a hit is. scrub.go only counts hits by id; the CLI tools also print the
// masked text, so this file adds the span-returning view (matches), the Scanner label/hide helpers of
// ci/scrub_scan.py and the two allowlist readers. Owner terms are read only from the files the Python scripts read
// (--terms-file paths, $SCRUB_TERMS_FILE, the scrub_terms_file config path) and are never printed.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	ciEnvFile       = "env-file"
	ciBinaryProbe   = 8000 // scrub_scan.BINARY_PROBE
	ciOwnerTermHide = "owner-term"
)

// ciOut is a writer that remembers its first error: a failed write must end the tool with exit 2, not vanish.
type ciOut struct {
	w   io.Writer
	err error
}

func (o *ciOut) printf(format string, a ...any) {
	if o.err != nil {
		return
	}
	if _, err := fmt.Fprintf(o.w, format, a...); err != nil {
		o.err = fmt.Errorf("write output: %w", err)
	}
}

// ciMatch is one hit of the line scanner: the pattern id and the byte span of the match in the line.
type ciMatch struct {
	id         string
	start, end int
}

// ciPair is scan_line's (pattern id, masked text).
type ciPair struct{ id, masked string }

// ciScanner is scrub_scan.Scanner (extra false) or public_text_guard.GuardScanner (extra true).
type ciScanner struct {
	extra       bool
	secretsOnly bool     // skip the identityRules (secretsonly.go)
	keys        [][]rune // fold key of each term, in the (sorted) order the terms were given
	order       []int    // indexes into keys by decreasing length (stable): the alternation order of Scanner.term_re
	short       []bool   // term is at most shortTerm characters: whole words only
}

func newCIScanner(terms []string, extra bool) *ciScanner {
	scanOnce.Do(buildRules)
	sc := &ciScanner{extra: extra}
	for _, t := range terms {
		rs := []rune(t)
		k := make([]rune, len(rs))
		for j, r := range rs {
			k[j] = foldKey(r)
		}
		sc.keys = append(sc.keys, k)
		sc.short = append(sc.short, len(rs) <= shortTerm)
	}
	sc.order = make([]int, len(terms))
	for i := range sc.order {
		sc.order[i] = i
	}
	sort.SliceStable(sc.order, func(a, b int) bool { return len(sc.keys[sc.order[a]]) > len(sc.keys[sc.order[b]]) })
	return sc
}

// spans is finditer of one rule over a line: the byte span of every kept match (count in scrub.go, with spans).
func (r *scanRule) spans(s string) [][2]int {
	var out [][2]int
	pos := 0
	for pos <= len(s) {
		loc := r.re.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}
		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += pos
			}
		}
		start := loc[0]
		if (r.lb != nil && !r.lb(s, start)) || (r.asrt != nil && !r.asrt(s, loc)) {
			if start >= len(s) {
				break
			}
			pos = start + runeLen(s, start)
			continue
		}
		end := loc[2*r.main+1]
		if r.keep == nil || r.keep(s, loc) {
			out = append(out, [2]int{start, end})
		}
		if end <= start {
			end = start + 1
		}
		pos = end
	}
	return out
}

// termSpans is term_re.finditer: the byte spans of the owner terms in text.
func (sc *ciScanner) termSpans(text string) [][2]int {
	if len(sc.keys) == 0 {
		return nil
	}
	var rs, keys []rune
	var offs []int // byte offset of each rune (a stray byte is one U+FFFD rune of one byte, as in Python's replace)
	for i, r := range text {
		rs, keys, offs = append(rs, r), append(keys, foldKey(r)), append(offs, i)
	}
	offs = append(offs, len(text))
	var out [][2]int
	for i := 0; i < len(rs); {
		matched := 0
		for _, t := range sc.order {
			term := sc.keys[t]
			end := i + len(term)
			if end > len(rs) || len(term) == 0 {
				continue
			}
			ok := true
			for j, k := range term {
				if keys[i+j] != k {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			if sc.short[t] && ((i > 0 && isWord(rs[i-1])) || (end < len(rs) && isWord(rs[end]))) {
				continue
			}
			matched = len(term)
			break
		}
		if matched > 0 {
			out = append(out, [2]int{offs[i], offs[i+matched]})
			i += matched
		} else {
			i++
		}
	}
	return out
}

// matches is Scanner.matches / GuardScanner.matches: GENERIC, the owner terms, then (guard) EXTRA.
func (sc *ciScanner) matches(line string) []ciMatch {
	var out []ciMatch
	for _, r := range genRules {
		if sc.secretsOnly && identityRules[r.id] {
			continue
		}
		for _, sp := range r.spans(line) {
			out = append(out, ciMatch{r.id, sp[0], sp[1]})
		}
	}
	for _, sp := range sc.termSpans(line) {
		out = append(out, ciMatch{idOwnerTerm, sp[0], sp[1]})
	}
	if sc.extra {
		for _, r := range extraRule {
			for _, sp := range r.spans(line) {
				out = append(out, ciMatch{r.id, sp[0], sp[1]})
			}
		}
	}
	return out
}

// label is Scanner.label: the index of the term, never any of its text. The first term whose case folding equals
// the matched text wins (fullmatch, not a lower() lookup: re.I folds more than str.lower()).
func (sc *ciScanner) label(text string) string {
	rs := []rune(text)
	for i, k := range sc.keys {
		if len(k) != len(rs) {
			continue
		}
		eq := true
		for j, r := range rs {
			if foldKey(r) != k[j] {
				eq = false
				break
			}
		}
		if eq {
			return fmt.Sprintf("%s#%d", ciOwnerTermHide, i+1)
		}
	}
	return ciOwnerTermHide + "#?"
}

// ciMask is scrub_scan.mask: at most the first 4 characters (fewer for short matches); the rest is hidden.
func ciMask(text string) string {
	rs := []rune(text)
	return string(rs[:min(4, len(rs)/2)]) + "***"
}

// scanLine is Scanner.scan_line: (pattern id, masked text) of every hit on one line.
func (sc *ciScanner) scanLine(line string) []ciPair {
	var out []ciPair
	for _, m := range sc.matches(line) {
		if m.id == idOwnerTerm {
			out = append(out, ciPair{m.id, sc.label(line[m.start:m.end])})
			continue
		}
		out = append(out, ciPair{m.id, ciMask(sc.hideTerms(line[m.start:m.end]))}) // a hit overlapping a term must not show a term prefix
	}
	return out
}

// hideTerms is Scanner.hide_terms: owner terms inside printed text print as owner-term#<n>.
func (sc *ciScanner) hideTerms(text string) string {
	spans := sc.termSpans(text)
	if len(spans) == 0 {
		return text
	}
	var b strings.Builder
	pos := 0
	for _, sp := range spans {
		b.WriteString(text[pos:sp[0]])
		b.WriteString(sc.label(text[sp[0]:sp[1]]))
		pos = sp[1]
	}
	b.WriteString(text[pos:])
	return b.String()
}

// hideAll is Scanner.hide_all: every hit (not only owner terms) in text that is about to be printed is masked.
func (sc *ciScanner) hideAll(text string) string {
	ms := sc.matches(text)
	sort.SliceStable(ms, func(a, b int) bool {
		if ms[a].start != ms[b].start {
			return ms[a].start < ms[b].start
		}
		return ms[a].end < ms[b].end
	})
	var b strings.Builder
	pos := 0
	for _, m := range ms {
		if m.start < pos {
			continue
		}
		b.WriteString(text[pos:m.start])
		if m.id == idOwnerTerm {
			b.WriteString(sc.label(text[m.start:m.end]))
		} else {
			b.WriteString(ciMask(sc.hideTerms(text[m.start:m.end])))
		}
		pos = m.end
	}
	b.WriteString(text[pos:])
	return b.String()
}

// shown is scrub_scan.shown: a raw path as one line of printable text, every hit masked, control characters escaped.
func (sc *ciScanner) shown(path []byte) string {
	text := sc.hideAll(pyDecodeReplace(path))
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if c := text[i]; c < 0x20 || c == 0x7f {
			fmt.Fprintf(&b, "\\x%02x", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ----- owner terms -----

// ciLoadTerms is scrub_scan.load_terms: the stripped non-empty lines of the files, de-duplicated, sorted by
// str.lower. The file content is never printed or logged; an unreadable file is a usage error naming the path only.
func ciLoadTerms(paths []string) ([]string, error) {
	seen := map[string]struct{}{}
	var terms []string
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, &usageError{"cannot read terms file " + pyRepr(p) + ": " + pyStrerror(err) + ". Check SCRUB_TERMS_FILE / --terms-file."}
		}
		for _, line := range pyLines(pyDecodeReplace(b)) {
			t := strings.TrimFunc(line, pyIsSpace)
			if t == "" {
				continue
			}
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				terms = append(terms, t)
			}
		}
	}
	sort.SliceStable(terms, func(a, b int) bool { return pyLowerFull(terms[a]) < pyLowerFull(terms[b]) })
	return terms, nil
}

// ciTermsOrFail is public_text_guard.load_terms_or_fail(os.environ, configPath, extraPaths).
func ciTermsOrFail(configPath string, extra []string) ([]string, error) {
	paths := append([]string(nil), extra...)
	if v := os.Getenv(termsVar); v != "" {
		paths = append(paths, v)
	} else if len(paths) == 0 && configPath != "" {
		paths = append(paths, configPath)
	}
	terms, err := ciLoadTerms(paths)
	if err != nil {
		return nil, err
	}
	if len(terms) == 0 {
		return nil, &usageError{noTermsMsg + ". Set SCRUB_TERMS_FILE (or the scrub_terms_file config) to the owner terms file."}
	}
	return terms, nil
}

// ciDefaultAllowlist is scrub_scan.DEFAULT_ALLOWLIST (ci/scrub_allowlist.txt next to the Python script). The Go tool
// has no script directory: it looks for ci/scrub_allowlist.txt in the checkout above the executable or above any
// given root (the factory home). It reads no environment variable and never the working directory: the allowlist only
// loosens the guard, so the pushing worker must not be able to choose it. Not found -> "", which the loader refuses
// (fail closed).
func ciDefaultAllowlist(roots ...string) string {
	const name = "scrub_allowlist.txt"
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	for _, r := range roots {
		for d := r; ; d = filepath.Dir(d) {
			if st, err := os.Stat(filepath.Join(d, "ci", name)); err == nil && !st.IsDir() {
				return filepath.Join(d, "ci", name)
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return ""
}

// ----- allowlists -----

// ciAllowEntry is one `path:pattern-id:regex  # reason` line; pid "" is the legacy `path:regex` form.
type ciAllowEntry struct {
	path string
	pid  string
	re   *regexp.Regexp
	src  string
}

// ciAllowlist is scrub_scan.Allowlist (applies to --tree, --path and the pushed added lines).
type ciAllowlist struct {
	entries []ciAllowEntry
	used    []bool
}

func ciLoadAllowlist(path string, warn io.Writer) (*ciAllowlist, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &usageError{"cannot read allowlist " + pyRepr(path) + ": " + pyStrerror(err)}
	}
	if msg := pyUTF8Error(raw); msg != "" {
		// read_text(encoding="utf-8") raises UnicodeDecodeError, which is no UsageError (the Python tool crashes)
		return nil, errors.New("UnicodeDecodeError: " + msg)
	}
	al := &ciAllowlist{}
	for n, line := range pyLines(string(raw)) {
		n++
		if strings.TrimFunc(line, pyIsSpace) == "" || strings.HasPrefix(strings.TrimLeftFunc(line, pyIsSpace), "#") {
			continue
		}
		at := strings.LastIndex(line, " # ")
		if at < 0 || strings.TrimFunc(line[at+3:], pyIsSpace) == "" {
			return nil, &usageError{fmt.Sprintf("%s:%d: allowlist line needs a '  # reason' comment. See docs/scrub-scan.md.", path, n)}
		}
		apath, rest, found := strings.Cut(line[:at], ":")
		if !found || strings.TrimFunc(apath, pyIsSpace) == "" || strings.TrimFunc(rest, pyIsSpace) == "" {
			return nil, &usageError{fmt.Sprintf("%s:%d: expected 'path:pattern-id:regex  # reason'. See docs/scrub-scan.md.", path, n)}
		}
		rest = strings.TrimFunc(rest, pyIsSpace)
		pid, rxs, colon := strings.Cut(rest, ":")
		if colon && in(allowPatternIDs, pid) {
			if pid == idOwnerTerm {
				return nil, &usageError{fmt.Sprintf("%s:%d: owner-term hits can never be allowlisted. Remove this line.", path, n)}
			}
			if strings.TrimFunc(rxs, pyIsSpace) == "" {
				return nil, &usageError{fmt.Sprintf("%s:%d: expected 'path:pattern-id:regex  # reason'. See docs/scrub-scan.md.", path, n)}
			}
		} else {
			pid, rxs = "", rest
			if warn != nil {
				if _, werr := fmt.Fprintf(warn, "scrub-scan: warning: %s:%d: legacy 'path:regex' allowlist entry is deprecated; use 'path:pattern-id:regex'. See docs/scrub-scan.md.\n", path, n); werr != nil {
					return nil, fmt.Errorf("write warning: %w", werr)
				}
			}
		}
		src := strings.TrimFunc(rxs, pyIsSpace)
		goSrc, err := pyRegexToRE2(src)
		var re *regexp.Regexp
		if err == nil {
			re, err = regexp.Compile(goSrc)
		}
		if err != nil {
			return nil, &usageError{fmt.Sprintf("%s:%d: bad regex: %s", path, n, err.Error())}
		}
		al.entries = append(al.entries, ciAllowEntry{strings.TrimFunc(apath, pyIsSpace), pid, re, src})
	}
	al.used = make([]bool, len(al.entries))
	return al, nil
}

// allows is Allowlist.allows(names, line, pid): every matching entry is marked used.
func (a *ciAllowlist) allows(names []string, line, pid string) bool {
	if pid == idOwnerTerm {
		return false
	}
	hit := false
	for i, e := range a.entries {
		if !containsStr(names, e.path) || !e.re.MatchString(line) {
			continue
		}
		if e.pid == pid || (e.pid == "" && in(legacyOK, pid)) {
			a.used[i] = true
			hit = true
		}
	}
	return hit
}

// unused is Allowlist.unused: entries that suppressed no hit so far.
func (a *ciAllowlist) unused() []ciAllowEntry {
	var out []ciAllowEntry
	for i, e := range a.entries {
		if !a.used[i] {
			out = append(out, e)
		}
	}
	return out
}

// ciHistoryEntry is the regex of HistoryAllowlist.ENTRY (fullmatch): path:pattern-id:sha256  # reason.
var ciHistoryEntry = regexp.MustCompile(`^(.+?):([a-z0-9-]+):([0-9a-f]{64})[ \t]+#[ \t]*([^` + rxSpace + `].*)$`)

type ciHistKey struct{ path, pid, sha string }

// ciHistoryAllowlist is scrub_scan.HistoryAllowlist (--history-allowlist).
type ciHistoryAllowlist struct {
	keys []ciHistKey
	used map[ciHistKey]bool
}

func ciLoadHistoryAllowlist(path string) (*ciHistoryAllowlist, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &usageError{"cannot read history allowlist " + pyRepr(path) + ": " + pyStrerror(err)}
	}
	h := &ciHistoryAllowlist{used: map[ciHistKey]bool{}}
	for n, line := range strings.Split(string(raw), "\n") {
		n++
		if strings.TrimFunc(line, pyIsSpace) == "" || strings.HasPrefix(strings.TrimLeftFunc(line, pyIsSpace), "#") {
			continue
		}
		m := ciHistoryEntry.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if m == nil {
			why := "expected 'path:pattern-id:sha256  # reason' with a 64-character lowercase hex sha256"
			if !strings.Contains(line, " # ") && !strings.Contains(line, "\t# ") {
				why = "needs a '  # reason' comment"
			}
			return nil, &usageError{fmt.Sprintf("%s:%d: history allowlist line %s. See docs/scrub-scan.md.", path, n, why)}
		}
		pid := m[2]
		if pid == idOwnerTerm {
			return nil, &usageError{fmt.Sprintf("%s:%d: owner-term hits can never be allowlisted. Remove this line.", path, n)}
		}
		if !in(allowPatternIDs, pid) {
			return nil, &usageError{fmt.Sprintf("%s:%d: unknown pattern id %s. Use one of the ids in docs/scrub-scan.md.", path, n, pyRepr(pid))}
		}
		k := ciHistKey{m[1], pid, m[3]}
		if _, dup := h.used[k]; dup {
			return nil, &usageError{fmt.Sprintf("%s:%d: duplicate history allowlist entry. Remove one of them.", path, n)}
		}
		h.keys = append(h.keys, k)
		h.used[k] = false
	}
	return h, nil
}

func (h *ciHistoryAllowlist) allows(path, pid, sha string) bool {
	k := ciHistKey{path, pid, sha}
	if _, ok := h.used[k]; ok {
		h.used[k] = true
		return true
	}
	return false
}

func (h *ciHistoryAllowlist) unused() []ciHistKey {
	var out []ciHistKey
	for _, k := range h.keys {
		if !h.used[k] {
			out = append(out, k)
		}
	}
	return out
}
