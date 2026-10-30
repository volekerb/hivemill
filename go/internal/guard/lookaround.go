package guard

import "strings"

// Hand-written matchers for the Python regexes of worker-guard.py that use a lookaround
// ((?<!..) (?<=..) (?!..) (?=..)), which RE2 does not have. \w, \d, \s and \b of a Python str
// pattern are Unicode-aware, so the matchers use isWord / isDecimal / pyIsSpace and boundary
// (never the ASCII classes of Go's regexp). The table tests in lookaround_test.go take their
// expected values from the Python regexes.

func asciiWord(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// hasAt reports whether s has lit at byte index i.
func hasAt(s string, i int, lit string) bool { return i >= 0 && strings.HasPrefix(s[i:], lit) }

// skipSpace returns the index after the run of \s characters starting at i.
func skipSpace(s string, i int) int {
	for i < len(s) {
		r := nextRune(s, i)
		if !pyIsSpace(r) {
			break
		}
		i += runeLen(s, i)
	}
	return i
}

// isSpaceAt reports whether the rune at i is \s.
func isSpaceAt(s string, i int) bool { r := nextRune(s, i); return r >= 0 && pyIsSpace(r) }

// FindPrivCode ports PRIV_CODE (worker-guard.py:99), the regex
//
//	(?<![\w./-])(?:sudo|doas|pkexec|docker)(?![\w-])|\bsu\b(?=\s+-|\s+root\b)|["']su["']
//
// and returns the leftmost match text (group 0) like PRIV_CODE.search(text).group(0).
func FindPrivCode(s string) (string, bool) {
	for i := 0; i <= len(s); i++ {
		if i < len(s) {
			if p := prevRune(s, i); !(p >= 0 && (isWord(p) || p == '.' || p == '/' || p == '-')) {
				for _, w := range []string{"sudo", "doas", "pkexec", "docker"} {
					if hasAt(s, i, w) {
						if n := nextRune(s, i+len(w)); !(n >= 0 && (isWord(n) || n == '-')) {
							return w, true
						}
					}
				}
			}
			if hasAt(s, i, "su") && boundary(s, i) && boundary(s, i+2) {
				j := skipSpace(s, i+2)
				if j > i+2 {
					if hasAt(s, j, "-") {
						return "su", true
					}
					if hasAt(s, j, "root") && boundary(s, j+4) {
						return "su", true
					}
				}
			}
			if (s[i] == '"' || s[i] == '\'') && hasAt(s, i+1, "su") && i+3 < len(s) && (s[i+3] == '"' || s[i+3] == '\'') {
				return s[i : i+4], true
			}
		}
	}
	return "", false
}

// FindPrivSubst ports the regex of worker-guard.py:priv_subst (line 397),
// (?<![\w.-])(sudo|su|doas|pkexec|docker)(?![\w-]), returning group 1.
func FindPrivSubst(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if p := prevRune(s, i); p >= 0 && (isWord(p) || p == '.' || p == '-') {
			continue
		}
		for _, w := range []string{"sudo", "su", "doas", "pkexec", "docker"} {
			if hasAt(s, i, w) {
				if n := nextRune(s, i+len(w)); !(n >= 0 && (isWord(n) || n == '-')) {
					return w, true
				}
			}
		}
	}
	return "", false
}

var persistNames = []string{"gitconfig", "bashrc", "zshrc", "zshenv", "profile", "bash_profile", "zprofile"}

