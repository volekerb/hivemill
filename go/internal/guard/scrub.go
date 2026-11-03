package guard

import (
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// Port of the line scanner of ci/scrub_scan.py (Scanner, GENERIC) and ci/public_text_guard.py (GuardScanner, EXTRA)
// for the public-text rule (T-0887). The hook only reads the pattern id of every hit (the refusal prints the count
// and the sorted ids, never the matched text), so the masks, labels and hide_* helpers are not ported.
//
// RE2 has no lookaround and its \b, \w, \s, \d are ASCII, Python's are Unicode. Each pattern is an RE2 expression
// plus hand-written assertions (scanRule.lb before the match, scanRule.asrt inside it) and a trailing "lookahead"
// that is consumed by the expression and left out of the match end (the capture group at scanRule.main). RE2's
// leftmost-first priority equals Python's backtracking order, so the same match is found; a failed assertion
// retries one character further, as Python's search does.

const (
	idOwnerTerm = "owner-term"
	shortTerm   = 4 // owner terms this short or shorter match whole words only
	lookback    = 100
)

// pyFoldGroups is re._casefix._EXTRA_CASES (CPython 3.13): characters that re.IGNORECASE treats as equal although
// their simple lower-case forms differ. The first member is the representative.
var pyFoldGroups = [][]rune{
	{'i', 0x131}, {'s', 0x17f}, {0xb5, 0x3bc}, {0x345, 0x3b9, 0x1fbe}, {0x390, 0x1fd3}, {0x3b0, 0x1fe3},
	{0x3b2, 0x3d0}, {0x3b5, 0x3f5}, {0x3b8, 0x3d1}, {0x3ba, 0x3f0}, {0x3c0, 0x3d6}, {0x3c1, 0x3f1}, {0x3c2, 0x3c3},
	{0x3c6, 0x3d5}, {0x432, 0x1c80}, {0x434, 0x1c81}, {0x43e, 0x1c82}, {0x441, 0x1c83}, {0x442, 0x1c84, 0x1c85},
	{0x44a, 0x1c86}, {0x463, 0x1c87}, {0x1c88, 0xa64b}, {0x1e61, 0x1e9b}, {0xfb05, 0xfb06},
}

var pyFoldCanon = func() map[rune]rune {
	m := map[rune]rune{}
	for _, g := range pyFoldGroups {
		for _, r := range g {
			m[r] = g[0]
		}
	}
	return m
}()

// foldKey is the value re.IGNORECASE compares: the simple lower-case form (U+0130 gives "i"), then the extra cases.
func foldKey(r rune) rune {
	if r < 0x80 {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	k := unicode.ToLower(r)
	if c, ok := pyFoldCanon[k]; ok {
		return c
	}
	return k
}

// pyLowerFull is str.lower(): U+0130 lowers to two characters and a capital sigma ends a word as a final sigma.
func pyLowerFull(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s)
	}
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch {
		case r == 0x130:
			b.WriteString("i̇")
		case r == 0x3a3:
			if finalSigma(rs, i) {
				b.WriteRune(0x3c2)
			} else {
				b.WriteRune(0x3c3)
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isCased(r rune) bool { return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) }

func isCaseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) || strings.ContainsRune("'.:^`·‘’․‧︓﹒﹕＇．：", r)
}

// finalSigma is the Final_Sigma condition: a cased letter before (case-ignorables skipped), none after.
func finalSigma(rs []rune, i int) bool {
	j := i - 1
	for j >= 0 && isCaseIgnorable(rs[j]) {
		j--
	}
	if j < 0 || !isCased(rs[j]) {
		return false
	}
	k := i + 1
	for k < len(rs) && isCaseIgnorable(rs[k]) {
		k++
	}
	return k >= len(rs) || !isCased(rs[k])
}

// digitVal is int() of one Unicode decimal digit.
func digitVal(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	n := 0
	for q := r - 1; unicode.Is(unicode.Nd, q); q-- {
		n++
	}
	return n % 10
}

