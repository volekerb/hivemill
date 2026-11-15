package guard

import (
	"fmt"
	"strings"
)

// A small argparse-alike for the CI tools: long options with no value, one value, an optional value or one or more
// values, "--opt=value", repeatable options, positionals. allow_abbrev is off in the Python scripts, so a prefix of
// an option is not an option. Unknown options and missing values are errors (exit 2, as argparse).

const (
	ciArgFlag = iota // --opt
	ciArgOne         // --opt VALUE
	ciArgOpt         // --opt [VALUE]  (nargs "?")
	ciArgMany        // --opt VALUE [VALUE ...]  (nargs "+")
)

type ciArgSpec map[string]int

// ciParsed holds every occurrence of every option (in order) and the positionals.
type ciParsed struct {
	occ map[string][][]string // option -> one value list per occurrence ("?" without value: empty list)
	pos []string
}

func (p *ciParsed) has(name string) bool { return len(p.occ[name]) > 0 }

// last is the value of the last occurrence of a one-value option, or "".
func (p *ciParsed) last(name string) (string, bool) {
	o := p.occ[name]
	if len(o) == 0 || len(o[len(o)-1]) == 0 {
		return "", false
	}
	v := o[len(o)-1]
	return v[0], true
}

// all is the flattened values of every occurrence.
func (p *ciParsed) all(name string) []string {
	var out []string
	for _, o := range p.occ[name] {
		out = append(out, o...)
	}
	return out
}

func ciLooksLikeOption(tok string) bool { return len(tok) > 1 && strings.HasPrefix(tok, "-") }

// ciParseArgs parses args against spec. stopAtPositional stops at the first positional and returns the rest.
func ciParseArgs(args []string, spec ciArgSpec, stopAtPositional bool) (*ciParsed, []string, error) {
	p := &ciParsed{occ: map[string][][]string{}}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if !ciLooksLikeOption(tok) {
			if stopAtPositional {
				return p, args[i:], nil
			}
			p.pos = append(p.pos, tok)
			continue
		}
		name, val, hasVal := tok, "", false
		if strings.HasPrefix(tok, "--") {
			if k := strings.IndexByte(tok, '='); k >= 0 {
				name, val, hasVal = tok[:k], tok[k+1:], true
			}
		}
		kind, ok := spec[name]
		if !ok {
			return nil, nil, fmt.Errorf("unrecognized arguments: %s", tok)
		}
		switch kind {
		case ciArgFlag:
			if hasVal {
				return nil, nil, fmt.Errorf("argument %s: ignored explicit argument %s", name, pyRepr(val))
			}
			p.occ[name] = append(p.occ[name], nil)
		case ciArgOne, ciArgOpt, ciArgMany:
			var vals []string
			switch {
			case hasVal:
				vals = []string{val}
			case kind == ciArgOne:
				if i+1 >= len(args) || ciLooksLikeOption(args[i+1]) {
					return nil, nil, fmt.Errorf("argument %s: expected one argument", name)
				}
				i++
				vals = []string{args[i]}
			default:
				for i+1 < len(args) && !ciLooksLikeOption(args[i+1]) {
					i++
					vals = append(vals, args[i])
					if kind == ciArgOpt {
						break
					}
				}
				if kind == ciArgMany && len(vals) == 0 {
					return nil, nil, fmt.Errorf("argument %s: expected at least one argument", name)
				}
			}
			p.occ[name] = append(p.occ[name], vals)
		}
	}
	return p, nil, nil
}