// hasPersistCode ports PERSIST_CODE (worker-guard.py:151-152):
//
//	(?<![\w-])\.(?:gitconfig|bashrc|zshrc|zshenv|profile|bash_profile|zprofile)(?![\w.-])|\.config/git(?![\w-])|/etc/gitconfig
func hasPersistCode(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			if p := prevRune(s, i); !(p >= 0 && (isWord(p) || p == '-')) {
				for _, n := range persistNames {
					if hasAt(s, i+1, n) {
						if r := nextRune(s, i+1+len(n)); !(r >= 0 && (isWord(r) || r == '.' || r == '-')) {
							return true
						}
					}
				}
			}
			if hasAt(s, i, ".config/git") {
				if r := nextRune(s, i+len(".config/git")); !(r >= 0 && (isWord(r) || r == '-')) {
					return true
				}
			}
		}
		if hasAt(s, i, "/etc/gitconfig") {
			return true
		}
	}
	return false
}

// PersistCode ports worker-guard.py:persist_code: the PERSIST_CODE search on the text and on the text
// with quote and plus characters removed.
func PersistCode(text string) bool {
	return hasPersistCode(text) || hasPersistCode(strings.Map(func(r rune) rune {
		if r == '\'' || r == '"' || r == '+' {
			return -1
		}
		return r
	}, text))
}

// HasCfgEnvAssign ports CFG_ENV_ASSIGN (worker-guard.py:153):
// (?<![\w])(?:GIT_CONFIG\w*|HOME|XDG_CONFIG_HOME)=
func HasCfgEnvAssign(s string) bool {
	for i := 0; i < len(s); i++ {
		if wordBefore(s, i) {
			continue
		}
		if hasAt(s, i, "GIT_CONFIG") {
			j := i + len("GIT_CONFIG")
			for wordAt(s, j) {
				j += runeLen(s, j)
			}
			if hasAt(s, j, "=") {
				return true
			}
		}
		if hasAt(s, i, "HOME=") || hasAt(s, i, "XDG_CONFIG_HOME=") {
			return true
		}
	}
	return false
}

// hasCfgCode ports CFG_CODE (worker-guard.py:225):
// \.config/factory|run/settings|(?<![A-Za-z0-9_])live_home(?![A-Za-z0-9_])   (explicit ASCII classes in the original)
func hasCfgCode(s string) bool {
	if strings.Contains(s, ".config/factory") || strings.Contains(s, "run/settings") {
		return true
	}
	for i := 0; i+len("live_home") <= len(s); i++ {
		if hasAt(s, i, "live_home") {
			p, n := prevRune(s, i), nextRune(s, i+len("live_home"))
			if !(p >= 0 && asciiWord(p)) && !(n >= 0 && asciiWord(n)) {
				return true
			}
		}
	}
	return false
}

// CfgCode ports the test at worker-guard.py:1235: CFG_CODE on the word and on the word with
// whitespace, quotes, plus and comma removed.
func CfgCode(word string) bool {
	return hasCfgCode(word) || hasCfgCode(strings.Map(func(r rune) rune {
		if pyIsSpace(r) || r == '\'' || r == '"' || r == '+' || r == ',' {
			return -1
		}
		return r
	}, word))
}

// HasHerdrWord ports HERDR_WORD (worker-guard.py:268): (?<!tests/stubs/)\bherdr\b
func HasHerdrWord(s string) bool {
	for i := 0; i+5 <= len(s); i++ {
		if herdrAt(s, i) {
			return true
		}
	}
	return false
}

func herdrAt(s string, i int) bool {
	return hasAt(s, i, "herdr") && !(i >= 12 && s[i-12:i] == "tests/stubs/") && boundary(s, i) && boundary(s, i+5)
}

var r1Nouns = []string{"agent", "pane", "tab", "workspace", "worktree"}
var r1Verbs = []string{"start", "stop", "kill", "close", "prompt", "split", "new", "run", "send", "create", "move", "swap", "remove", "rename"}

