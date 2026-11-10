package guard

import (
	"regexp"
	"strings"
)

// G-036 SC3 (worker-guard.py): the retro role may not push, merge, run most factory commands, or edit live roles/.

const retroRole = "retro"

const retroRoleWhy = "the retro agent may not edit files under <live>/roles/; role prompts and hooks are changed only " +
	"by the owner and the tick"

var (
	retroFactoryCmds = set("whoami", "inbox", "done", "retro")
	reRetroFactory   = regexp.MustCompile(`(?:^|[/])(?:[^` + gitSp + `]+/)?factory/roles(?:/|$)`)
)

func retroRolesTrees() []string {
	h := mustHost()
	out := make([]string, 0, len(h.liveHomes))
	for _, home := range h.liveHomes {
		out = append(out, pyJoin(home, "roles"))
	}
	return out
}

func retroRolesCode(text string) bool {
	norm := strings.ReplaceAll(text, "\\", "/")
	for _, tree := range retroRolesTrees() {
		rel := strings.ReplaceAll(tree, "\\", "/")
		if strings.Contains(norm, rel) {
			return true
		}
	}
	for _, home := range mustHost().liveHomes {
		hr := regexp.QuoteMeta(strings.ReplaceAll(home, "\\", "/"))
		if !regexp.MustCompile(hr).MatchString(norm) {
			continue
		}
		if regexp.MustCompile(hr + `\)?/roles(?:/|$)`).MatchString(norm) {
			return true
		}
		if regexp.MustCompile(hr + `/rol[^;]*;[^)]*es\)`).MatchString(norm) {
			return true
		}
		if regexp.MustCompile(`%s/roles(?:/|$)`).MatchString(norm) {
			return true
		}
	}
	if strings.Contains(norm, "tory/roles") {
		return true
	}
	return reRetroFactory.MatchString(norm)
}

var reRetroPathStrip = regexp.MustCompile(`^>[>|]?[` + gitSp + `]*`)

func retroPathExpr(text string) string {
	s := strings.TrimFunc(text, pyIsSpace)
	s = reRetroPathStrip.ReplaceAllString(s, "")
	if len(s) >= 2 && s[0] == s[len(s)-1] && (s[0] == '"' || s[0] == '\'') {
		s = s[1 : len(s)-1]
	}
	return s
}

