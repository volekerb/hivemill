package tick

import (
	"fmt"
	"math/big"
	"strings"
	"unicode"

	"factory/go/internal/store"
)

// The pane-dialog classifier of bin/factory_cli.py (_dialog_block, has_unsent_input, classify_dialog, bottom_region).
// Python's re module matches \s, \d and \w with Unicode rules, and Go's do not (G8), so the three regexes are hand
// scanners over runes: whitespace is store.PyIsSpace, a digit is any Unicode Nd rune (unicode.IsDigit), a word rune is
// '_' or a letter or a number. Lengths are rune counts, as len() of a Python str.

const (
	settingsWarnHead = "SettingsWarning"                                            // SETTINGS_WARN_HEAD
	settingsWarnTail = "Thevalueslistedabovewereskipped;therestofthefileisineffect" // SETTINGS_WARN_TAIL
	permQuestion     = "Doyouwantto"
	bottomLines      = 30  // BOTTOM_LINES (factory_cli.py:17353)
	maxOptions       = 6   // len(opts) > 6 -> None
	maxOptionsSpan   = 400 // sum(len(x) for x in sqs[first:]) > 400 -> None
	maxIntDigits     = 4300
)

// bottomRegion is bottom_region: the last 30 lines of text that are not blank (str.strip() is empty).
func bottomRegion(text string) string {
	var keep []string
	for _, l := range strings.Split(text, "\n") {
		if store.PyStrip(l) != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > bottomLines {
		keep = keep[len(keep)-bottomLines:]
	}
	return strings.Join(keep, "\n")
}

func isBar(r rune) bool { return r == '│' || r == '|' }

func isSpace(r rune) bool { return store.PyIsSpace(r) }

func isPrompt(r rune) bool { return r == '>' || r == '❯' }

// optParse is _OPT_RE ^[│|\s]*[>❯]?\s*(\d+)\.(?:\s|$): the digits of the number and whether the line matches. Backtracking
// cannot change the result: the leading class and the optional marker are disjoint from digits.
func optParse(line string) (digits string, ok bool) {
	rs := []rune(line)
	i := 0
	for i < len(rs) && (isBar(rs[i]) || isSpace(rs[i])) {
		i++
	}
	if i < len(rs) && isPrompt(rs[i]) {
		i++
	}
	for i < len(rs) && isSpace(rs[i]) {
		i++
	}
	s := i
	for i < len(rs) && unicode.IsDigit(rs[i]) {
		i++
	}
	if i == s || i >= len(rs) || rs[i] != '.' {
		return "", false
	}
	if i+1 < len(rs) && !isSpace(rs[i+1]) {
		return "", false
	}
	return string(rs[s:i]), true
}

// digitValue is the decimal value of a Unicode Nd rune: the offset in its contiguous run of ten.
func digitValue(r rune) int {
	v := 0
	for c := r - 1; unicode.IsDigit(c) && v < 9; c-- {
		v++
	}
	return v
}

// optInt is int(group): Python reads any Nd digits, and refuses more than 4300 of them.
func optInt(digits string) (*big.Int, error) {
	rs := []rune(digits)
	if len(rs) > maxIntDigits {
		return nil, fmt.Errorf("Exceeds the limit (%d digits) for integer string conversion: value has %d digits; use sys.set_int_max_str_digits() to increase the limit",
			maxIntDigits, len(rs))
	}
	var sb strings.Builder
	for _, r := range rs {
		sb.WriteByte(byte('0' + digitValue(r)))
	}
	n, ok := new(big.Int).SetString(sb.String(), 10)
	if !ok {
		return nil, fmt.Errorf("invalid literal for int() with base 10: %q", digits)
	}
	return n, nil
}

// promptMatch is _PROMPT_RE ^\s*[│|]?\s*[>❯]\s*(.*?)\s*[│|]?\s*$ : ok when the line is a prompt line, group is the text
// after the marker (only its truthiness is used, but the text is returned for the tests).
func promptMatch(line string) (group string, ok bool) {
	rs := []rune(line)
	i := 0
	for i < len(rs) && isSpace(rs[i]) {
		i++
	}
	if i < len(rs) && isBar(rs[i]) {
		i++
	}
	for i < len(rs) && isSpace(rs[i]) {
		i++
	}
	if i >= len(rs) || !isPrompt(rs[i]) {
		return "", false
	}
	i++
	rest := rs[i:]
	for len(rest) > 0 && isSpace(rest[0]) {
		rest = rest[1:]
	}
	n := len(rest)
	for n > 0 && isSpace(rest[n-1]) {
		n--
	}
	rest = rest[:n]
	if n > 0 && isBar(rest[n-1]) {
		rest = rest[:n-1]
		n = len(rest)
		for n > 0 && isSpace(rest[n-1]) {
			n--
		}
		rest = rest[:n]
	}
	return string(rest), true
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// foldEq is Python's case-insensitive rune equality for the ASCII letters of the footer words: 'i' also matches U+0130
// and U+0131, 's' U+017F, 'k' U+212A (the Unicode simple case folds re.IGNORECASE honours).
func foldRune(r rune) rune {
	switch r {
	case 0x130, 0x131:
		return 'i'
	case 0x17F:
		return 's'
	case 0x212A:
		return 'k'
	}
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

var (
	footerFirst  = []string{"enter", "esc"}
	footerSecond = []string{"confirm", "cancel", "select", "exit", "continue", "go back"}
)

// wordAt reports the end offsets of every occurrence of one of words at rs[start:] with \b on both sides.
func wordEnds(rs []rune, words []string, from int, firstOnly bool) (ends []int) {
	for s := from; s < len(rs); s++ {
		if s > 0 && isWordRune(rs[s-1]) == isWordRune(rs[s]) {
			continue // no \b before s
		}
		if !isWordRune(rs[s]) {
			continue
		}
		for _, w := range words {
			wr := []rune(w)
			if s+len(wr) > len(rs) {
				continue
			}
			match := true
			for k, c := range wr {
				if foldRune(rs[s+k]) != c {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			e := s + len(wr)
			if e < len(rs) && isWordRune(rs[e]) {
				continue // no \b after the word
			}
			ends = append(ends, e)
			if firstOnly {
				return ends
			}
		}
	}
	return ends
}

// footerSearch is _FOOTER_RE.search: (?i)\b(enter|esc)\b.*\b(confirm|cancel|select|exit|continue|go back)\b. The dot
// does not cross a newline (a line never holds one here, but a lone \n in a fuzzed input keeps Python's answer).
func footerSearch(line string) bool {
	for _, seg := range strings.Split(line, "\n") {
		rs := []rune(seg)
		firsts := wordEnds(rs, footerFirst, 0, true)
		if len(firsts) == 0 {
			continue
		}
		if len(wordEnds(rs, footerSecond, firsts[0], true)) > 0 {
			return true
		}
	}
	return false
}

// squash is _squash: every whitespace rune removed.
func squash(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if !isSpace(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// bottomLinesOf is _bottom_lines: the bottom region's lines; an option line is only right-stripped, any other line has
// its leading and trailing runs of whitespace and box bars removed.
func bottomLinesOf(text string) []string {
	var out []string
	for _, l := range strings.Split(bottomRegion(text), "\n") {
		if _, ok := optParse(l); ok {
			out = append(out, strings.TrimRightFunc(l, isSpace))
		} else {
			out = append(out, strings.TrimFunc(l, func(r rune) bool { return isSpace(r) || isBar(r) }))
		}
	}
	return out
}

func runeLen(s string) int { return len([]rune(s)) }

// block is the tuple _dialog_block returns.
type block struct {
	kind    string // "permission" or "settings-warning"
	line    int    // li (permission) or line_of(mark_start) (settings warning)
	opts    map[int]bool
	body    string
	file    string
	warning string
}

// runeIndex is str.index of sub in s as a rune offset, -1 when absent.
func runeIndex(s, sub string) int {
	i := strings.Index(s, sub)
	if i < 0 {
		return -1
	}
	return runeLen(s[:i])
}

func runeSlice(rs []rune, a, b int) string {
	if a < 0 {
		a = 0
	}
	if b > len(rs) {
		b = len(rs)
	}
	if a >= b {
		return ""
	}
	return string(rs[a:b])
}

// jsonFile is re.search(r"[~/][^:]*?\.json", body).group(0), "" when there is none.
func jsonFile(body string) string {
	rs := []rune(body)
	for s := 0; s < len(rs); s++ {
		if rs[s] != '~' && rs[s] != '/' {
			continue
		}
		for k := s + 1; k <= len(rs); k++ {
			if k+5 <= len(rs) && string(rs[k:k+5]) == ".json" {
				return string(rs[s : k+5])
			}
			if k >= len(rs) || rs[k] == ':' {
				break
			}
		}
	}
	return ""
}

// dialogBlock is _dialog_block. A nil block with a nil error is Python's None; an error is the ValueError int() raises on
// a number of more than 4300 digits (auto_clear_dialog catches it).
func dialogBlock(text string) (*block, error) {
	lines := bottomLinesOf(text)
	sqs := make([]string, len(lines))
	starts := make([]int, len(lines))
	var sqb strings.Builder
	off := 0
	for i, l := range lines {
		sqs[i] = squash(l)
		starts[i] = off
		off += runeLen(sqs[i])
		sqb.WriteString(sqs[i])
	}
	sq := sqb.String()
	sqr := []rune(sq)
	lineOf := func(o int) int {
		li := 0
		for i, s := range starts {
			if s <= o {
				li = i
			}
		}
		return li
	}
	nHead, nTail := strings.Count(sq, settingsWarnHead), strings.Count(sq, settingsWarnTail)
	warn, perm := nHead+nTail, strings.Count(sq, permQuestion)
	var kind string
	var markStart, markEnd, maxLead, h, t int
	switch {
	case warn == 2 && perm == 0 && nHead == 1 && nTail == 1:
		kind = "settings-warning"
		h, t = runeIndex(sq, settingsWarnHead), runeIndex(sq, settingsWarnTail)
		if h > t {
			return nil, nil
		}
		markEnd, markStart, maxLead = t+runeLen(settingsWarnTail), h, 1
	case perm == 1 && warn == 0:
		kind = "permission"
		markStart = runeIndex(sq, permQuestion)
		markEnd, maxLead = markStart+runeLen(permQuestion), 80
	default:
		return nil, nil
	}
	li := lineOf(markEnd - 1)
	first := -1
	for i := li + 1; i < len(lines); i++ {
		if d, ok := optParse(lines[i]); ok && d == "1" {
			first = i
			break
		}
	}
	if first < 0 {
		return nil, nil
	}
	lead := ""
	if markEnd <= starts[first] {
		lead = strings.TrimRight(runeSlice(sqr, markEnd, starts[first]), "·")
	}
	if runeLen(lead) > maxLead || (kind == "settings-warning" && lead != "" && lead != ".") ||
		(kind == "permission" && !strings.HasSuffix(lead, "?")) {
		return nil, nil
	}
	for i := li + 1; i < first; i++ {
		if _, ok := promptMatch(lines[i]); ok {
			return nil, nil
		}
	}
	opts := map[int]bool{}
	nOpts, want, cont, footer := 0, int64(1), 0, false
	for i := first; i < len(lines); i++ {
		l := lines[i]
		d, isOpt := optParse(l)
		if footer {
			if !footerSearch(l) {
				return nil, nil
			}
			continue
		}
		if isOpt {
			n, err := optInt(d)
			if err != nil {
				return nil, err
			}
			if !n.IsInt64() || n.Int64() != want {
				return nil, nil
			}
			opts[i] = true
			nOpts++
			want, cont = want+1, 0
			continue
		}
		if footerSearch(l) && runeLen(l) < 100 {
			footer = true
			continue
		}
		if _, isPrompt := promptMatch(l); isPrompt || cont >= 3 || runeLen(l) > 80 {
			return nil, nil
		}
		cont++
	}
	span := 0
	for _, x := range sqs[first:] {
		span += runeLen(x)
	}
	if nOpts > maxOptions || span > maxOptionsSpan {
		return nil, nil
	}
	if kind == "permission" {
		blk := strings.Join(sqs[first:], "")
		if !strings.Contains(blk, "1.Yes") || !strings.Contains(blk, "No") {
			return nil, nil
		}
		return &block{kind: "permission", line: li, opts: opts}, nil
	}
	body := runeSlice(sqr, markStart+runeLen(settingsWarnHead), t)
	file := jsonFile(body)
	if file == "" {
		file = "?"
	}
	w := strings.Join(strings.FieldsFunc(strings.Join(lines, " "), isSpace), " ")
	if p := strings.SplitN(w, "Settings Warning", 2); len(p) == 2 {
		w = p[1]
	}
	w = strings.SplitN(w, "The values listed above", 2)[0]
	w = truncRunes(store.PyStrip(w), 500)
	return &block{kind: "settings-warning", line: lineOf(markStart), opts: opts, body: body, file: file, warning: w}, nil
}

// hasUnsentInput is has_unsent_input.
func hasUnsentInput(text string) (bool, error) {
	blk, err := dialogBlock(text)
	if err != nil {
		return false, err
	}
	exempt := map[int]bool{}
	if blk != nil {
		exempt = blk.opts
	}
	lines := bottomLinesOf(text)
	for i, l := range lines {
		g, ok := promptMatch(l)
		if !ok || exempt[i] {
			continue
		}
		if g != "" {
			return true, nil
		}
		later := false
		for j := i + 1; j < len(lines); j++ {
			if !exempt[j] {
				later = true
				break
			}
		}
		if later && (blk == nil || i < blk.line) {
			return true, nil
		}
	}
	return false, nil
}

// dialogClass is classify_dialog's result: kind "unknown", "permission" or "settings-warning" (with key, file, warning).
type dialogClass struct{ kind, key, file, warning string }

// classifyDialog is classify_dialog.
func classifyDialog(text string) (dialogClass, error) {
	b, err := dialogBlock(text)
	if err != nil {
		return dialogClass{}, err
	}
	switch {
	case b == nil:
		return dialogClass{kind: "unknown"}, nil
	case b.kind == "permission":
		return dialogClass{kind: "permission"}, nil
	}
	return dialogClass{kind: "settings-warning", key: b.body, file: b.file, warning: b.warning}, nil
}
