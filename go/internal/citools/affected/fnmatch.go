package affected

import (
	"regexp"
	"strings"
	"sync"
)

var fnCache sync.Map // pattern -> *regexp.Regexp

// FnMatch is Python's fnmatch.fnmatchcase: `*` matches any run of characters including `/`, `?` one character,
// `[seq]` and `[!seq]` a character class. Case-sensitive.
func FnMatch(path, pat string) bool {
	if v, ok := fnCache.Load(pat); ok {
		return v.(*regexp.Regexp).MatchString(path)
	}
	re, err := regexp.Compile(translate(pat))
	if err != nil {
		return false
	}
	fnCache.Store(pat, re)
	return re.MatchString(path)
}

func translate(pat string) string {
	var b strings.Builder
	b.WriteString(`(?s)\A`)
	i, n := 0, len(pat)
	for i < n {
		c := pat[i]
		i++
		switch c {
		case '*':
			for i < n && pat[i] == '*' {
				i++
			}
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
		case '[':
			j := i
			if j < n && pat[j] == '!' {
				j++
			}
			if j < n && pat[j] == ']' {
				j++
			}
			for j < n && pat[j] != ']' {
				j++
			}
			if j >= n {
				b.WriteString(`\[`)
				continue
			}
			stuff := pat[i:j]
			i = j + 1
			neg := false
			if strings.HasPrefix(stuff, "!") {
				neg = true
				stuff = stuff[1:]
			} else if strings.HasPrefix(stuff, "^") {
				stuff = `\` + stuff
			}
			stuff = strings.NewReplacer(`\`, `\\`, `[`, `\[`).Replace(stuff)
			if neg {
				b.WriteString(`[^` + stuff + `]`)
			} else {
				b.WriteString(`[` + stuff + `]`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`\z`)
	return b.String()
}
