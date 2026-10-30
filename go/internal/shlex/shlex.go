// Package shlex is a byte-exact port of three CPython shlex behaviours the
// worker-guard hook depends on:
//
//   - Lex     shlex.shlex(text, posix=True, punctuation_chars=True) with the
//     options the hook sets (worker-guard.py Analyzer.script:
//     whitespace_split=True, commenters="") or the library defaults
//   - Split   shlex.split(s) (posix, comments=False)
//   - Quote   shlex.quote(s)
//
// Reference: CPython 3.13 Lib/shlex.py, the version CI runs
// (.github/workflows/tests.yml python-version "3.13"). Ported regions of that
// file: shlex.__init__ (the wordchars, whitespace, quotes, escape and
// punctuation_chars setup, lines 18-62 of the 3.13 source), shlex.read_token
// (lines 126-282), shlex.__next__/get_token reduced to the posix, no-source,
// no-pushback case (lines 105-125, 300-304), split (lines 307-315), and
// quote with _find_unsafe (lines 323-332). Not ported because the hook never
// uses them: non-posix mode, push_token, push_source/sourcehook, debug and
// lineno tracking, join.
//
// Input is decoded to code points like a Python str; an invalid UTF-8 byte
// becomes U+FFFD (Python has no such input, so there is nothing to match).
package shlex

import (
	"errors"
	"strings"
)

// The two errors CPython raises as ValueError; the text is its message.
var (
	ErrNoClosingQuotation = errors.New("No closing quotation")
	ErrNoEscapedCharacter = errors.New("No escaped character")
	// ErrUnsupportedOptions: no punctuation_chars and no whitespace_split makes
	// CPython push a lone character back as a token; nothing here needs that.
	ErrUnsupportedOptions = errors.New("shlex: options need punctuation_chars or whitespace_split")
)

// DefaultPunctuationChars is what punctuation_chars=True stands for.
const DefaultPunctuationChars = "();<>|&"

const (
	// posix wordchars: 'abcdfeghijk...' as spelled in shlex.__init__ plus the
	// Latin-1 letters added in posix mode (the set is CPython's, not unicode.IsLetter).
	baseWordchars = "abcdfeghijklmnopqrstuvwxyz" + "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_" +
		"ßàáâãäåæçèéêëìíîïðñòóôõöøùúûüýþÿ" + "ÀÁÂÃÄÅÆÇÈÉÊËÌÍÎÏÐÑÒÓÔÕÖØÙÚÛÜÝÞ"
	// added when punctuation_chars is set (before the punctuation chars are removed).
	punctWordchars = "~-./*?="
	whitespace     = " \t\r\n"
	quotes         = `'"`
	escape         = `\`
	escapedQuotes  = `"`
)

// Options mirror the attributes of a posix shlex object that the hook sets.
type Options struct {
	// PunctuationChars is shlex's punctuation_chars ("" = off).
	PunctuationChars string
	// WhitespaceSplit is shlex.whitespace_split.
	WhitespaceSplit bool
	// Commenters is shlex.commenters ('#' by default; the hook and split use "").
	Commenters string
}

// DefaultOptions is shlex.shlex(text, posix=True, punctuation_chars=True) untouched.
func DefaultOptions() Options {
	return Options{PunctuationChars: DefaultPunctuationChars, Commenters: "#"}
}

// ScriptOptions is the configuration at worker-guard.py Analyzer.script.
func ScriptOptions() Options {
	return Options{PunctuationChars: DefaultPunctuationChars, WhitespaceSplit: true}
}

// Lex returns every token of text, or the error CPython would raise while
// iterating the lexer. Tokens produced before an error are discarded, as
// list(lex) does.
func Lex(text string, o Options) ([]string, error) {
	if o.PunctuationChars == "" && !o.WhitespaceSplit {
		return nil, ErrUnsupportedOptions
	}
	l := newLexer(text, o)
	toks := []string{}
	for {
		tok, ok, err := l.readToken()
		if err != nil {
			return nil, err
		}
		if !ok {
			return toks, nil
		}
		toks = append(toks, tok)
	}
}

// Script is Lex with ScriptOptions.
func Script(text string) ([]string, error) { return Lex(text, ScriptOptions()) }

// Split is shlex.split(s): posix, whitespace_split, no comments, no punctuation_chars.
func Split(s string) ([]string, error) { return Lex(s, Options{WhitespaceSplit: true}) }

// Quote is shlex.quote(s).
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		if !safeByte(s[i]) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// safeByte is the complement of re.compile(r'[^\w@%+=:,./-]', re.ASCII).
func safeByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("_@%+=:,./-", c) >= 0
}

