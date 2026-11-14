package affected

import (
	"fmt"
	"sort"
	"strings"
)

// This file is a small Python source reader, enough for what ci/affected.py gets from ast: the top-level statements of
// a module (name, first line, last line, text) and the number of calls f(...) by bare name. It lexes strings, f-string
// expressions, comments, brackets and line continuations; it does not check the grammar, so it reports a parse error
// only for what a lexer can see (unterminated string, unbalanced bracket). The Python ast also rejects bad indentation
// and other grammar errors; the judge's full gate is the backstop, as for ci/affected.py itself.

// ParseError is a lexing failure; the caller treats the file as "does not parse" (FULL / risky).
type ParseError struct{ Msg string }

func (e *ParseError) Error() string { return e.Msg }

type tokKind int

const (
	tkName tokKind = iota
	tkOp
	tkString
	tkNumber
)

type token struct {
	kind    tokKind
	text    string
	line    int // 1-based first line
	endLine int
	col     int  // 0-based column of the first byte on its line
	depth   int  // bracket depth before this token
	bol     bool // first token of a logical line
}

var pyKeywords = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true, "assert": true, "async": true, "await": true,
	"break": true, "class": true, "continue": true, "def": true, "del": true, "elif": true, "else": true, "except": true,
	"finally": true, "for": true, "from": true, "global": true, "if": true, "import": true, "in": true, "is": true,
	"lambda": true, "nonlocal": true, "not": true, "or": true, "pass": true, "raise": true, "return": true, "try": true,
	"while": true, "with": true, "yield": true,
}

var compoundStart = map[string]bool{
	"if": true, "for": true, "while": true, "try": true, "with": true, "def": true, "class": true, "async": true,
	"else": true, "elif": true, "except": true, "finally": true,
}

// continuation clauses that belong to the compound statement above them when they start a column-0 line
var clauseKeyword = map[string]bool{"else": true, "elif": true, "except": true, "finally": true}

var stringPrefixes = map[string]bool{
	"r": true, "u": true, "b": true, "f": true, "br": true, "rb": true, "fr": true, "rf": true,
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}
func isIdentChar(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }
func isWordByte(c byte) bool  { return isIdentChar(c) && c < 0x80 }

var ops3 = []string{"**=", "//=", ">>=", "<<=", "..."}
var ops2 = []string{"==", "!=", "<=", ">=", "+=", "-=", "*=", "/=", "%=", "@=", "&=", "|=", "^=", ":=", "->", "**", "//", ">>", "<<"}

// lexer state for one source text.
type lexer struct {
	src   string
	pos   int
	line  int
	lineS int // byte offset of the current line start
	depth int
	toks  []token
	bol   bool // next token starts a logical line
}

func lexSource(src string) ([]token, error) {
	toks, _, err := lexFull(src, 1)
	return toks, err
}

// lexFull tokenises src; line0 is the line number of its first line. It also returns the f-string expression token
// streams (already flattened) so call counting sees them.
func lexFull(src string, line0 int) ([]token, [][]token, error) {
	lx := &lexer{src: src, line: line0, bol: true}
	var fexprs [][]token
	if err := lx.run(&fexprs); err != nil {
		return nil, nil, err
	}
	return lx.toks, fexprs, nil
}

