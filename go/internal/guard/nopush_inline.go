package guard

import "strings"

// T-0789 (worker-guard.py:interp_inline_push): NO_PUSH_ROLES may not run inline interpreter code that names `git`
// and `push` as words, or a gh write. Incomplete by design (defense in depth, string obfuscation beats it).

// inlineCodeInterp ports INLINE_CODE_INTERPS.match: ^(python[\d.]*|perl|ruby|node|nodejs)$.
func inlineCodeInterp(name string) bool {
	switch strings.TrimSuffix(name, "\n") {
	case "php", "lua", "deno", "bun":
		return false
	}
	return interpreterFull(name)
}

// hasWordToken reports whether w occurs in s with a \b on both sides (re.search(r"\bw\b"), case-sensitive).
func hasWordToken(s, w string) bool {
	for i := 0; i+len(w) <= len(s); i++ {
		if hasAt(s, i, w) && boundary(s, i) && boundary(s, i+len(w)) {
			return true
		}
	}
	return false
}

// nonWordEnds returns every end index of \W+ starting at i (one per run length >= 1).
func nonWordEnds(s string, i int) []int {
	var ends []int
	for i < len(s) {
		if isWord(nextRune(s, i)) {
			break
		}
		i += runeLen(s, i)
		ends = append(ends, i)
	}
	return ends
}

// runEnds returns every end index of (class)* starting at i, including i itself.
func runEnds(s string, i int, class func(rune) bool) []int {
	ends := []int{i}
	for i < len(s) {
		if !class(nextRune(s, i)) {
			break
		}
		i += runeLen(s, i)
		ends = append(ends, i)
	}
	return ends
}

func isFlagNameRune(r rune) bool { return isWord(r) || r == '-' }

func isFlagValueRune(r rune) bool { return isWord(r) || r == '.' || r == '/' || r == ':' || r == '-' }

// ghFlagGroupEnds ports one iteration of the optional gh global flag group of INLINE_GH_WRITE,
// \W+-{1,2}[A-Za-z][\w-]*(?:\W+[\w./:-]+)?, and returns every position it can end at.
func ghFlagGroupEnds(s string, p int) []int {
	var out []int
	for _, a := range nonWordEnds(s, p) {
		for dashes := 1; dashes <= 2; dashes++ {
			if !hasAt(s, a, strings.Repeat("-", dashes)) {
				continue
			}
			b := a + dashes
			c := nextRune(s, b)
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
				continue
			}
			for _, e := range runEnds(s, b+1, isFlagNameRune) {
				out = append(out, e)
				for _, f := range nonWordEnds(s, e) {
					for _, g := range runEnds(s, f, isFlagValueRune) {
						if g > f {
							out = append(out, g)
						}
					}
				}
			}
		}
	}
	return out
}

// foldWordAt reports whether the text at i is one of words ignoring case ((?i:..) of a str pattern) and ends at a \b.
func foldWordAt(s string, i int, words ...string) bool {
	for _, w := range words {
		j := i
		ok := true
		for _, want := range w {
			if j >= len(s) || !strings.EqualFold(string(nextRune(s, j)), string(want)) {
				ok = false
				break
			}
			j += runeLen(s, j)
		}
		if ok && boundary(s, j) {
			return true
		}
	}
	return false
}

// ghSubVerb reports whether s has gh ... sub W+ verb with a word boundary after verb (INLINE_GH_WRITE tail).
func ghSubVerb(s string, q int, sub string, verbs ...string) bool {
	if !hasAt(s, q, sub) || !boundary(s, q+len(sub)) {
		return false
	}
	for _, e := range nonWordEnds(s, q+len(sub)) {
		for _, v := range verbs {
			if hasAt(s, e, v) && boundary(s, e+len(v)) {
				return true
			}
		}
	}
	return false
}

// ghWriteTail matches what follows `\bgh\b(flags)*?\W+` at q (worker-guard.py:INLINE_GH_WRITE; `gh pr comment` is not included).
func ghWriteTail(s string, q int) bool {
	if ghSubVerb(s, q, "pr", "merge", "create", "new", "edit", "close", "reopen", "ready", "review", "lock", "unlock", "update-branch", "revert") {
		return true
	}
	if ghSubVerb(s, q, "issue", "create", "new", "edit", "close", "reopen", "comment", "delete", "transfer", "pin", "unpin", "lock", "unlock", "develop") {
		return true
	}
	if ghSubVerb(s, q, "workflow", "run", "enable", "disable") {
		return true
	}
	if hasAt(s, q, "repo") && boundary(s, q+4) {
		return true
	}
	if ghSubVerb(s, q, "label", "create") {
		return true
	}
	if ghSubVerb(s, q, "secret", "set") {
		return true
	}
	if ghSubVerb(s, q, "variable", "set") {
		return true
	}
	if ghSubVerb(s, q, "release", "create", "new", "edit", "delete-asset", "delete", "upload") {
		return true
	}
	if hasAt(s, q, "api") && boundary(s, q+3) {
		for m := q + 3; m <= len(s); m++ {
			if r := prevRune(s, m); r >= 0 && isWord(r) {
				continue // (?<!\w)
			}
			for _, flag := range []string{"-X", "--method"} {
				if !hasAt(s, m, flag) {
					continue
				}
				j := m + len(flag)
				for _, e := range append([]int{j}, nonWordEnds(s, j)...) {
					if foldWordAt(s, e, "POST", "PUT", "PATCH", "DELETE") {
						return true
					}
				}
			}
		}
	}
	return false
}

