package guard

import (
	"strings"
	"unicode/utf8"
)

// RepoProtBlock is worker-guard.py:RepoProtBlock, a PrivBlock: gh commands that change branch protection or
// repository settings (R-a, T-0638), every role.
type RepoProtBlock struct{ Msg string }

func (b *RepoProtBlock) Error() string { return b.Msg }

var (
	ghValueOpts = set("-H", "--header", "-q", "--jq", "-t", "--template", "--hostname", "--cache", "-R", "--repo",
		"-f", "--raw-field", "-F", "--field", "--input", "-X", "--method", "-p", "--preview")
	ghMutating  = set("PUT", "PATCH", "POST", "DELETE")
	ghRepoVerbs = set("edit", "rename", "archive", "unarchive", "delete")
)

// ghRepoChange is a port of gh_repo_change: why `gh <args>` changes branch protection or repository settings (R-a,
// T-0638), else "": `gh repo edit|rename|archive|unarchive|delete`, and `gh api` with a mutating method (or a body,
// which makes gh use POST) on repos/<o>/<r>, its branches/*/protection or its rulesets. A read (GET, no body) is fine.
func ghRepoChange(args []string) string {
	var pos []string
	method, hasMethod, body := "", false, false
	note := func(opt, val string) {
		switch opt {
		case "-X", "--method":
			method, hasMethod = strings.ToUpper(val), true
		case "-f", "--raw-field", "-F", "--field", "--input":
			body = true
		}
	}
	next := func(j int) string {
		if j+1 < len(args) {
			return args[j+1]
		}
		return ""
	}
	for j := 0; j < len(args); j++ {
		x := args[j]
		switch {
		case strings.HasPrefix(x, "--") && utf8.RuneCountInString(x) > 2:
			opt, val, eq := strings.Cut(x, "=")
			if in(ghValueOpts, opt) {
				if !eq {
					val = next(j)
					j++
				}
				note(opt, val)
			}
		case strings.HasPrefix(x, "-") && utf8.RuneCountInString(x) > 1: // a short-flag cluster as pflag reads it: -iX DELETE, -XPUT, -X=PUT
			for k, r := range x {
				if k == 0 {
					continue
				}
				opt := "-" + string(r)
				if !in(ghValueOpts, opt) {
					continue
				}
				val := x[k+utf8.RuneLen(r):]
				if strings.HasPrefix(val, "=") {
					val = val[1:]
				} else if val == "" {
					val = next(j)
					j++
				}
				note(opt, val)
				break
			}
		default:
			pos = append(pos, x)
		}
	}
	if len(pos) > 1 && pos[0] == "repo" && in(ghRepoVerbs, pos[1]) {
		return "gh repo " + pos[1]
	}
	if len(pos) > 1 && pos[0] == "alias" && (pos[1] == "set" || pos[1] == "import") { // a stored alias would run the change later, unseen
		return "gh " + pos[0] + " " + pos[1]
	}
	if len(pos) < 2 || pos[0] != "api" {
		return ""
	}
	endpoint := strings.ToLower(pyUnquote(stripHashComment(pos[1])))
	if endpoint == "graphql" { // mutations of protection or settings; a query read from a file cannot be inspected
		if ghGraphqlSettings(strings.Join(args, " ")) || anyOf(args, func(x string) bool { return strings.Contains(x, "=@") || strings.Contains(x, "--input") }) {
			return "gh api graphql (settings mutation or a query read from a file)"
		}
		return ""
	}
	if !hasMethod && !body {
		return ""
	}
	if hasMethod && !in(ghMutating, method) {
		return ""
	}
	if ghProtected(endpoint) || ghRepoRoot(endpoint) {
		m := method
		if !hasMethod || method == "" {
			m = "POST"
		}
		return "gh api " + m + " " + endpoint
	}
	return ""
}