func (lx *lexer) run(fexprs *[][]token) error {
	src := lx.src
	for lx.pos < len(src) {
		c := src[lx.pos]
		switch {
		case c == '\n':
			lx.pos++
			lx.line++
			lx.lineS = lx.pos
			lx.bol = lx.depth == 0
		case c == '\r':
			lx.pos++
			if lx.pos < len(src) && src[lx.pos] == '\n' {
				lx.pos++
			}
			lx.line++
			lx.lineS = lx.pos
			lx.bol = lx.depth == 0
		case c == ' ' || c == '\t' || c == '\f':
			lx.pos++
		case c == '#':
			for lx.pos < len(src) && src[lx.pos] != '\n' && src[lx.pos] != '\r' {
				lx.pos++
			}
		case c == '\\':
			// line continuation: backslash + newline
			lx.pos++
			if lx.pos < len(src) && src[lx.pos] == '\r' {
				lx.pos++
				if lx.pos < len(src) && src[lx.pos] == '\n' {
					lx.pos++
				}
				lx.line++
				lx.lineS = lx.pos
			} else if lx.pos < len(src) && src[lx.pos] == '\n' {
				lx.pos++
				lx.line++
				lx.lineS = lx.pos
			} else if lx.pos < len(src) {
				return &ParseError{fmt.Sprintf("unexpected character after line continuation character (line %d)", lx.line)}
			}
		case c == '"' || c == '\'':
			if err := lx.str(lx.pos, "", fexprs); err != nil {
				return err
			}
		case isIdentStart(c):
			start := lx.pos
			for lx.pos < len(src) && isIdentChar(src[lx.pos]) {
				lx.pos++
			}
			word := src[start:lx.pos]
			if lx.pos < len(src) && (src[lx.pos] == '"' || src[lx.pos] == '\'') && stringPrefixes[strings.ToLower(word)] {
				if err := lx.str(start, strings.ToLower(word), fexprs); err != nil {
					return err
				}
				continue
			}
			lx.emit(tkName, word, start, lx.line)
		case c >= '0' && c <= '9' || (c == '.' && lx.pos+1 < len(src) && src[lx.pos+1] >= '0' && src[lx.pos+1] <= '9'):
			start := lx.pos
			lx.pos++
			for lx.pos < len(src) {
				d := src[lx.pos]
				if isIdentChar(d) || d == '.' {
					lx.pos++
				} else if (d == '+' || d == '-') && (src[lx.pos-1] == 'e' || src[lx.pos-1] == 'E') && !strings.HasPrefix(strings.ToLower(src[start:]), "0x") {
					lx.pos++
				} else {
					break
				}
			}
			lx.emit(tkNumber, src[start:lx.pos], start, lx.line)
		default:
			start := lx.pos
			op := string(c)
			for _, o := range ops3 {
				if strings.HasPrefix(src[lx.pos:], o) {
					op = o
					break
				}
			}
			if len(op) == 1 {
				for _, o := range ops2 {
					if strings.HasPrefix(src[lx.pos:], o) {
						op = o
						break
					}
				}
			}
			lx.pos += len(op)
			depth := lx.depth
			switch op {
			case "(", "[", "{":
				lx.depth++
			case ")", "]", "}":
				if lx.depth == 0 {
					return &ParseError{fmt.Sprintf("unmatched '%s' (line %d)", op, lx.line)}
				}
				lx.depth--
				depth = lx.depth
			}
			lx.toks = append(lx.toks, token{kind: tkOp, text: op, line: lx.line, endLine: lx.line, col: start - lx.lineS, depth: depth, bol: lx.bol})
			lx.bol = false
		}
	}
	if lx.depth > 0 {
		return &ParseError{"'(' was never closed"}
	}
	return nil
}

func (lx *lexer) emit(k tokKind, text string, start, endLine int) {
	lx.toks = append(lx.toks, token{kind: k, text: text, line: lx.line, endLine: endLine, col: start - lx.lineS, depth: lx.depth, bol: lx.bol})
	lx.bol = false
}

// str lexes a string literal whose first byte (prefix included) is at start and whose quote is at lx.pos.
func (lx *lexer) str(start int, prefix string, fexprs *[][]token) error {
	src := lx.src
	startLine := lx.line
	col := start - lx.lineS
	q := src[lx.pos]
	triple := strings.HasPrefix(src[lx.pos:], strings.Repeat(string(q), 3))
	if triple {
		lx.pos += 3
	} else {
		lx.pos++
	}
	bodyStart := lx.pos
	closed := false
	for lx.pos < len(src) {
		c := src[lx.pos]
		if c == '\\' {
			lx.pos++
			if lx.pos < len(src) {
				if src[lx.pos] == '\r' && lx.pos+1 < len(src) && src[lx.pos+1] == '\n' {
					lx.pos++
				}
				if src[lx.pos] == '\n' || src[lx.pos] == '\r' {
					lx.line++
					lx.lineS = lx.pos + 1
				}
				lx.pos++
			}
			continue
		}
		if c == q {
			if !triple {
				closed = true
				break
			}
			if strings.HasPrefix(src[lx.pos:], strings.Repeat(string(q), 3)) {
				closed = true
				break
			}
		}
		if c == '\n' || c == '\r' {
			if !triple {
				return &ParseError{fmt.Sprintf("unterminated string literal (line %d)", startLine)}
			}
			if c == '\r' && lx.pos+1 < len(src) && src[lx.pos+1] == '\n' {
				lx.pos++
			}
			lx.line++
			lx.lineS = lx.pos + 1
		}
		lx.pos++
	}
	if !closed {
		return &ParseError{fmt.Sprintf("unterminated string literal (line %d)", startLine)}
	}
	body := src[bodyStart:lx.pos]
	if triple {
		lx.pos += 3
	} else {
		lx.pos++
	}
	lx.toks = append(lx.toks, token{kind: tkString, text: src[start:lx.pos], line: startLine, endLine: lx.line, col: col, depth: lx.depth, bol: lx.bol})
	lx.bol = false
	if strings.Contains(prefix, "f") {
		for _, expr := range fstringExprs(body) {
			toks, more, err := lexFull(expr, startLine)
			if err != nil {
				// An expression the lexer cannot read (format-spec text, odd nesting) is skipped: it can only
				// lower a call-site count, never raise it.
				continue
			}
			*fexprs = append(*fexprs, toks)
			*fexprs = append(*fexprs, more...)
		}
	}
	return nil
}

