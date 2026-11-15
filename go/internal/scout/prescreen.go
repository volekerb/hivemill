package scout

import (
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Port of the exfiltration pre-screen of scout/vendor.py (plan G-009 §7.3). Hits are cheap evidence for the owner,
// not a verdict. The Python patterns use lookarounds and Unicode classes that RE2 lacks, so each of them is a small
// hand-written scanner here; TestPrescreenGolden compares the hits with the Python function on a corpus of lines.
//
// Known gap: Python folds text with NFKC before matching. Go's standard library has no NFKC (golang.org/x/text is
// not a dependency of this module), so fold() covers the compatibility forms an attacker can use to hide a keyword
// (fullwidth forms, mathematical and circled letters, the ideographic full stop) and drops every format character.

// DOCS hosts are the only hosts a vendored file may link to without a hit (plus anthropics/ and its own repo on GitHub).
var docHosts = set("docs.anthropic.com", "docs.claude.com", "code.claude.com", "anthropic.com")

var fileExts = set("tsx", "jsx", "java", "cpp", "c", "h", "hpp", "php", "rst", "mdx", "webp", "gz", "tgz", "env", "xml", "sql", "bat", "ps1", "lua", "swift", "kt", "png", "jpg", "jpeg", "gif", "svg", "pdf", "md", "txt", "py", "sh", "json", "yml", "yaml", "js", "cjs", "mjs", "ts", "csv", "toml", "html", "css", "rb", "go", "rs", "ini", "cfg", "lock", "log")

const (
	maxProseGap = 3
	urlStop     = wsClass + `<>"'` + "`" + `)\]`
	b64Class    = `A-Za-z0-9+/=_-`
)

var (
	b64Run        = regexp.MustCompile(`[` + b64Class + `]{200,}`)
	urlRe2        = regexp.MustCompile(`(?i)https?://[^` + urlStop + `]+`)
	apacheURL     = regexp.MustCompile(`^https?://www\.apache\.org/licenses/(?:LICENSE-2\.0)?$`)
	emailRe       = regexp.MustCompile(`^[A-Za-z0-9._+-]+@([A-Za-z0-9.-]+)$`)
	licenseRe     = regexp.MustCompile(`(?i)^(?:license|notice)(?:[-_.][^/]*)?$`)
	b64Token      = regexp.MustCompile(`^[` + b64Class + `]+$`)
	skAnt         = "sk-ant-"
	credFile      = regexp.MustCompile(`(?i)\.credentials\.json`)
	apiKeyAlt     = regexp.MustCompile(`(?i)api[ _]key|ANTHROPIC_API_KEY`)
	reCurl        = regexp.MustCompile(`(?i)curl`)
	reWget        = regexp.MustCompile(`(?i)wget`)
	reUrllib      = regexp.MustCompile(`(?i)urllib`)
	reNc          = regexp.MustCompile(`nc`)
	reNetcat      = regexp.MustCompile(`(?i)netcat`)
	rePrintenv    = regexp.MustCompile(`(?i)printenv`)
	reEnviron     = regexp.MustCompile(`(?i)environ`)
	reCredentials = regexp.MustCompile(`(?i)credentials`)
	requestsRe    = regexp.MustCompile(`(?i)requests\.`)
)

// fold is _fold: NFKC (subset, see the package note) and drop every format character (Unicode Cf: zero-width, soft
// hyphen, bidi controls, BOM), then map the ideographic and fullwidth full stops to ".".
func fold(text string) string {
	ascii := true
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return text
	}
	var sb strings.Builder
	for _, r := range text {
		for _, c := range compat(r) {
			if unicode.Is(unicode.Cf, c) {
				continue
			}
			switch c {
			case 0x3002, 0xFF0E, 0xFF61:
				c = '.'
			}
			sb.WriteRune(c)
		}
	}
	return sb.String()
}