// stripHashComment is re.sub(r"#.*", "", s): from each # to the end of its line (`.` does not match a newline).
func stripHashComment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// pyUnquote is urllib.parse.unquote: %XX runs are decoded as UTF-8 with errors="replace".
func pyUnquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var out strings.Builder
	var run []byte
	flush := func() {
		if len(run) > 0 {
			out.WriteString(replaceInvalidUTF8(run))
			run = run[:0]
		}
	}
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			run = append(run, hexVal(s[i+1])<<4|hexVal(s[i+2]))
			i += 3
			continue
		}
		flush()
		out.WriteByte(s[i])
		i++
	}
	flush()
	return out.String()
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
func hexVal(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// ghGraphqlSettings is GH_GRAPHQL_SETTINGS.search(text) (re.I):
// (?:update|create|delete)(?:BranchProtectionRule|Repository|RepositoryRuleset)\b|(?:un)?archiveRepository\b
func ghGraphqlSettings(text string) bool {
	l := strings.ToLower(text)
	atWordEnd := func(i int) bool { return !wordAt(l, i) }
	for i := 0; i < len(l); i++ {
		for _, p := range []string{"update", "create", "delete"} {
			if !hasAt(l, i, p) {
				continue
			}
			for _, t := range []string{"branchprotectionrule", "repositoryruleset", "repository"} {
				if hasAt(l, i+len(p), t) && atWordEnd(i+len(p)+len(t)) {
					return true
				}
			}
		}
		for _, p := range []string{"unarchiverepository", "archiverepository"} {
			if hasAt(l, i, p) && atWordEnd(i+len(p)) {
				return true
			}
		}
	}
	return false
}

// atEnd is the regex `$` after position i: the end of s, or a final newline.
func atEnd(s string) bool { return s == "" || s == "\n" }

// ghRepoStarts is the index after each (?:^|/)repos/ in s.
func ghRepoStarts(s string) []int {
	var out []int
	for from := 0; ; {
		k := strings.Index(s[from:], "repos/")
		if k < 0 {
			return out
		}
		p := from + k
		if p == 0 || s[p-1] == '/' {
			out = append(out, p+len("repos/"))
		}
		from = p + 1
	}
}

// ghName is [^/\s]+ at i (excludeQ: [^/\s?]+): the end of the run, or -1 when it is empty.
func ghName(s string, i int, excludeQ bool) int {
	j := i
	for j < len(s) {
		r, n := utf8.DecodeRuneInString(s[j:])
		if r == '/' || pyIsSpace(r) || (excludeQ && r == '?') {
			break
		}
		j += n
	}
	if j == i {
		return -1
	}
	return j
}

// ghProtected is GH_PROTECTED.search(endpoint).
func ghProtected(endpoint string) bool {
	for _, p := range ghRepoStarts(endpoint) {
		o := ghName(endpoint, p, false)
		if o < 0 || o >= len(endpoint) || endpoint[o] != '/' {
			continue
		}
		r := ghName(endpoint, o+1, false)
		if r < 0 || r >= len(endpoint) || endpoint[r] != '/' {
			continue
		}
		rest := endpoint[r+1:]
		if tail, ok := strings.CutPrefix(rest, "branches/"); ok {
			line := tail
			if k := strings.IndexByte(line, '\n'); k >= 0 {
				line = line[:k]
			}
			for from := 1; from <= len(line); { // .+ takes at least one character
				k := strings.Index(line[from:], "protection")
				if k < 0 {
					break
				}
				e := from + k + len("protection")
				after := tail[e:]
				if after == "" || strings.HasPrefix(after, "/") || strings.HasPrefix(after, "?") || after == "\n" {
					return true
				}
				from += k + 1
			}
		}
		if tail, ok := strings.CutPrefix(rest, "rulesets"); ok &&
			(tail == "" || strings.HasPrefix(tail, "/") || strings.HasPrefix(tail, "?") || tail == "\n") {
			return true
		}
		if strings.HasPrefix(rest, "rules/branches") {
			return true
		}
	}
	return false
}

// ghRepoRoot is GH_REPO_ROOT.search(endpoint): (?:^|/)repos/[^/\s]+/[^/\s?]+/?(?:\?.*)?$
func ghRepoRoot(endpoint string) bool {
	for _, p := range ghRepoStarts(endpoint) {
		o := ghName(endpoint, p, false)
		if o < 0 || o >= len(endpoint) || endpoint[o] != '/' {
			continue
		}
		r := ghName(endpoint, o+1, true)
		if r < 0 {
			continue
		}
		t := strings.TrimPrefix(endpoint[r:], "/")
		if strings.HasPrefix(t, "?") {
			if k := strings.IndexByte(t, '\n'); k >= 0 {
				t = t[k:]
			} else {
				t = ""
			}
		}
		if atEnd(t) {
			return true
		}
	}
	return false
}