// fstringExprs returns the replacement-field expression texts of an f-string body (text between the quotes).
func fstringExprs(body string) []string {
	var out []string
	i := 0
	for i < len(body) {
		c := body[i]
		if c == '{' {
			if i+1 < len(body) && body[i+1] == '{' {
				i += 2
				continue
			}
			depth := 1
			j := i + 1
			var quote byte
			for j < len(body) && depth > 0 {
				d := body[j]
				switch {
				case quote != 0:
					if d == quote {
						quote = 0
					}
				case d == '"' || d == '\'':
					quote = d
				case d == '{' || d == '(' || d == '[':
					depth++
				case d == '}' || d == ')' || d == ']':
					depth--
				}
				j++
			}
			if depth == 0 {
				out = append(out, body[i+1:j-1])
			}
			i = j
			continue
		}
		i++
	}
	return out
}

// Unit is one top-level statement of a Python module.
type Unit struct {
	Start, End int
	Text       string
}

// PySplitLines is Python's str.splitlines(): it also splits on \v, \f, \x1c-\x1e, U+0085, U+2028 and U+2029, not
// only on \n, \r and \r\n. ci/affected.py slices unit text with it while ast counts lines by \n, \r and \r\n only,
// so a file holding such a character in a string yields shifted unit text; the port reproduces that.
func PySplitLines(src string) []string {
	var out []string
	start := 0
	i := 0
	for i < len(src) {
		sep := 0
		switch c := src[i]; {
		case c == '\n' || c == '\v' || c == '\f' || c == 0x1c || c == 0x1d || c == 0x1e:
			sep = 1
		case c == '\r':
			sep = 1
			if i+1 < len(src) && src[i+1] == '\n' {
				sep = 2
			}
		case c == 0xc2 && i+1 < len(src) && src[i+1] == 0x85:
			sep = 2
		case c == 0xe2 && i+2 < len(src) && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9):
			sep = 3
		}
		if sep > 0 {
			out = append(out, src[start:i])
			i += sep
			start = i
			continue
		}
		i++
	}
	if start < len(src) {
		out = append(out, src[start:])
	}
	return out
}

type logical struct{ toks []token }

// logicalLines groups tokens into logical lines (a line outside brackets and not joined by a backslash).
func logicalLines(toks []token) []logical {
	var out []logical
	for i, t := range toks {
		if i == 0 || t.bol {
			out = append(out, logical{})
		}
		out[len(out)-1].toks = append(out[len(out)-1].toks, t)
	}
	return out
}

func nameOfUnit(l []token) string {
	i := 0
	if l[i].kind == tkName && l[i].text == "async" && len(l) > 1 {
		i++
	}
	if l[i].kind == tkName && (l[i].text == "def" || l[i].text == "class") && i+1 < len(l) && l[i+1].kind == tkName {
		return l[i+1].text
	}
	if l[0].kind != tkName || pyKeywords[l[0].text] {
		return ""
	}
	if len(l) > 1 && l[1].kind == tkOp && l[1].text == ":" {
		return l[0].text // AnnAssign with a Name target
	}
	// Assign: every target a bare Name; stop at lambda (its defaults hold '=')
	var targets [][]token
	cur := []token{}
	sawEq := false
	for _, t := range l {
		if t.kind == tkName && t.text == "lambda" && t.depth == 0 {
			break
		}
		if t.kind == tkOp && t.text == "=" && t.depth == 0 {
			targets = append(targets, cur)
			cur = []token{}
			sawEq = true
			continue
		}
		cur = append(cur, t)
	}
	if !sawEq {
		return ""
	}
	for _, tg := range targets {
		if len(tg) != 1 || tg[0].kind != tkName || pyKeywords[tg[0].text] {
			return ""
		}
	}
	return targets[0][0].text
}

