package hm

import (
	"path/filepath"
	"regexp/syntax"
	"strings"

	"factory/go/internal/citools/risktier"
)

// Risk decides the model from files, never from words in the task text: the files the task declares in scope (its
// FILES: line and scope criterion, see DeclaredScope) and, once a diff exists, the files it changes, matched against
// sensitive_paths of ci/tier_rules.json in the main checkout; or an explicit tier T3. A task that only mentions a
// sensitive path or word in its prose is not risky. Unreadable rules count as risky for the factory project.
func Risk(home string, t *Task, changed []string) (bool, string) {
	if t.Tier == "T3" {
		return true, "task tier T3"
	}
	rules, err := risktier.LoadRules(filepath.Join(home, "ci", "tier_rules.json"))
	if err != nil {
		if t.Project == "factory" {
			return true, "tier rules unreadable"
		}
		return false, ""
	}
	var hits []string
	for _, f := range changed {
		if sensitivePath(f, rules) {
			hits = append(hits, "changes "+f)
		}
	}
	s := DeclaredScope(t)
	for _, e := range s.Files {
		if scopeEntryRisky(strings.TrimPrefix(e, "./"), rules) {
			hits = append(hits, "scope "+e)
		}
	}
	for _, re := range s.Allow {
		for _, p := range regexPrefixes(re.String()) {
			if (p.full && sensitivePath(p.lit, rules)) || (!p.full && prefixOverlapsSensitive(p.lit, rules)) {
				hits = append(hits, "scope `"+re.String()+"`")
				break
			}
		}
	}
	if len(hits) == 0 {
		return false, ""
	}
	if len(hits) > 3 {
		hits = hits[:3]
	}
	return true, strings.Join(hits, ", ")
}

func sensitivePath(p string, rules risktier.Rules) bool {
	why, _ := risktier.Reasons(map[string][2]string{}, []string{p}, 0, rules)
	return len(why) > 0
}

// scopeEntryRisky: a FILES: entry is risky when it is a sensitive path, a directory holding one, or a glob that can
// match one.
func scopeEntryRisky(e string, rules risktier.Rules) bool {
	if i := strings.IndexAny(e, "*?["); i >= 0 {
		return prefixOverlapsSensitive(e[:i], rules)
	}
	if !strings.HasSuffix(e, "/") && strings.Contains(e[strings.LastIndex(e, "/")+1:], ".") {
		return sensitivePath(e, rules) // a file: only that path, never a directory under it
	}
	e = strings.TrimSuffix(e, "/")
	return sensitivePath(e, rules) || prefixOverlapsSensitive(e+"/", rules)
}

// prefixOverlapsSensitive: some path starting with prefix can match a sensitive_paths glob. Literal prefixes are
// compared, so the answer errs towards risky (an empty prefix overlaps everything).
func prefixOverlapsSensitive(prefix string, rules risktier.Rules) bool {
	for _, g := range rules.SensitivePaths {
		gp := g
		if i := strings.IndexAny(g, "*?["); i >= 0 {
			gp = g[:i]
		}
		if strings.HasPrefix(prefix, gp) || strings.HasPrefix(gp, prefix) {
			return true
		}
	}
	return false
}

type litPrefix struct {
	lit  string
	full bool // the whole match is this literal path
}

// regexPrefixes lists the literal prefix of each path an allow-list regex can match, one per alternative. A regex
// that is not anchored at the start yields the empty prefix, which overlaps everything.
func regexPrefixes(src string) []litPrefix {
	re, err := syntax.Parse(src, syntax.Perl)
	if err != nil {
		return []litPrefix{{"", false}}
	}
	re = re.Simplify()
	if !anchoredStart(re) {
		return []litPrefix{{"", false}}
	}
	out := prefixesOf(re)
	if !strings.HasSuffix(src, "$") {
		for i := range out {
			out[i].full = false
		}
	}
	return out
}

func anchoredStart(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpBeginText, syntax.OpBeginLine:
		return true
	case syntax.OpConcat, syntax.OpCapture:
		return len(re.Sub) > 0 && anchoredStart(re.Sub[0])
	case syntax.OpAlternate:
		for _, s := range re.Sub {
			if !anchoredStart(s) {
				return false
			}
		}
		return len(re.Sub) > 0
	}
	return false
}

const maxPrefixes = 256

func prefixesOf(re *syntax.Regexp) []litPrefix {
	switch re.Op {
	case syntax.OpLiteral:
		return []litPrefix{{string(re.Rune), true}}
	case syntax.OpBeginText, syntax.OpBeginLine, syntax.OpEndText, syntax.OpEndLine, syntax.OpEmptyMatch:
		return []litPrefix{{"", true}}
	case syntax.OpCapture:
		return prefixesOf(re.Sub[0])
	case syntax.OpAlternate:
		var out []litPrefix
		for _, s := range re.Sub {
			out = append(out, prefixesOf(s)...)
		}
		if len(out) > maxPrefixes {
			return []litPrefix{{"", false}}
		}
		return out
	case syntax.OpConcat:
		cur := []litPrefix{{"", true}}
		for _, s := range re.Sub {
			sub := prefixesOf(s)
			var next []litPrefix
			for _, c := range cur {
				if !c.full {
					next = append(next, c)
					continue
				}
				for _, x := range sub {
					next = append(next, litPrefix{c.lit + x.lit, x.full})
				}
			}
			if len(next) > maxPrefixes {
				return []litPrefix{{"", false}}
			}
			cur = next
		}
		return cur
	}
	return []litPrefix{{"", false}}
}