// compat maps the compatibility characters NFKC would turn into ASCII (the ones that matter for a keyword match).
func compat(r rune) []rune {
	switch {
	case r >= 0xFF01 && r <= 0xFF5E: // fullwidth ASCII
		return []rune{r - 0xFEE0}
	case r == 0x3000 || r == 0xA0 || (r >= 0x2000 && r <= 0x200A) || r == 0x202F || r == 0x205F:
		return []rune{' '}
	case r >= 0x1D400 && r <= 0x1D7FF: // mathematical alphanumerics
		return []rune{mathAlnum(r)}
	case r >= 0x24B6 && r <= 0x24CF: // circled capital letters
		return []rune{'A' + (r - 0x24B6)}
	case r >= 0x24D0 && r <= 0x24E9: // circled small letters
		return []rune{'a' + (r - 0x24D0)}
	case r >= 0x2070 && r <= 0x2079 && r != 0x2071 && r != 0x2072 && r != 0x2073:
		return []rune{'0' + (r - 0x2070)}
	case r >= 0x2080 && r <= 0x2089:
		return []rune{'0' + (r - 0x2080)}
	case r == 0xFB00:
		return []rune("ff")
	case r == 0xFB01:
		return []rune("fi")
	case r == 0xFB02:
		return []rune("fl")
	case r == 0xFB03:
		return []rune("ffi")
	case r == 0xFB04:
		return []rune("ffl")
	case r == 0x17F:
		return []rune{'s'}
	}
	return []rune{r}
}

func mathAlnum(r rune) rune {
	switch {
	case r >= 0x1D7CE: // digits: five sets of ten
		return '0' + (r-0x1D7CE)%10
	case r >= 0x1D400 && r <= 0x1D6A3: // 13 alphabets of 52 letters
		i := (r - 0x1D400) % 52
		if i < 26 {
			return 'A' + i
		}
		return 'a' + (i - 26)
	}
	return r
}

// pySplitlines is str.splitlines().
func pySplitlines(s string) []string {
	var out []string
	start := 0
	i := 0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, s[start:i])
			start = i + w
		case '\r':
			out = append(out, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
		i += w
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func inB64(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' || r == '_' || r == '-'
}

// distinct is len(set(s)).
func distinct(s string) int {
	seen := map[rune]bool{}
	for _, r := range s {
		seen[r] = true
	}
	return len(seen)
}

// b64Wrapped is B64_WRAPPED.finditer: runs of 8+ base64 characters joined by whitespace, anchored at the start of a
// run (the lookbehind). Returns the [start,end) byte offsets of every match.
func b64Wrapped(s string) [][2]int {
	var out [][2]int
	runEnd := func(i int) int {
		for i < len(s) {
			r, w := utf8.DecodeRuneInString(s[i:])
			if !inB64(r) {
				break
			}
			i += w
		}
		return i
	}
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if !inB64(r) || (i > 0 && prevIsB64(s, i)) {
			i += w
			continue
		}
		// i starts a maximal run
		e := runEnd(i)
		if e-i < 8 {
			i = e
			continue
		}
		matchEnd, reps := e, 0
		for {
			j := e
			for j < len(s) {
				r2, w2 := utf8.DecodeRuneInString(s[j:])
				if !isPySpace(r2) {
					break
				}
				j += w2
			}
			if j == e {
				break // \s+ needs at least one whitespace character
			}
			e2 := runEnd(j)
			if e2-j < 8 {
				break
			}
			matchEnd, reps, e = e2, reps+1, e2
		}
		if reps > 0 {
			out = append(out, [2]int{i, matchEnd})
			i = matchEnd
		} else {
			i = e
		}
	}
	return out
}

func prevIsB64(s string, i int) bool {
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return inB64(r)
}

func removeSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if isPySpace(r) {
			return -1
		}
		return r
	}, s)
}

func hasAny(s, set string) bool { return strings.ContainsAny(s, set) }

func hasUpper(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}
func hasLower(s string) bool {
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			return true
		}
	}
	return false
}

// b64ish is B64_ISH.match: mixed case plus a digit or symbol (the token has no newline).
func b64ish(s string) bool { return hasAny(s, "0123456789+=") && hasLower(s) && hasUpper(s) }

func countUpper(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsUpper(r) {
			n++
		}
	}
	return n
}

func countLower(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLower(r) {
			n++
		}
	}
	return n
}

