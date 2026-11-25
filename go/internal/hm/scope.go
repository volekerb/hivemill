package hm

import (
	"fmt"
	"regexp"
	"strings"

	"factory/go/internal/citools/affected"
)

// Scope is the set of files a task declares it may touch. It is read from two places the specs already use:
//   - a scope criterion, a cmd: proof `git diff --name-only ... | grep -v[xE] '<allowed>' => empty` (allow-list), or
//     `... | grep -E '<forbidden>' => empty` (deny-list);
//   - the spec's `FILES:` line, a prose list of paths, globs and directories.
//
// A file is out of scope when it matches a deny pattern, when allow patterns exist and it matches none, or when a
// FILES: list exists and it names none of them.
type Scope struct {
	Allow   []*regexp.Regexp
	Deny    []*regexp.Regexp
	Files   []string // FILES: entries: a path, a glob or a directory (trailing / optional)
	Sources []string // one line per declaration, for the judge packet
	Bad     []string // declarations that could not be read (a regex that does not compile)
}

// Declared reports whether the task declares any scope.
func (s Scope) Declared() bool { return len(s.Allow)+len(s.Deny)+len(s.Files) > 0 }

var (
	filesLineRe = regexp.MustCompile(`(?m)^[ \t]*FILES:[ \t]*(.+)$`)
	scopeTokRe  = regexp.MustCompile(`[\w.\-*\[\]]+(?:/[\w.\-*\[\]]+)*/?`)
	clauseRe    = regexp.MustCompile(`;|\.\s+|\.$`)
	negClauseRe = regexp.MustCompile(`(?i)^\s*(?:no|not|never|nothing|without)\b`)
	nextLabelRe = regexp.MustCompile(`\s[A-Z][A-Za-z]+:\s`) // "... Rules: merge origin/main" starts another section
	grepRe      = regexp.MustCompile(`\bgrep\s+((?:-[A-Za-z]+\s+)*)(?:'([^']*)'|"([^"]*)")`)
)

// DeclaredScope reads the scope a task declares (see Scope).
func DeclaredScope(t *Task) Scope {
	var s Scope
	for _, c := range Criteria(t.Acceptance) {
		if c.Kind != "cmd" || !strings.Contains(c.Proof, "git diff --name-only") || !emptyExpect(c.Expect) {
			continue
		}
		i := strings.Index(c.Proof, "git diff --name-only")
		for _, m := range grepRe.FindAllStringSubmatch(c.Proof[i:], -1) {
			flags := strings.Join(strings.Fields(strings.ReplaceAll(m[1], "-", " ")), "")
			pat := m[2] + m[3]
			if strings.Contains(flags, "F") {
				pat = regexp.QuoteMeta(pat)
			}
			if strings.Contains(flags, "x") {
				pat = "^(?:" + pat + ")$"
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				s.Bad = append(s.Bad, fmt.Sprintf("criterion %d: the pattern %q is not a valid regex (%v)", c.ID, m[2]+m[3], err))
				continue
			}
			if strings.Contains(flags, "v") {
				s.Allow = append(s.Allow, re)
				s.Sources = append(s.Sources, fmt.Sprintf("criterion %d allows only files matching `%s`", c.ID, m[2]+m[3]))
			} else {
				s.Deny = append(s.Deny, re)
				s.Sources = append(s.Sources, fmt.Sprintf("criterion %d forbids files matching `%s`", c.ID, m[2]+m[3]))
			}
		}
	}
	for _, m := range filesLineRe.FindAllStringSubmatch(t.Spec, -1) {
		var got []string
		list := m[1]
		if loc := nextLabelRe.FindStringIndex(list); loc != nil {
			list = list[:loc[0]]
		}
		for _, clause := range clauseRe.Split(list, -1) {
			if negClauseRe.MatchString(clause) {
				continue // "No bin/factory_cli.py or roles/ change" names files that are out of scope, not in it
			}
			for _, tok := range scopeTokRe.FindAllString(clause, -1) {
				if strings.Contains(tok, "/") || strings.Contains(strings.TrimLeft(tok, "."), ".") {
					got = append(got, strings.TrimPrefix(tok, "./"))
				}
			}
		}
		if len(got) > 0 {
			s.Files = append(s.Files, got...)
			s.Sources = append(s.Sources, "spec FILES: "+strings.TrimSpace(list))
		}
	}
	return s
}

func emptyExpect(e string) bool {
	switch strings.ToLower(strings.TrimSpace(e)) {
	case "empty", "no output", "nothing":
		return true
	}
	return false
}

// fileEntryMatch: a FILES: entry names a path when it is that path, a directory holding it, or a glob matching it.
func fileEntryMatch(path, entry string) bool {
	e := strings.TrimSuffix(entry, "/")
	if path == e || strings.HasPrefix(path, e+"/") {
		return true
	}
	return strings.ContainsAny(entry, "*?[") && affected.FnMatch(path, entry)
}

// OutOfScope lists each changed file the declared scope does not cover, with the declaration it falls outside.
func (s Scope) OutOfScope(files []string) []string {
	var out []string
	for _, f := range files {
		var why []string
		for _, re := range s.Deny {
			if re.MatchString(f) {
				why = append(why, "matches the forbidden pattern `"+re.String()+"`")
			}
		}
		if len(s.Allow) > 0 && !anyRe(s.Allow, f) {
			why = append(why, "outside the scope criterion's allow-list")
		}
		if len(s.Files) > 0 && !anyEntry(s.Files, f) {
			why = append(why, "not named on the FILES: line")
		}
		if len(why) > 0 {
			out = append(out, f+" ("+strings.Join(why, "; ")+")")
		}
	}
	return out
}

func anyRe(res []*regexp.Regexp, f string) bool {
	for _, re := range res {
		if re.MatchString(f) {
			return true
		}
	}
	return false
}

func anyEntry(entries []string, f string) bool {
	for _, e := range entries {
		if fileEntryMatch(f, e) {
			return true
		}
	}
	return false
}

// requirement is a criterion's text without its proof (`- cmd:...`): what was asked, not how it is checked.
func requirement(c Criterion) string {
	if m := proofRe.FindStringSubmatchIndex(c.Text); m != nil {
		return strings.TrimRight(strings.TrimSpace(c.Text[:m[0]]), " -–—:")
	}
	return c.Text
}