// pyDecodeReplace is bytes.decode("utf-8", "replace"): every maximal ill-formed subpart becomes one U+FFFD.
func pyDecodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			sb.WriteByte(c)
			i++
			continue
		}
		need, lo, hi := 0, byte(0x80), byte(0xbf)
		switch {
		case c >= 0xc2 && c <= 0xdf:
			need = 1
		case c == 0xe0:
			need, lo = 2, 0xa0
		case c >= 0xe1 && c <= 0xec, c == 0xee, c == 0xef:
			need = 2
		case c == 0xed:
			need, hi = 2, 0x9f
		case c == 0xf0:
			need, lo = 3, 0x90
		case c >= 0xf1 && c <= 0xf3:
			need = 3
		case c == 0xf4:
			need, hi = 3, 0x8f
		}
		if need == 0 {
			sb.WriteRune(utf8.RuneError)
			i++
			continue
		}
		j, ok := i+1, true
		for k := 0; k < need; k++ {
			if j >= len(b) || b[j] < lo || b[j] > hi {
				ok = false
				break
			}
			lo, hi = 0x80, 0xbf
			j++
		}
		if ok {
			sb.Write(b[i:j])
		} else {
			sb.WriteRune(utf8.RuneError)
		}
		i = j
	}
	return sb.String()
}

// pyStrerror is OSError.strerror (the C library's text) for an error of os or syscall.
func pyStrerror(err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		s := en.Error()
		if s != "" {
			return strings.ToUpper(s[:1]) + s[1:]
		}
	}
	return err.Error()
}

// ───────────────────────────── the patterns ─────────────────────────────

type scanRule struct {
	id   string
	re   *regexp.Regexp
	main int                          // capture group that is the match (the rest is a consumed lookahead)
	lb   func(s string, i int) bool   // lookbehind at the start; false = no match here
	asrt func(s string, m []int) bool // assertion inside the match (\b); false = no match here
	keep func(s string, m []int) bool // the match predicate of the Python pattern; false = hit dropped
}

const (
	reO    = `(?:25[0-5]|2[0-4]\p{Nd}|[01]?\p{Nd}?\p{Nd})`
	reTail = `(?:\z|[^\p{Nd}])`
	reWord = `\p{L}\p{N}_`
	// extras of re.IGNORECASE next to [A-Za-z]: U+0130, U+0131 (Go's fold already adds U+017F and U+212A)
	reCI = `\x{130}\x{131}`
)

// ciI spells the letter i so that (?i) also takes U+0130 and U+0131, as Python does.
func ciI(s string) string { return strings.NewReplacer("i", `[i\x{130}\x{131}]`).Replace(s) }

func notWordBefore(s string, i int) bool { return !wordBefore(s, i) }

// noDigitDotBefore is (?<![\d.]).
func noDigitDotBefore(s string, i int) bool {
	r := prevRune(s, i)
	return !(r == '.' || (r >= 0 && isDecimal(r)))
}

func grp(s string, m []int, g int) string {
	if m[2*g] < 0 {
		return ""
	}
	return s[m[2*g]:m[2*g+1]]
}

var placeholderWords = []string{"xxx", "changeme", "example", "dummy", "test"}

// hasPlaceholder is PLACEHOLDER_RE.search(v) (re.I).
func hasPlaceholder(v string) bool {
	var b strings.Builder
	for _, r := range v {
		b.WriteRune(foldKey(r))
	}
	k := b.String()
	for _, w := range placeholderWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// isCodeRef is CODE_REF_RE.fullmatch: [A-Za-z_]\w*(?:\.\w+)+
func isCodeRef(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return false
	}
	for i, p := range parts {
		if p == "" {
			return false
		}
		for j, r := range p {
			if !isWord(r) || (i == 0 && j == 0 && !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')) {
				return false
			}
		}
	}
	return true
}

// realValue is _real_value: not a placeholder such as <...>, ${...}, $VAR, xxx, changeme, example, dummy, test.
func realValue(v string) bool {
	return !strings.HasPrefix(v, "<") && !strings.HasPrefix(v, "$") && !hasPlaceholder(v) && !isCodeRef(v)
}