func countDigit(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// randomCase is _random_case: the letter mix of random base64.
func randomCase(joined string) bool {
	n := runeLen(joined)
	return float64(countUpper(joined))*10 >= float64(n)*2.5 && float64(countLower(joined))*10 >= float64(n)*2.5 &&
		countDigit(joined)*20 >= n
}

// plainToken is PLAIN_TOKEN.fullmatch: [A-Z]?[a-z]+ | [A-Z]+ | \d+.
func plainToken(t string) bool {
	if t == "" {
		return false
	}
	allDigits, allUpper := true, true
	for _, r := range t {
		if !unicode.IsDigit(r) {
			allDigits = false
		}
		if !(r >= 'A' && r <= 'Z') {
			allUpper = false
		}
	}
	if allDigits || allUpper {
		return true
	}
	rs := []rune(t)
	if rs[0] >= 'A' && rs[0] <= 'Z' {
		rs = rs[1:]
	}
	if len(rs) == 0 {
		return false
	}
	for _, r := range rs {
		if !(r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

// wordy is _wordy: over a third of the chunks are plain words or numbers.
func wordy(run []string) bool {
	var long []string
	for _, t := range run {
		if runeLen(t) > 1 {
			long = append(long, t)
		}
	}
	n := 0
	for _, t := range long {
		if plainToken(t) {
			n++
		}
	}
	return n*3 > len(long)
}

// hexColorSub is HEX_COLOR.sub(" ", s): `#` + 3-8 hex digits that end at a word boundary (Unicode \w).
func hexColorSub(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '#' {
			j := i + 1
			for j < len(s) && isHex(s[j]) {
				j++
			}
			if n := j - (i + 1); n >= 3 && n <= 8 && nextNotWord(s, j) {
				sb.WriteByte(' ')
				i = j
				continue
			}
		}
		_, w := utf8.DecodeRuneInString(s[i:])
		sb.WriteString(s[i : i+w])
		i += w
	}
	return sb.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// lineLabelStrip is LINE_LABEL.sub("", line, 1): `^\s*[A-Za-z_]{1,12}:\s+` (a one-word label per line is not blob content).
func lineLabelStrip(s string) string {
	i := 0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if !isPySpace(r) {
			break
		}
		i += w
	}
	j := i
	for j < len(s) && (s[j] == '_' || (s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
		j++
	}
	if n := j - i; n < 1 || n > 12 || j >= len(s) || s[j] != ':' {
		return s
	}
	k := j + 1
	start := k
	for k < len(s) {
		r, w := utf8.DecodeRuneInString(s[k:])
		if !isPySpace(r) {
			break
		}
		k += w
	}
	if k == start {
		return s
	}
	return s[k:]
}

const chunkSepChars = "\"'`,;>|#*()<>:.\\!?[]{}"

// chunkSepSub is CHUNK_SEP.sub(" ", s): a run of separator characters, a lone `+` between whitespace, or a `+`
// at the start of the line before whitespace.
func chunkSepSub(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if strings.IndexByte(chunkSepChars, c) >= 0 {
			for i < len(s) && strings.IndexByte(chunkSepChars, s[i]) >= 0 {
				i++
			}
			sb.WriteByte(' ')
			continue
		}
		if c == '+' && i+1 < len(s) {
			nr, _ := utf8.DecodeRuneInString(s[i+1:])
			pr, _ := utf8.DecodeLastRuneInString(s[:i])
			if isPySpace(nr) && (i == 0 || isPySpace(pr)) {
				sb.WriteByte(' ')
				i++
				continue
			}
		}
		_, w := utf8.DecodeRuneInString(s[i:])
		sb.WriteString(s[i : i+w])
		i += w
	}
	return sb.String()
}

// smallChunkBlobs is _small_chunk_blobs: 0-based first line of each blob split into short chunks (under 8
// characters) or wrapped with per-line prefixes. One pass over the tokens.
func smallChunkBlobs(lines []string) map[int]bool {
	out := map[int]bool{}
	var run []string
	start, gap := 0, 0
	closeRun := func() {
		gap = 0
		joined := strings.Join(run, "")
		if runeLen(joined) >= 200 && distinct(joined) > 8 && randomCase(joined) && !wordy(run) {
			out[start] = true
		}
		run = nil
	}
	take := func(seg []string, i int) {
		if len(seg) == 0 {
			return
		}
		joined := strings.Join(seg, "")
		var blobby bool
		if runeLen(joined) < 8*len(seg) { // chunks too short to show word shape: ask for random-looking case
			up := countUpper(joined)
			blobby = up*4 >= runeLen(joined) || (up*7 >= runeLen(joined) && b64ish(joined))
		} else {
			n := 0
			for _, t := range seg {
				if b64ish(t) {
					n++
				}
			}
			blobby = n*10 >= len(seg)*3
		}
		if blobby {
			if len(run) == 0 {
				start = i
			}
			run = append(run, seg...)
			gap = 0
		} else if len(run) > 0 {
			gap++
			if gap > maxProseGap { // a long stretch of prose: not one blob
				closeRun()
			}
		}
	}
	for i, ln := range lines {
		var seg []string
		for _, tok := range pyFields(chunkSepSub(hexColorSub(lineLabelStrip(ln)))) {
			if runeLen(tok) > 1 && distinct(tok) == 1 {
				continue // a rule of dashes or equals
			}
			if b64Token.MatchString(tok) {
				seg = append(seg, tok)
				continue
			}
			take(seg, i)
			seg = nil
			closeRun()
		}
		take(seg, i)
	}
	closeRun()
	return out
}

func isIP(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

// pyUnquote is urllib.parse.unquote: %XX sequences decoded as UTF-8 with replacement.
func pyUnquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			n, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b = append(b, byte(n))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	return decodeReplace(b)
}

// splitURL is the part of urlsplit this check needs: scheme (lower-cased), netloc and path. ok is false when
// urlsplit would raise ValueError (an unbalanced IPv6 bracket).
func splitURL(u string) (scheme, netloc, path string, ok bool) {
	u = strings.TrimLeftFunc(u, func(r rune) bool { return r <= ' ' })
	u = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(u)
	if i := strings.IndexByte(u, ':'); i > 0 {
		valid := true
		for j := 0; j < i; j++ {
			c := u[j]
			isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
			if !(isAlpha || (j > 0 && ((c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.'))) {
				valid = false
				break
			}
		}
		if valid {
			scheme, u = strings.ToLower(u[:i]), u[i+1:]
		}
	}
	if strings.HasPrefix(u, "//") {
		rest := u[2:]
		end := len(rest)
		for _, c := range "/?#" {
			if i := strings.IndexRune(rest, c); i >= 0 && i < end {
				end = i
			}
		}
		netloc, u = rest[:end], rest[end:]
		if (strings.Contains(netloc, "[") && !strings.Contains(netloc, "]")) || (strings.Contains(netloc, "]") && !strings.Contains(netloc, "[")) {
			return "", "", "", false
		}
	}
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	return scheme, netloc, u, true
}

// hostname is SplitResult.hostname: lower-cased, without userinfo and port; "" when empty.
func hostname(netloc string) string {
	if i := strings.LastIndexByte(netloc, '@'); i >= 0 {
		netloc = netloc[i+1:]
	}
	if strings.HasPrefix(netloc, "[") {
		if i := strings.IndexByte(netloc, ']'); i > 0 {
			return strings.ToLower(netloc[1:i])
		}
		return ""
	}
	if i := strings.IndexByte(netloc, ':'); i >= 0 {
		netloc = netloc[:i]
	}
	return strings.ToLower(netloc)
}

// posixNormpath is posixpath.normpath for a path that starts with "/".
func posixNormpath(p string) string {
	var comps []string
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." {
			continue
		}
		if c != ".." || (len(comps) > 0 && comps[len(comps)-1] == "..") {
			comps = append(comps, c)
		} else if len(comps) > 0 {
			comps = comps[:len(comps)-1]
		}
	}
	return "/" + strings.Join(comps, "/")
}

// urlAllowed is _url_allowed: only http(s) links to the docs hosts, to github.com/anthropics/... or to the
// candidate's own repo are allowed; userinfo and IP literals never are.
func urlAllowed(u, ownRepo string) bool {
	scheme, netloc, path, ok := splitURL(u)
	if !ok {
		return false
	}
	host := hostname(netloc)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if strings.Contains(netloc, "@") || isIP(host) {
		return false
	}
	if docHosts[host] {
		return true
	}
	if host == "github.com" {
		p := posixNormpath("/" + strings.ReplaceAll(pyUnquote(path), "\\", "/"))
		var segs []string
		for _, s := range strings.Split(p, "/") {
			if s != "" && s != "." {
				segs = append(segs, s)
			}
		}
		if len(segs) > 0 && strings.ToLower(segs[0]) == "anthropics" {
			return true
		}
		own := strings.Split(strings.ToLower(ownRepo), "/")
		if len(own) != 2 || len(segs) < 2 {
			return false
		}
		return strings.ToLower(segs[0]) == own[0] && strings.ToLower(segs[1]) == own[1]
	}
	return false
}

func isASCIIAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func notURLStop(r rune) bool {
	return !isPySpace(r) && !strings.ContainsRune("<>\"'`)]", r)
}

// bareHostAt tries BARE_HOST at byte offset i: `(?<![\w.-])(?://)?((?:label\.)+[a-z]{2,24}(?::\d+)?(?:/[^stop]*)?)(?![\w-])`.
// It returns the match end and the byte offset where group 1 starts.
func bareHostAt(s string, i int) (end, g1 int, ok bool) {
	if !prevNotWordDashDot(s, i) {
		return 0, 0, false
	}
	starts := []int{i}
	if strings.HasPrefix(s[i:], "//") {
		starts = []int{i + 2, i} // "//" is tried first; without it the domain must start at "/", which cannot match
	}
	for _, st := range starts {
		if st == i && strings.HasPrefix(s[i:], "//") {
			continue
		}
		if e, ok := domainFrom(s, st); ok {
			return e, st, true
		}
	}
	return 0, 0, false
}

func prevNotWordDashDot(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !(r == '.' || r == '-' || isWordRune(r))
}

// domainFrom parses `(?:label\.)+ TLD port? path?` at st with the same preference order as backtracking.
func domainFrom(s string, st int) (int, bool) {
	// chain of labels, each `[a-z0-9]([a-z0-9-]*[a-z0-9])?\.`; after[k] is the offset after the (k+1)-th dot
	var after []int
	p := st
	for {
		q := p
		for q < len(s) && (isASCIIAlnum(s[q]) || s[q] == '-') {
			q++
		}
		if q == p || q >= len(s) || s[q] != '.' || !isASCIIAlnum(s[p]) || !isASCIIAlnum(s[q-1]) {
			break
		}
		after = append(after, q+1)
		p = q + 1
	}
	for k := len(after) - 1; k >= 0; k-- {
		t := after[k]
		m := t
		for m < len(s) && isASCIILetter(s[m]) {
			m++
		}
		for n := min(24, m-t); n >= 2; n-- {
			if e, ok := afterTLD(s, t+n); ok {
				return e, true
			}
		}
	}
	return 0, false
}

// afterTLD handles `(?::\d+)?(?:/[^stop]*)?(?![\w-])` after the TLD, trying the greedy alternatives first.
func afterTLD(s string, e0 int) (int, bool) {
	var ports []int
	if e0 < len(s) && s[e0] == ':' {
		j := e0 + 1
		for j < len(s) {
			r, w := utf8.DecodeRuneInString(s[j:])
			if !unicode.IsDigit(r) {
				break
			}
			j += w
		}
		for p := j; p > e0+1; { // greedy digits, then fewer
			ports = append(ports, p)
			_, w := utf8.DecodeLastRuneInString(s[:p])
			p -= w
		}
	}
	ports = append(ports, e0)
	for _, pe := range ports {
		if pe < len(s) && s[pe] == '/' {
			j := pe + 1
			for j < len(s) {
				r, w := utf8.DecodeRuneInString(s[j:])
				if !notURLStop(r) {
					break
				}
				j += w
			}
			if nextNotWordDash(s, j) {
				return j, true
			}
		}
		if nextNotWordDash(s, pe) {
			return pe, true
		}
	}
	return 0, false
}

func nextNotWordDash(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return !(r == '-' || isWordRune(r))
}

// ipv4Tokens returns IPV4_TOKEN matches: `(?<![\w.])(?:\d{1,3}\.){3}\d{1,3}(?![\w])`.
func ipv4Tokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsDigit(r) || !prevNotWordDot(s, i) {
			i += w
			continue
		}
		if e, ok := ipv4At(s, i); ok {
			out = append(out, s[i:e])
			i = e
			continue
		}
		i += w
	}
	return out
}

func prevNotWordDot(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !(r == '.' || isWordRune(r))
}

func ipv4At(s string, i int) (int, bool) {
	p := i
	for g := 0; g < 4; g++ {
		j, n := p, 0
		for j < len(s) {
			r, w := utf8.DecodeRuneInString(s[j:])
			if !unicode.IsDigit(r) {
				break
			}
			j, n = j+w, n+1
		}
		if n < 1 || n > 3 {
			return 0, false
		}
		if g < 3 {
			if j >= len(s) || s[j] != '.' {
				return 0, false
			}
			p = j + 1
			continue
		}
		p = j
	}
	if p < len(s) {
		r, _ := utf8.DecodeRuneInString(s[p:])
		if isWordRune(r) {
			return 0, false
		}
	}
	return p, true
}

// ipv6Tokens returns the group 1 of each IPV6_TOKEN match: `(?<![\w:.])\[?([0-9a-f:]{2,}...)\]?(?![\w])`.
func ipv6Tokens(s string) []string {
	var out []string
	isH := func(c byte) bool { return isHex(c) || c == ':' }
	for i := 0; i < len(s); {
		_, w := utf8.DecodeRuneInString(s[i:])
		if !prevNotWordColonDot(s, i) {
			i += w
			continue
		}
		g, e, ok := ipv6At(s, i, isH)
		if ok {
			out = append(out, g)
			i = e
			continue
		}
		i += w
	}
	return out
}

func prevNotWordColonDot(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !(r == '.' || r == ':' || isWordRune(r))
}

func ipv6At(s string, i int, isH func(byte) bool) (group string, end int, ok bool) {
	try := func(from int) (string, int, bool) {
		m := from
		for m < len(s) && isH(s[m]) {
			m++
		}
		for n := m; n-from >= 2; n-- {
			if n < len(s) && s[n] == ']' {
				if nextNotWord(s, n+1) {
					return s[from:n], n + 1, true
				}
			}
			if nextNotWord(s, n) {
				return s[from:n], n, true
			}
		}
		return "", 0, false
	}
	if s[i] == '[' {
		return try(i + 1)
	}
	if isH(s[i]) {
		return try(i)
	}
	return "", 0, false
}

// intIPHit reports whether INT_IP has a match whose value fits 32 bits: an integer-form IP after `//` or `@`.
func intIPHit(s string) bool {
	for i := 0; i < len(s); i++ {
		var j int
		switch {
		case s[i] == '@':
			j = i + 1
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
			j = i + 2
		default:
			continue
		}
		if e, ok := hexIntAt(s, j); ok && intIPTail(s, e) {
			return true
		}
		if e, val, ok := decIntAt(s, j); ok && intIPTail(s, e) && val <= 0xFFFFFFFF {
			return true
		}
	}
	return false
}

// hexIntAt: `0x0*[0-9a-f]{1,8}` ending at the end of the hex run (anything shorter fails the (?!\w) lookahead).
func hexIntAt(s string, j int) (int, bool) {
	if j+1 >= len(s) || s[j] != '0' || (s[j+1] != 'x' && s[j+1] != 'X') {
		return 0, false
	}
	e := j + 2
	for e < len(s) && isHex(s[e]) {
		e++
	}
	total := e - (j + 2)
	z := 0
	for z < total && s[j+2+z] == '0' {
		z++
	}
	sig := total - min(z, total-1)
	if total < 1 || sig > 8 {
		return 0, false
	}
	return e, true
}

// decIntAt: `\d{8,10}` ending at the end of the digit run. Non-ASCII digits count as a hit (fail closed).
func decIntAt(s string, j int) (end int, val uint64, ok bool) {
	e, n := j, 0
	ascii := true
	for e < len(s) {
		r, w := utf8.DecodeRuneInString(s[e:])
		if !unicode.IsDigit(r) {
			break
		}
		if r > '9' {
			ascii = false
		}
		e, n = e+w, n+1
	}
	if n < 8 || n > 10 {
		return 0, 0, false
	}
	if !ascii {
		return e, 0, true
	}
	v, _ := strconv.ParseUint(s[j:e], 10, 64)
	return e, v, true
}

// intIPTail is `(?!\w)(?!\.\d)` at e.
func intIPTail(s string, e int) bool {
	if e >= len(s) {
		return true
	}
	r, w := utf8.DecodeRuneInString(s[e:])
	if isWordRune(r) {
		return false
	}
	if r == '.' && e+w < len(s) {
		n, _ := utf8.DecodeRuneInString(s[e+w:])
		if unicode.IsDigit(n) {
			return false
		}
	}
	return true
}

// bareHostBad is _bare_host_bad: a host-like token outside an http(s) URL that is not allowed. Fails closed:
// anything not clearly a file name or an allowed docs host hits.
func bareHostBad(line, ownRepo string) bool {
	line = urlRe2.ReplaceAllString(line, " ")
	for _, tok := range ipv4Tokens(line) {
		if isIP(tok) {
			return true
		}
	}
	for _, g := range ipv6Tokens(line) {
		if strings.Count(g, ":") >= 2 && isIP(g) {
			return true
		}
	}
	if intIPHit(line) {
		return true
	}
	for i := 0; i < len(line); {
		e, g1, ok := bareHostAt(line, i)
		if !ok {
			_, w := utf8.DecodeRuneInString(line[i:])
			i += w
			continue
		}
		tok := strings.TrimRight(line[g1:e], ".,;:")
		host := tok
		if k := strings.IndexAny(tok, "/:"); k >= 0 {
			host = tok[:k]
		}
		before := line[:i]
		if strings.HasSuffix(before, "@") {
			return true // userinfo or e-mail: never docs
		}
		schemeCtx := strings.HasSuffix(before, "//") || strings.HasPrefix(line[i:e], "//")
		ext := strings.ToLower(host[strings.LastIndex(host, ".")+1:])
		if fileExts[ext] && !schemeCtx && !strings.Contains(tok, "/") && !strings.Contains(tok, ":") {
			i = e
			continue // SKILL.md, run.py, images/logo.png: a file name (last segment), not a host
		}
		if !urlAllowed("https://"+tok, ownRepo) {
			return true
		}
		i = e
	}
	return false
}

func docEmail(addr string) bool {
	m := emailRe.FindStringSubmatch(addr)
	if m == nil {
		return false
	}
	host := strings.ToLower(m[1])
	return docHosts[host] || (strings.HasPrefix(host, "www.") && docHosts[host[4:]])
}

// pluginJSONEmails are the addresses a plugin.json may carry without a hit: author.email, support or support.email
// at a docs host, written on one line and nowhere else in the file. Anything unparseable or ambiguous gives none.
func pluginJSONEmails(text string) map[string]bool {
	out := map[string]bool{}
	if !isASCII(text) || strings.Contains(text, `\`) { // raw == folded == parsed literals; never parse the folded text
		return out
	}
	data, ok := parseJSON(text)
	obj, isObj := data.(map[string]any)
	if !ok || !isObj {
		return out
	}
	var cands []any
	author, _ := obj["author"].(map[string]any)
	support := obj["support"]
	if author != nil {
		cands = append(cands, author["email"])
	}
	cands = append(cands, support)
	if sm, ok := support.(map[string]any); ok {
		cands = append(cands, sm["email"])
	}
	for _, c := range cands {
		if a, ok := c.(string); ok && docEmail(a) && strings.Count(text, a) == 1 {
			out[a] = true
		}
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Hit is one pre-screen finding; File is relative to the screened directory.
type Hit struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Pattern string `json:"pattern"`
}

// pathSuffix is Path.suffix.
func pathSuffix(name string) string {
	i := strings.LastIndex(name, ".")
	if i > 0 && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

// patternHits is the PATTERNS table: the ids of every exfil pattern that matches the line, in table order.
func patternHits(line string, prose bool) []string {
	var found []string
	add := func(id string, hit bool) {
		if hit {
			found = append(found, id)
		}
	}
	add("sk-ant", strings.Contains(line, skAnt))
	add("curl", wordMatch(reCurl, line, nil))
	add("wget", wordMatch(reWget, line, nil))
	add("urllib", wordMatch(reUrllib, line, nil))
	add("requests", !prose && leadingWord(requestsRe, line))
	add("nc", wordMatch(reNc, line, nil))
	add("netcat", wordMatch(reNetcat, line, nil))
	add("printenv", wordMatch(rePrintenv, line, nil))
	add("environ", wordMatch(reEnviron, line, nil))
	add("env-cmd", envCmd(line))
	add("credentials-file", credFile.MatchString(line))
	add("credentials", wordMatch(reCredentials, line, nil))
	add("api-key", wordMatch(apiKeyAlt, line, nil))
	return found
}

// leadingWord is a match of re with a Unicode word boundary at its start only.
func leadingWord(re *regexp.Regexp, s string) bool {
	for _, loc := range re.FindAllStringIndex(s, -1) {
		if prevNotWord(s, loc[0]) {
			return true
		}
	}
	return false
}

// envCmd is `^\s*env\b|\benv\s*[|>]` (no IGNORECASE).
func envCmd(line string) bool {
	t := strings.TrimLeftFunc(line, isPySpace)
	if strings.HasPrefix(t, "env") && nextNotWord(t, 3) {
		return true
	}
	for off := 0; ; {
		i := strings.Index(line[off:], "env")
		if i < 0 {
			return false
		}
		i += off
		off = i + 1
		if !prevNotWord(line, i) {
			continue
		}
		rest := strings.TrimLeftFunc(line[i+3:], isPySpace)
		if rest != "" && (rest[0] == '|' || rest[0] == '>') {
			return true
		}
	}
}

// Prescreen scans every text file under path and returns its hits, sorted by (file, line, pattern).
func Prescreen(path, ownRepo string) ([]Hit, error) {
	var hits []Hit
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		hits = append(hits, screenText(filepath.ToSlash(rel), d.Name(), decodeReplace(raw), ownRepo)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortHits(hits)
	return hits, nil
}

func sortHits(h []Hit) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].File != h[j].File {
			return h[i].File < h[j].File
		}
		if h[i].Line != h[j].Line {
			return h[i].Line < h[j].Line
		}
		return h[i].Pattern < h[j].Pattern
	})
}

// ScreenText screens one file's text; rel is its path relative to the screened root, name its base name.
func ScreenText(rel, text, ownRepo string) []Hit {
	h := screenText(rel, rel[strings.LastIndex(rel, "/")+1:], text, ownRepo)
	sortHits(h)
	return h
}

func screenText(rel, name, text, ownRepo string) []Hit {
	var lines []string
	for _, x := range pySplitlines(text) {
		lines = append(lines, fold(x))
	}
	wrapped := map[int]bool{}
	joinedText := strings.Join(lines, "\n")
	for _, m := range b64Wrapped(joinedText) {
		joined := removeSpace(joinedText[m[0]:m[1]])
		if runeLen(joined) >= 200 && distinct(joined) > 8 && hasAny(joined, "0123456789+/=") {
			wrapped[strings.Count(joinedText[:m[0]], "\n")] = true
		}
	}
	for k := range smallChunkBlobs(lines) {
		wrapped[k] = true
	}
	suffix := strings.ToLower(pathSuffix(name))
	licenseFile := licenseRe.MatchString(name) && (suffix == "" || suffix == ".md" || suffix == ".txt")
	prose := suffix == ".md" || suffix == ".txt"
	var emails map[string]bool
	if name == "plugin.json" {
		emails = pluginJSONEmails(text)
	}
	var hits []Hit
	for i, line := range lines {
		ln := i + 1
		found := patternHits(line, prose)
		blob := wrapped[ln-1]
		for _, m := range b64Run.FindAllString(line, -1) {
			if distinct(m) > 8 {
				blob = true
			}
		}
		if blob {
			found = append(found, "base64-run") // not a rule of dashes/equals
		}
		bare := line
		for addr := range emails { // only the whole `"email": "addr"` / `"support": "addr"` line is exempt
			re := regexp.MustCompile(`^[` + wsClass + `]*"(?:email|support)"[` + wsClass + `]*:[` + wsClass + `]*"` + regexp.QuoteMeta(addr) + `"[` + wsClass + `]*,?[` + wsClass + `]*$`)
			if re.MatchString(line) {
				bare = ""
			}
		}
		urlBad := false
		for _, m := range urlRe2.FindAllString(line, -1) {
			u := strings.TrimRight(m, ".,;:")
			if !urlAllowed(u, ownRepo) && !(licenseFile && apacheURL.MatchString(u)) {
				urlBad = true
			}
		}
		if urlBad || bareHostBad(bare, ownRepo) {
			found = append(found, "url")
		}
		for _, id := range found {
			hits = append(hits, Hit{rel, ln, id})
		}
	}
	return hits
}