const (
	eof     rune = -1
	stNone  rune = -2 // state None: past end of file
	stSpace rune = ' '
	stWord  rune = 'a'
	stPunct rune = 'c'
)

type lexer struct {
	in        []rune
	pos       int
	pushback  []rune // _pushback_chars, popped from the end
	opt       Options
	wordchars string
	state     rune
}

func newLexer(text string, o Options) *lexer {
	w := baseWordchars
	if o.PunctuationChars != "" {
		w += punctWordchars
		w = strings.Map(func(r rune) rune {
			if strings.ContainsRune(o.PunctuationChars, r) {
				return -1
			}
			return r
		}, w)
	}
	return &lexer{in: []rune(text), opt: o, wordchars: w, state: stSpace}
}

func (l *lexer) next() rune {
	if l.opt.PunctuationChars != "" && len(l.pushback) > 0 {
		c := l.pushback[len(l.pushback)-1]
		l.pushback = l.pushback[:len(l.pushback)-1]
		return c
	}
	if l.pos >= len(l.in) {
		return eof
	}
	c := l.in[l.pos]
	l.pos++
	return c
}

// readline discards input through the next newline (instream.readline()).
func (l *lexer) readline() {
	for l.pos < len(l.in) {
		c := l.in[l.pos]
		l.pos++
		if c == '\n' {
			return
		}
	}
}

func in(c rune, set string) bool { return c != eof && strings.ContainsRune(set, c) }

// readToken is shlex.read_token for posix mode; ok=false means end of input
// (CPython returns None, which iteration treats as EOF).
func (l *lexer) readToken() (tok string, ok bool, err error) {
	var token []rune
	quoted := false
	escapedstate := stSpace
	// emit mirrors "if self.token or (self.posix and quoted): break / else continue".
	emit := func() bool { return len(token) > 0 || quoted }
loop:
	for {
		c := l.next()
		switch {
		case l.state == stNone:
			token = nil // past end of file
			break loop
		case l.state == stSpace:
			switch {
			case c == eof:
				l.state = stNone
				break loop
			case in(c, whitespace):
				if emit() {
					break loop
				}
			case in(c, l.opt.Commenters):
				l.readline()
			case in(c, escape):
				escapedstate = stWord
				l.state = c
			case in(c, l.wordchars):
				token = []rune{c}
				l.state = stWord
			case in(c, l.opt.PunctuationChars):
				token = []rune{c}
				l.state = stPunct
			case in(c, quotes):
				l.state = c
			case l.opt.WhitespaceSplit:
				token = []rune{c}
				l.state = stWord
			default:
				token = []rune{c}
				break loop
			}
		case in(l.state, quotes):
			quoted = true
			switch {
			case c == eof:
				return "", false, ErrNoClosingQuotation
			case c == l.state:
				l.state = stWord
			case in(c, escape) && in(l.state, escapedQuotes):
				escapedstate = l.state
				l.state = c
			default:
				token = append(token, c)
			}
		case in(l.state, escape):
			if c == eof {
				return "", false, ErrNoEscapedCharacter
			}
			// Inside quotes only the quote itself or the escape may be escaped.
			if in(escapedstate, quotes) && c != l.state && c != escapedstate {
				token = append(token, l.state)
			}
			token = append(token, c)
			l.state = escapedstate
		default: // stWord or stPunct
			switch {
			case c == eof:
				l.state = stNone
				break loop
			case in(c, whitespace):
				l.state = stSpace
				if emit() {
					break loop
				}
			case in(c, l.opt.Commenters):
				l.readline()
				l.state = stSpace
				if emit() {
					break loop
				}
			case l.state == stPunct:
				if in(c, l.opt.PunctuationChars) {
					token = append(token, c)
				} else {
					if !in(c, whitespace) {
						l.pushback = append(l.pushback, c)
					}
					l.state = stSpace
					break loop
				}
			case in(c, quotes):
				l.state = c
			case in(c, escape):
				escapedstate = stWord
				l.state = c
			case in(c, l.wordchars) || (l.opt.WhitespaceSplit && !in(c, l.opt.PunctuationChars)):
				token = append(token, c)
			default:
				if l.opt.PunctuationChars != "" {
					l.pushback = append(l.pushback, c)
				} else {
					return "", false, ErrUnsupportedOptions // unreachable: Lex rejects these options
				}
				l.state = stSpace
				if emit() {
					break loop
				}
			}
		}
	}
	if len(token) == 0 && !quoted {
		return "", false, nil
	}
	return string(token), true, nil
}