// MatchR1 ports R1 (worker-guard.py:254-257).search(line):
//
//	(?<!tests/stubs/)\bherdr\b.*?(?:\bserver\s+\S|\b(?:agent|pane|tab|workspace|worktree)["'\\\s]+(?:start|stop|kill|close|prompt|split|new|run|send|create|move|swap|remove|rename)\b)
//
// (`.` does not match a newline; the mutating sequence itself may continue past one).
func MatchR1(s string) bool {
	for i := 0; i+5 <= len(s); i++ {
		if !herdrAt(s, i) {
			continue
		}
		eol := len(s)
		if k := strings.IndexByte(s[i+5:], '\n'); k >= 0 {
			eol = i + 5 + k
		}
		for p := i + 5; p <= eol; p++ {
			if r1Sequence(s, p) {
				return true
			}
		}
	}
	return false
}

func r1Sequence(s string, p int) bool {
	if !boundary(s, p) {
		return false
	}
	if hasAt(s, p, "server") {
		j := skipSpace(s, p+6)
		if j > p+6 && j < len(s) { // \s+ then a non-space character (skipSpace stopped at one)
			return true
		}
	}
	for _, noun := range r1Nouns {
		if !hasAt(s, p, noun) {
			continue
		}
		j := p + len(noun)
		k := j
		for k < len(s) && (s[k] == '"' || s[k] == '\'' || s[k] == '\\' || isSpaceAt(s, k)) {
			k += runeLen(s, k)
		}
		if k == j {
			continue
		}
		for _, v := range r1Verbs {
			if hasAt(s, k, v) && boundary(s, k+len(v)) {
				return true
			}
		}
	}
	return false
}

// MatchR3Redir ports R3_REDIR (worker-guard.py:267).search(s):
//
//	<\s*["']?(?!\S*tests/stubs/)\S*(?:/herdr\b|\$\{?HERDR_BIN_PATH)
func MatchR3Redir(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		p := skipSpace(s, i+1)
		starts := []int{p}
		if p < len(s) && (s[p] == '"' || s[p] == '\'') {
			starts = append(starts, p+1) // the optional quote taken, or not
		}
		for _, st := range starts {
			end := st
			for end < len(s) && !isSpaceAt(s, end) {
				end += runeLen(s, end)
			}
			run := s[st:end]
			if strings.Contains(run, "tests/stubs/") {
				continue
			}
			if strings.Contains(run, "$HERDR_BIN_PATH") || strings.Contains(run, "${HERDR_BIN_PATH") {
				return true
			}
			for k := strings.Index(run, "/herdr"); k >= 0; {
				if boundary(s, st+k+len("/herdr")) {
					return true
				}
				nx := strings.Index(run[k+1:], "/herdr")
				if nx < 0 {
					break
				}
				k += 1 + nx
			}
		}
	}
	return false
}

// BlankRedirectFds ports re.sub(r"(?<![\w$])\d+(?=[<>])", " ", text) at worker-guard.py:989.
func BlankRedirectFds(text string) string {
	var b strings.Builder
	rs := []rune(text)
	for i := 0; i < len(rs); {
		if isDecimal(rs[i]) && !(i > 0 && (isWord(rs[i-1]) || rs[i-1] == '$')) {
			j := i
			for j < len(rs) && isDecimal(rs[j]) {
				j++
			}
			if j < len(rs) && (rs[j] == '<' || rs[j] == '>') {
				b.WriteByte(' ')
			} else {
				b.WriteString(string(rs[i:j]))
			}
			i = j
			continue
		}
		b.WriteRune(rs[i])
		i++
	}
	return b.String()
}

// ExpandHome ports re.sub(r"^(~|\$HOME|\$\{HOME\})(?=/|$)", PW_DIR, w) (worker-guard.py:1229 and 3055).
// The `$` of the lookahead also matches just before a final newline.
func ExpandHome(w, home string) string {
	for _, p := range []string{"~", "$HOME", "${HOME}"} {
		if strings.HasPrefix(w, p) {
			rest := w[len(p):]
			if rest == "" || rest == "\n" || rest[0] == '/' {
				return home + rest
			}
		}
	}
	return w
}