// splitSemicolons splits a simple-statement logical line at depth-0 ';'.
func splitSemicolons(l []token) [][]token {
	if l[0].kind == tkName && compoundStart[l[0].text] {
		return [][]token{l}
	}
	var out [][]token
	cur := []token{}
	for _, t := range l {
		if t.kind == tkOp && t.text == ";" && t.depth == 0 {
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = []token{}
			continue
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

type rawUnit struct {
	name       string
	start, end int
	pieces     [][]token
}

// TopUnits returns {unit name: Unit} for each top-level statement; unnamed ones are keyed "<stmt@N>" and a name
// defined twice gets a "'" appended for the later one, like ci/affected.py top_units.
func TopUnits(src string) (map[string]Unit, error) {
	toks, err := lexSource(src)
	if err != nil {
		return nil, err
	}
	lines := PySplitLines(src)
	units := map[string]Unit{}
	if len(toks) == 0 {
		return units, nil
	}
	ll := logicalLines(toks)
	if ll[0].toks[0].col != 0 {
		return nil, &ParseError{"unexpected indent"}
	}
	var stmts []rawUnit
	pendingDecorator := 0
	for _, l := range ll {
		first := l.toks[0]
		lastTok := l.toks[len(l.toks)-1]
		end := lastTok.endLine
		if first.col > 0 {
			// body of the compound statement above
			if len(stmts) == 0 {
				return nil, &ParseError{"unexpected indent"}
			}
			stmts[len(stmts)-1].end = end
			continue
		}
		if first.kind == tkName && clauseKeyword[first.text] && len(stmts) > 0 {
			stmts[len(stmts)-1].end = end
			continue
		}
		if first.kind == tkOp && first.text == "@" {
			if pendingDecorator == 0 {
				pendingDecorator = first.line
			}
			continue
		}
		start := first.line
		if pendingDecorator > 0 {
			start = pendingDecorator
			pendingDecorator = 0
		}
		stmts = append(stmts, rawUnit{start: start, end: end, pieces: splitSemicolons(l.toks)})
	}
	for _, s := range stmts {
		for _, piece := range s.pieces {
			name := nameOfUnit(piece)
			if name == "" {
				name = fmt.Sprintf("<stmt@%d>", piece[0].line)
			}
			end := s.end
			if len(s.pieces) > 1 {
				end = piece[len(piece)-1].endLine
				if end < s.end {
					end = s.end
				}
			}
			key := name
			for {
				if _, dup := units[key]; !dup {
					break
				}
				key += "'"
			}
			text := ""
			if s.start >= 1 && s.start <= len(lines) {
				hi := end
				if hi > len(lines) {
					hi = len(lines)
				}
				text = strings.Join(lines[s.start-1:hi], "\n")
			}
			units[key] = Unit{Start: s.start, End: end, Text: text}
		}
	}
	return units, nil
}

// ChangedDefs returns the names of top-level units whose source differs (added, removed or edited) between the
// versions; unnamed statements collapse into "<stmt>" when their multiset of texts differs. An empty source counts
// as no units. A source that does not lex returns a *ParseError.
func ChangedDefs(baseSrc, headSrc string) (map[string]bool, error) {
	var a, b map[string]Unit
	var err error
	if baseSrc != "" {
		if a, err = TopUnits(baseSrc); err != nil {
			return nil, err
		}
	}
	if headSrc != "" {
		if b, err = TopUnits(headSrc); err != nil {
			return nil, err
		}
	}
	names := map[string]bool{}
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	for k := range seen {
		if strings.HasPrefix(k, "<stmt@") {
			continue
		}
		ua, okA := a[k]
		ub, okB := b[k]
		if !okA || !okB || ua.Text != ub.Text {
			names[k] = true
		}
	}
	if !sameStmtTexts(a, b) {
		names["<stmt>"] = true
	}
	return names, nil
}

func sameStmtTexts(a, b map[string]Unit) bool {
	collect := func(m map[string]Unit) []string {
		var out []string
		for k, u := range m {
			if strings.HasPrefix(k, "<stmt@") {
				out = append(out, u.Text)
			}
		}
		sort.Strings(out)
		return out
	}
	ua, ub := collect(a), collect(b)
	if len(ua) != len(ub) {
		return false
	}
	for i := range ua {
		if ua[i] != ub[i] {
			return false
		}
	}
	return true
}

// CallSites counts calls f(...) by bare name over the whole module (f-string expressions included).
func CallSites(src string) (map[string]int, error) {
	toks, fexprs, err := lexFull(src, 1)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	count := func(ts []token) {
		for i := 0; i+1 < len(ts); i++ {
			t := ts[i]
			if t.kind != tkName || pyKeywords[t.text] {
				continue
			}
			if nx := ts[i+1]; nx.kind != tkOp || nx.text != "(" || nx.bol {
				continue
			}
			if i > 0 {
				p := ts[i-1]
				if p.kind == tkOp && p.text == "." {
					continue
				}
				if p.kind == tkName && (p.text == "def" || p.text == "class") {
					continue
				}
			}
			counts[t.text]++
		}
	}
	count(toks)
	for _, e := range fexprs {
		count(e)
	}
	return counts, nil
}