// matchInlineGhWrite ports INLINE_GH_WRITE.search(text) (the regex needs a lookbehind, so it is hand-written).
func matchInlineGhWrite(s string) bool {
	for i := 0; i+2 <= len(s); i++ {
		if !hasAt(s, i, "gh") || !boundary(s, i) || !boundary(s, i+2) {
			continue
		}
		seen := map[int]bool{i + 2: true}
		queue := []int{i + 2}
		for len(queue) > 0 {
			p := queue[0]
			queue = queue[1:]
			for _, q := range nonWordEnds(s, p) {
				if ghWriteTail(s, q) {
					return true
				}
			}
			for _, e := range ghFlagGroupEnds(s, p) {
				if !seen[e] {
					seen[e] = true
					queue = append(queue, e)
				}
			}
		}
	}
	return false
}

// bodyFlagAt reports whether one of -f -F --field --raw-field --input starts at m with (?<![\w-]) before and (?![\w-]) after.
func bodyFlagAt(s string, m int) bool {
	if r := prevRune(s, m); r >= 0 && isFlagNameRune(r) {
		return false
	}
	for _, flag := range []string{"-f", "-F", "--field", "--raw-field", "--input"} {
		if hasAt(s, m, flag) {
			if e := m + len(flag); e >= len(s) || !isFlagNameRune(nextRune(s, e)) {
				return true
			}
		}
	}
	return false
}

// matchInlineGhAPIBody ports INLINE_GH_API_BODY.search(c) for a c that starts at a `gh` word:
// \bgh\b(flags)*?\W+api\b[\s\S]*?<body flag>.
func matchInlineGhAPIBody(s string) bool {
	if !hasAt(s, 0, "gh") || !boundary(s, 2) {
		return false
	}
	seen := map[int]bool{2: true}
	queue := []int{2}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, q := range nonWordEnds(s, p) {
			if hasAt(s, q, "api") && boundary(s, q+3) {
				for m := q + 3; m <= len(s); m++ {
					if bodyFlagAt(s, m) {
						return true
					}
				}
			}
		}
		for _, e := range ghFlagGroupEnds(s, p) {
			if !seen[e] {
				seen[e] = true
				queue = append(queue, e)
			}
		}
	}
	return false
}

// matchInlineGhGet ports INLINE_GH_GET.search: (?<![\w-])(-X|--method)\W*(?i:GET)\b.
func matchInlineGhGet(s string) bool {
	for m := 0; m < len(s); m++ {
		if r := prevRune(s, m); r >= 0 && isFlagNameRune(r) {
			continue
		}
		for _, flag := range []string{"-X", "--method"} {
			if !hasAt(s, m, flag) {
				continue
			}
			j := m + len(flag)
			for _, e := range append([]int{j}, nonWordEnds(s, j)...) {
				if foldWordAt(s, e, "GET") {
					return true
				}
			}
		}
	}
	return false
}

// inlineGhAPIBody ports inline_gh_api_body (T-0881): the text is cut at each `gh` word, so a GET on another gh call
// exempts nothing.
func inlineGhAPIBody(s string) bool {
	var starts []int
	for i := 0; i+2 <= len(s); i++ {
		if hasAt(s, i, "gh") && boundary(s, i) && boundary(s, i+2) {
			starts = append(starts, i)
		}
	}
	for k, st := range starts {
		end := len(s)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		if c := s[st:end]; matchInlineGhAPIBody(c) && !matchInlineGhGet(c) {
			return true
		}
	}
	return false
}

// interpInlinePush is a port of Analyzer.interp_inline_push.
func (a *Analyzer) interpInlinePush(name string, rest []string) error {
	if !inlineCodeInterp(name) {
		return nil
	}
	iname := name
	if name == "nodejs" {
		iname = "node"
	}
	_, ci, unknown := InterpArgs(iname, rest)
	looksLikeCode := func(x string) bool { return hasSpace(x) || strings.ContainsAny(x, "\"'();") }
	var texts []string
	if unknown { // cannot tell which word is the code: look at every code-looking word
		for _, x := range rest {
			if looksLikeCode(x) {
				texts = append(texts, x)
			}
		}
	} else if ci >= 0 {
		texts = []string{rest[ci]}
	}
	// attached code (`perl -e'CODE'`, `node --eval=CODE`) is not a separate word: scan option words that look like code
	for _, x := range rest {
		if strings.HasPrefix(x, "-") && looksLikeCode(x) {
			texts = append(texts, x)
		}
	}
	for _, t := range texts {
		if err := a.scanInlineText(name, t, "inline code"); err != nil {
			return err
		}
	}
	return nil
}