// MatchEvalHomePrefix ports EVAL_HOME_PREFIX (worker-guard.py:2243).match(cmd):
//
//	\A\s*FACTORY_HOME=(?:"[^"$`\\]*"|'[^']*'|[^\s;&|()<>`$"'\\]+)\s+factory(?=\s)
func MatchEvalHomePrefix(cmd string) bool {
	i := skipSpace(cmd, 0)
	if !hasAt(cmd, i, "FACTORY_HOME=") {
		return false
	}
	i += len("FACTORY_HOME=")
	var ends []int
	switch {
	case hasAt(cmd, i, `"`):
		j := i + 1
		for j < len(cmd) && !strings.ContainsRune("\"$`\\", rune(cmd[j])) {
			j++
		}
		if hasAt(cmd, j, `"`) {
			ends = append(ends, j+1)
		}
	case hasAt(cmd, i, "'"):
		if k := strings.IndexByte(cmd[i+1:], '\''); k >= 0 {
			ends = append(ends, i+1+k+1)
		}
	}
	j := i
	for j < len(cmd) && !strings.ContainsRune(";&|()<>`$\"'\\", rune(cmd[j])) && !isSpaceAt(cmd, j) {
		j += runeLen(cmd, j)
	}
	if j > i {
		ends = append(ends, j)
	}
	for _, e := range ends {
		k := skipSpace(cmd, e)
		if k > e && hasAt(cmd, k, "factory") && isSpaceAt(cmd, k+len("factory")) {
			return true
		}
	}
	return false
}

// ───────── \b / \w helpers for the regexes of preprocess (worker-guard.py:~700) ─────────

// hasWord ports re.search(r"\b(a|b|...)\b", text) for whole words: some maximal \w+ run of text is in
// the set or among extra.
func hasWord(text string, words map[string]struct{}, extra ...string) bool {
	for _, run := range splitWords(text, false) {
		if in(words, run) {
			return true
		}
		for _, e := range extra {
			if run == e {
				return true
			}
		}
	}
	return false
}

// wordDotRuns is re.findall(r"[\w.]+", text).
func wordDotRuns(text string) []string { return splitWords(text, true) }

func splitWords(text string, dot bool) []string {
	var runs []string
	start := -1
	for i, r := range text {
		if isWord(r) || dot && r == '.' {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			runs = append(runs, text[start:i])
			start = -1
		}
	}
	if start >= 0 {
		runs = append(runs, text[start:])
	}
	return runs
}

// interpreterFull ports INTERPRETERS (worker-guard.py:228).search / .match:
// ^(python[\d.]*|perl|ruby|node|nodejs|php|lua|deno|bun)$   (`$` also matches before a final newline)
func interpreterFull(s string) bool {
	s = strings.TrimSuffix(s, "\n")
	switch s {
	case "perl", "ruby", "node", "nodejs", "php", "lua", "deno", "bun":
		return true
	}
	if rest, ok := strings.CutPrefix(s, "python"); ok {
		for _, r := range rest {
			if r != '.' && !isDecimal(r) {
				return false
			}
		}
		return true
	}
	return false
}

// hasInterpreterWord ports re.search(r"\b(python[\d.]*|perl|ruby|node|php|lua)\b", text).
func hasInterpreterWord(text string) bool {
	for i := 0; i < len(text); i++ {
		if !boundary(text, i) || wordBefore(text, i) {
			continue
		}
		for _, w := range []string{"perl", "ruby", "node", "php", "lua"} {
			if hasAt(text, i, w) && boundary(text, i+len(w)) {
				return true
			}
		}
		if hasAt(text, i, "python") {
			ends := []int{i + 6}
			for j := i + 6; j < len(text); {
				r := nextRune(text, j)
				if r != '.' && !isDecimal(r) {
					break
				}
				j += runeLen(text, j)
				ends = append(ends, j)
			}
			for _, e := range ends {
				if boundary(text, e) {
					return true
				}
			}
		}
	}
	return false
}