func retroQuoteUnquote(word string) string {
	s := strings.TrimFunc(word, pyIsSpace)
	if len(s) >= 2 && s[0] == s[len(s)-1] && (s[0] == '"' || s[0] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

func retroSplitShellWords(text string) []string {
	var words []string
	var cur []byte
	var quote *byte
	i, n := 0, len(text)
	for i < n {
		c := text[i]
		if quote != nil && *quote == '\'' {
			cur = append(cur, c)
			if c == '\'' {
				quote = nil
			}
			i++
			continue
		}
		if quote != nil && *quote == '"' {
			if c == '\\' && i+1 < n {
				cur = append(cur, c)
				i++
				c = text[i]
			}
			cur = append(cur, c)
			if c == '"' {
				quote = nil
			}
			i++
			continue
		}
		if c == '\'' {
			q := byte('\'')
			quote = &q
			cur = append(cur, c)
			i++
			continue
		}
		if c == '"' {
			q := byte('"')
			quote = &q
			cur = append(cur, c)
			i++
			continue
		}
		if c == ';' || c == '|' || c == '&' || c == '\n' {
			if len(cur) > 0 {
				words = append(words, string(cur))
				cur = nil
			}
			i++
			continue
		}
		if c == ' ' || c == '\t' {
			if len(cur) > 0 {
				words = append(words, string(cur))
				cur = nil
			}
			i++
			continue
		}
		cur = append(cur, c)
		i++
	}
	if len(cur) > 0 {
		words = append(words, string(cur))
	}
	return words
}

// retroPrintfSegment returns the static result of one printf segment. ok is false when the segment is
// not printf-only (Python _retro_printf_segment returns None); ok is true with an empty string when
// printf %s has no payload (Python returns "").
func retroPrintfSegment(text string) (got string, ok bool) {
	words := retroSplitShellWords(strings.TrimFunc(text, pyIsSpace))
	if len(words) < 2 || words[0] != "printf" {
		return "", false
	}
	args := make([]string, 0, len(words)-1)
	for _, w := range words[1:] {
		args = append(args, retroQuoteUnquote(w))
	}
	if len(args) == 0 {
		return "", false
	}
	fmt, rest := args[0], args[1:]
	if fmt == "%s" {
		return strings.Join(rest, ""), true
	}
	if fmt == "%s/%s" && len(rest) == 2 {
		return strings.TrimRight(rest[0], "/") + "/" + strings.TrimLeft(rest[1], "/"), true
	}
	return "", false
}

func retroResolvePathExpr(expr string) (string, bool) {
	if expr == "" {
		return "", false
	}
	s := strings.TrimFunc(expr, pyIsSpace)
	unwrapped := false
	if strings.HasPrefix(s, "$(") && strings.HasSuffix(s, ")") {
		s = s[2 : len(s)-1]
		unwrapped = true
	} else if len(s) >= 2 && s[0] == s[len(s)-1] && s[0] == '`' {
		s = s[1 : len(s)-1]
		unwrapped = true
	}
	if !unwrapped {
		if !strings.ContainsAny(s, "$`") {
			return s, true
		}
		return "", false
	}
	var parts []string
	var seg []byte
	var quote *byte
	depth, i, n := 0, 0, len(s)
	for i < n {
		c := s[i]
		if quote != nil && *quote == '\'' {
			seg = append(seg, c)
			if c == '\'' {
				quote = nil
			}
			i++
			continue
		}
		if quote != nil && *quote == '"' {
			if c == '\\' && i+1 < n {
				seg = append(seg, c)
				i++
				c = s[i]
			}
			seg = append(seg, c)
			if c == '"' {
				quote = nil
			}
			i++
			continue
		}
		if c == '\'' {
			q := byte('\'')
			quote = &q
			seg = append(seg, c)
			i++
			continue
		}
		if c == '"' {
			q := byte('"')
			quote = &q
			seg = append(seg, c)
			i++
			continue
		}
		if c == '(' {
			depth++
			seg = append(seg, c)
			i++
			continue
		}
		if c == ')' {
			depth--
			seg = append(seg, c)
			i++
			continue
		}
		if c == ';' && depth == 0 && quote == nil {
			parts = append(parts, string(seg))
			seg = nil
			i++
			continue
		}
		seg = append(seg, c)
		i++
	}
	parts = append(parts, string(seg))
	var out []string
	for _, p := range parts {
		got, segOk := retroPrintfSegment(p)
		if !segOk {
			return "", false
		}
		out = append(out, got)
	}
	merged := strings.Join(out, "")
	if merged == "" || strings.Contains(merged, "\n") {
		return "", false
	}
	return merged, true
}

func retroRolesSubstResolved(resolved, text, ccwd string, ccwdKnown bool) bool {
	rp := resolved
	if !strings.HasPrefix(rp, "/") {
		if !ccwdKnown {
			return true
		}
		rp = pyNormpath(pyJoin(ccwd, rp))
	}
	if retroRolesPath(rp, false) {
		return true
	}
	return retroRolesCode(text)
}

// retroRolesSubst is a port of retro_roles_subst.
func retroRolesSubst(text, ccwd string, ccwdKnown bool) bool {
	if strings.ContainsAny(text, "$`") {
		resolved, ok := retroResolvePathExpr(retroPathExpr(text))
		if !ok {
			return true
		}
		return retroRolesSubstResolved(resolved, text, ccwd, ccwdKnown)
	}
	return retroRolesCode(text)
}

var (
	reRetroRedirDQ = regexp.MustCompile(`>[>|]?[` + gitSp + `]*"([^"]*(?:\$\(|` + "`" + `)[^"]*)"`)
	reRetroRedirSQ = regexp.MustCompile(`>[>|]?[` + gitSp + `]*'([^']*(?:\$\(|` + "`" + `)[^']*)'`)
	reRetroRedirUQ = regexp.MustCompile(`>[>|]?[` + gitSp + `]*[^` + gitSp + `;|&"']*(?:\$\(|` + "`" + `)[^;|&\n]*`)
)

// retroSubstRedirectFrags is retro_subst_redirect_frags.
func retroSubstRedirectFrags(text string) []string {
	var frags []string
	for _, m := range reRetroRedirDQ.FindAllStringSubmatch(text, -1) {
		frags = append(frags, `> "`+m[1]+`"`)
	}
	for _, m := range reRetroRedirSQ.FindAllStringSubmatch(text, -1) {
		frags = append(frags, "> '"+m[1]+"'")
	}
	frags = append(frags, reRetroRedirUQ.FindAllString(text, -1)...)
	return frags
}

func retroRolesGlob(p string, ancestor bool) bool {
	pat := strings.Split(strings.Trim(pyNormpath(p), "/"), "/")
	var m func(i, j int, parts []string) bool
	m = func(i, j int, parts []string) bool {
		if j == len(parts) {
			return true
		}
		if i == len(pat) {
			return ancestor
		}
		if pat[i] == "**" {
			return m(i+1, j, parts) || m(i, j+1, parts)
		}
		return fnmatchCase(parts[j], pat[i]) && m(i+1, j+1, parts)
	}
	for _, d := range retroRolesTrees() {
		if m(0, 0, strings.Split(strings.Trim(d, "/"), "/")) {
			return true
		}
	}
	return false
}

func retroRolesPath(p string, spelled bool) bool {
	if spelled || strings.ContainsAny(p, "$`") {
		return retroRolesCode(p)
	}
	for _, t := range braceExpand(p) {
		if hasGlob(t) {
			if retroRolesGlob(t, false) {
				return true
			}
			continue
		}
		cands := map[string]struct{}{pyNormpath(t): {}}
		if rp := pyRealpath(t); rp != "" {
			cands[rp] = struct{}{}
		}
		for c := range cands {
			for _, d := range retroRolesTrees() {
				if c == d || Under(c, d) {
					return true
				}
			}
		}
	}
	return false
}

func (a *Analyzer) retroFactory(sub string, hasSub bool) error {
	if hasSub && in(retroFactoryCmds, sub) {
		return nil
	}
	subShown := sub
	if !hasSub {
		subShown = ""
	}
	return blockf("the retro agent may not run `factory " + subShown + "`; allowed: done, inbox, retro, whoami")
}