var (
	emailOKNoreply = regexp.MustCompile(`^(?:[0-9]{1,20}\+)?[a-z0-9][a-z0-9-]{0,38}@users\.noreply\.github\.com$`)
	emailOKDomain  = regexp.MustCompile(`^example\.(?:com|org|invalid)$`)
	systemdSuffix  = set("service", "socket", "target", "timer", "mount", "slice", "scope", "path", "device", "swap", "automount")
	homeOK         = set("user", "runner", "example", "placeholder")
	sha256Hex      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func allowedEmail(addr string) bool {
	low := strings.ToLower(addr) // [A-Za-z0-9._%+-@]: ASCII only
	at := strings.LastIndexByte(low, '@')
	labels := strings.Split(low[at+1:], ".")
	if len(labels) == 2 && in(systemdSuffix, labels[1]) {
		return true
	}
	return low == "noreply@anthropic.com" || low == "noreply@github.com" || emailOKNoreply.MatchString(low) ||
		emailOKDomain.MatchString(strings.ToLower(low[at+1:]))
}

func hostChar(r rune) bool {
	return r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
		r == 0x130 || r == 0x131 || r == 0x17f || r == 0x212a
}

// highEntropy is _high_entropy.
func highEntropy(s string, m []int) bool {
	v := grp(s, m, 1)
	digit := false
	for i := 0; i < len(v); i++ {
		if v[i] >= '0' && v[i] <= '9' {
			digit = true
			break
		}
	}
	if !digit || hasPlaceholder(v) || sha256Hex.MatchString(v) {
		return false
	}
	start := m[2]
	n, from := 0, start
	for from > 0 && n < lookback {
		_, size := utf8.DecodeLastRuneInString(s[:from])
		from -= size
		n++
	}
	window := s[from:start]
	if strings.Contains(v, "/") {
		return keyDirect.MatchString(window)
	}
	return keyNear.MatchString(window)
}

// KEY_WORDS with re.I
var keyWords = `(?:secret|token|ap` + `[i\x{130}\x{131}]` + `[_-]?key|passw(?:or)?d|pr` + `[i\x{130}\x{131}]` + `vate[_-]?key)`

var (
	keyNear   = regexp.MustCompile(`(?i)` + keyWords)
	keyDirect = regexp.MustCompile(`(?i)` + keyWords + `[` + reWord + `.-]{0,32}["']?[` + rxSpace + `]{0,8}[:=][` + rxSpace + `]{0,8}["']?\z`)
)

var (
	scanOnce  sync.Once
	genRules  []*scanRule
	extraRule []*scanRule
)

func rx(p string) *regexp.Regexp { return regexp.MustCompile(p) }

func buildRules() {
	sp := `[` + rxSpace + `]`
	homeRe := rx(`/ho` + `me/([^/` + rxSpace + `'"` + "`" + `]+)/`)
	emailRe := rx(`[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}(?:\.[A-Za-z0-9-]{1,63}){1,7}`)
	genRules = []*scanRule{
		{id: "home-path", re: homeRe, keep: func(s string, m []int) bool {
			n := grp(s, m, 1)
			return !(in(homeOK, pyLowerFull(n)) || strings.HasPrefix(n, "<") && strings.HasSuffix(n, ">"))
		}},
		{id: "rfc1918", re: rx(`(192\.168\.` + reO + `\.` + reO + `|10\.` + reO + `\.` + reO + `\.` + reO +
			`|172\.0*(?:1[6-9]|2\p{Nd}|3[01])\.` + reO + `\.` + reO + `)` + reTail), main: 1, lb: noDigitDotBefore},
		{id: "cgnat", re: rx(`(100\.0*(?:6[4-9]|[7-9]\p{Nd}|1[01]\p{Nd}|12[0-7])\.` + reO + `\.` + reO + `)` + reTail), main: 1, lb: noDigitDotBefore},
		{id: "email", re: emailRe, keep: func(s string, m []int) bool { return !allowedEmail(grp(s, m, 0)) }},
		{id: "github-token", re: rx(`gh[pousr]_[A-Za-z0-9]{36,}`), lb: notWordBefore},
		{id: "github-pat", re: rx(`github_pat_[A-Za-z0-9_]{22,}`), lb: notWordBefore},
		{id: "anthropic-key", re: rx(`sk-ant-[A-Za-z0-9]+-[A-Za-z0-9_-]{20,}`), lb: notWordBefore},
		{id: "aws-access-key-id", re: rx(`AKIA[0-9A-Z]{16}`), lb: notWordBefore},
		{id: "private-key", re: rx(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
		{id: "railway-token", re: rx(`(RAILWAY_(?:API_)?TOKEN)["']?` + sp + `*[:=]` + sp + `*["']?([^` + rxSpace + `"'` + "`" + `]{16,})`), main: 0,
			lb:   notWordBefore,
			asrt: func(s string, m []int) bool { return !wordAt(s, m[3]) },
			keep: func(s string, m []int) bool { return realValue(grp(s, m, 2)) }},
		{id: "railway-token", re: rx(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`), lb: notWordBefore,
			asrt: func(s string, m []int) bool { return !wordAt(s, m[1]) },
			keep: func(s string, m []int) bool { return strings.Contains(pyLowerFull(s), "railway") }},
		{id: "oauth-secret", re: rx(`(?i)(cl` + `[i\x{130}\x{131}]` + `ent_secret|refresh_token|access_token)["']?` + sp + `*[:=]` + sp + `*["']?([A-Za-z0-9._~+/=` + reCI + `-]{20,})`),
			lb: notWordBefore, asrt: func(s string, m []int) bool { return !wordAt(s, m[3]) }},
		{id: "generic-secret", re: rx(`(?i)` + keyWords + `[` + reWord + `.-]{0,64}["']?` + sp + `*[:=]` + sp + `*["']?([^` + rxSpace + `"'` + "`" + `(){}\[\]<>,;$]{16,})`),
			keep: func(s string, m []int) bool { return realValue(grp(s, m, 1)) }},
		{id: "high-entropy-id", re: rx(`([A-Za-z0-9+/_-]{40,}={0,2})(?:\z|[^A-Za-z0-9+/_-])`), main: 1,
			lb: func(s string, i int) bool {
				r := prevRune(s, i)
				return !(r >= 0 && r < 0x80 && (r == '+' || r == '/' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'))
			}, keep: highEntropy},
	}
	oct := `(?:25[0-5]|2[0-4]\p{Nd}|1\p{Nd}\p{Nd}|0?\p{Nd}?\p{Nd})`
	hc := `A-Za-z0-9_` + reCI + `\-`
	extraRule = []*scanRule{
		{id: "ipv4", re: rx(`(` + oct + `(?:\.` + oct + `){3})(?:\z|[^\p{Nd}.]|\.(?:\z|[^\p{Nd}]))`), main: 1,
			lb: func(s string, i int) bool {
				r := prevRune(s, i)
				if r >= 0 && isDecimal(r) {
					return false
				}
				if r == '.' {
					_, size := utf8.DecodeLastRuneInString(s[:i])
					if r2 := prevRune(s, i-size); r2 >= 0 && isDecimal(r2) {
						return false
					}
				}
				return true
			}, keep: func(s string, m []int) bool { return realIPv4(grp(s, m, 1)) }},
		{id: "private-host", re: rx(`(?i)((?:[` + hc + `]{1,63}\.){1,8}(?:local|lan|` + ciI("internal") + `|home\.arpa|ts\.net))(?:\z|[^` + hc + `.]|\.(?:\z|[^A-Za-z0-9` + reCI + `]))`), main: 1,
			lb: func(s string, i int) bool { r := prevRune(s, i); return !(r >= 0 && hostChar(r)) }},
	}
}

// realIPv4 is _real_ipv4.
func realIPv4(text string) bool {
	parts := strings.Split(text, ".")
	var oct [4]int
	for i, p := range parts {
		if len(p) > 1 && p[0] == '0' {
			return true // octal-looking form: never exempt
		}
		v := 0
		for _, r := range p {
			v = v*10 + digitVal(r)
		}
		oct[i] = v
	}
	if oct[0] == 127 || oct == [4]int{} || oct == [4]int{255, 255, 255, 255} {
		return false
	}
	t := [3]int{oct[0], oct[1], oct[2]}
	return t != [3]int{192, 0, 2} && t != [3]int{198, 51, 100} && t != [3]int{203, 0, 113}
}

// find is finditer for one rule over a line: the number of kept matches.
func (r *scanRule) count(s string) int {
	n, pos := 0, 0
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
			n++
		}
		if end <= start {
			end = start + 1
		}
		pos = end
	}
	return n
}

// ───────────────────────────── terms ─────────────────────────────

// scrubScanner is GuardScanner: the owner terms of one run.
type scrubScanner struct {
	terms [][]rune // by decreasing length (stable), as the alternation of Scanner.term_re
	words []bool   // the term matches whole words only
}

func newScrubScanner(terms []string) *scrubScanner {
	scanOnce.Do(buildRules)
	idx := make([]int, len(terms))
	for i := range idx {
		idx[i] = i
	}
	rs := make([][]rune, len(terms))
	for i, t := range terms {
		rs[i] = []rune(t)
	}
	sort.SliceStable(idx, func(a, b int) bool { return len(rs[idx[a]]) > len(rs[idx[b]]) })
	sc := &scrubScanner{}
	for _, i := range idx {
		k := make([]rune, len(rs[i]))
		for j, r := range rs[i] {
			k[j] = foldKey(r)
		}
		sc.terms = append(sc.terms, k)
		sc.words = append(sc.words, len(rs[i]) <= shortTerm)
	}
	return sc
}

// termHits counts the owner-term matches of one line.
func (sc *scrubScanner) termHits(line string) int {
	if len(sc.terms) == 0 {
		return 0
	}
	rs := []rune(line)
	keys := make([]rune, len(rs))
	for i, r := range rs {
		keys[i] = foldKey(r)
	}
	n := 0
	for i := 0; i < len(rs); {
		matched := 0
		for t, term := range sc.terms {
			end := i + len(term)
			if end > len(rs) {
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
			if sc.words[t] && ((i > 0 && isWord(rs[i-1])) || (end < len(rs) && isWord(rs[end]))) {
				continue
			}
			matched = len(term)
			break
		}
		if matched > 0 {
			n++
			i += matched
		} else {
			i++
		}
	}
	return n
}

// scanLine appends the pattern id of every hit on one line: GENERIC, then the owner terms, then EXTRA.
func (sc *scrubScanner) scanLine(line string, ids *[]string) {
	add := func(id string, n int) {
		for i := 0; i < n; i++ {
			*ids = append(*ids, id)
		}
	}
	for _, r := range genRules {
		add(r.id, r.count(line))
	}
	add(idOwnerTerm, sc.termHits(line))
	for _, r := range extraRule {
		add(r.id, r.count(line))
	}
}

// scanLines is check_text: every line of text (str.splitlines).
func (sc *scrubScanner) scanLines(text string) []string {
	var ids []string
	for _, line := range pyLines(text) {
		sc.scanLine(line, &ids)
	}
	return ids
}

// errNoTerms is public_text_guard.NoTermsError; errTermsRead is a UsageError of scrub_scan.load_terms.
type usageError struct{ Msg string }

func (e *usageError) Error() string { return e.Msg }

const noTermsMsg = "public-text-guard: no owner terms loaded"

// loadTermsOrFail is public_text_guard.load_terms_or_fail(os.environ, configPath).
func loadTermsOrFail(configPath string) ([]string, error) {
	var paths []string
	if v := os.Getenv(termsVar); v != "" {
		paths = append(paths, v)
	} else if configPath != "" {
		paths = append(paths, configPath)
	}
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
	if len(terms) == 0 {
		return nil, &usageError{noTermsMsg + ". Set SCRUB_TERMS_FILE (or the scrub_terms_file config) to the owner terms file."}
	}
	sort.SliceStable(terms, func(a, b int) bool { return pyLowerFull(terms[a]) < pyLowerFull(terms[b]) })
	return terms, nil
}
