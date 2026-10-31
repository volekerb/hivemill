package guard

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// T-0771 (worker-guard.py): NO_PUSH_ROLES may not assign HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* through a builtin
// either; a port of Analyzer.call_assign and Analyzer.builtin_assign.

const wordClass = `\p{L}\p{N}_`

var (
	reProtectedName = regexp.MustCompile(`^(?:HOME|XDG_CONFIG_HOME|PATH|path|PAGER|GIT_[` + wordClass + `]*)\n?$`) // T-0788: zsh `path` is tied to PATH
	reParamDefault  = regexp.MustCompile(`\$\{(!?[A-Za-z_][` + wordClass + `]*):{0,2}=`)
	reSubscriptAsg  = regexp.MustCompile(`^([A-Za-z_][` + wordClass + `]*)\[[^\]]*\]\+?=`)
	reClusterV      = regexp.MustCompile(`^-[A-Za-z]*v$`)
	reNamedFd       = regexp.MustCompile(`\{([A-Za-z_][` + wordClass + `]*|[^` + gitSp + `{}]*\$[^` + gitSp + `{}]*)\}[<>]`)
	reCoproc        = regexp.MustCompile(`coproc[` + gitSp + `]+([^` + gitSp + `{(;|&<>]+)[` + gitSp + `]+`)
	reNamerefFlags  = regexp.MustCompile(`^[^;&|\n]*[` + gitSp + `]-[A-Za-z]*n`)
	reSubscript     = regexp.MustCompile(`\[[\p{Nd}]+\]$`)
	reDynChars      = regexp.MustCompile("[$`!*?{]")
	reLetTarget     = regexp.MustCompile(`[A-Za-z_][` + wordClass + `]*|\$[^` + gitSp + `]*`)
)

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// precededBy reports whether the rune before byte offset i of s is a word rune or one of extra (a lookbehind).
func precededBy(s string, i int, extra string) bool {
	if i <= 0 {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(s[:i])
	return isWordRune(last) || strings.ContainsRune(extra, last)
}

func protectedName(s string) bool { return reProtectedName.MatchString(s) }

func (a *Analyzer) assignBlock(verb, what, name, why string) error {
	return blockf(a.Role + " may not " + verb + " HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* (or zsh path) with " + what + " (" + pyRepr(name) +
		": " + why + "); use a plain variable name, or ask your lead")
}

// callAssign is a port of Analyzer.call_assign: whole-call checks for NO_PUSH_ROLES.
func (a *Analyzer) callAssign(text string, bodies []string) error {
	for _, m := range reNamedFd.FindAllStringSubmatchIndex(text, -1) {
		if precededBy(text, m[0], `$\`) {
			continue
		}
		name := text[m[2]:m[3]]
		if protectedName(name) || strings.Contains(name, "$") {
			return blockf(a.Role + " may not assign HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* with a named-fd redirect " +
				"({" + name + "}>: it assigns the variable, or the name is dynamic); use a plain variable name, or ask your lead")
		}
	}
	for _, m := range reCoproc.FindAllStringSubmatchIndex(text, -1) {
		if precededBy(text, m[0], "-") {
			continue
		}
		rest := []rune(text[m[1]:])
		if len(rest) == 0 || pyIsSpace(rest[0]) || strings.ContainsRune(";|&)", rest[0]) {
			continue
		}
		name := strings.Trim(text[m[2]:m[3]], `'"`)
		if protectedName(name) || strings.ContainsAny(name, "$`!*?{[") {
			return blockf(a.Role + " may not assign HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* with coproc NAME (" +
				pyRepr(name) + ": it assigns the variable, or the name is dynamic); use a plain variable name, or ask your lead")
		}
	}
	if declaresNameref(text) && namesProtected(text) {
		return blockf(a.Role + " may not assign HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* ... in a call that declares a " +
			"nameref (-n): the nameref could assign it later; declare the nameref in a call that does not name them, or ask your lead")
	}
	for _, body := range bodies {
		if err := a.builtinAssign(nil, nil, []string{body}); err != nil {
			return err
		}
	}
	return nil
}

// declaresNameref: re.search(r"(?<![\w-])(declare|typeset|local)\b[^;&|\n]*\s-[A-Za-z]*n", text)
func declaresNameref(text string) bool {
	for _, kw := range []string{"declare", "typeset", "local"} {
		for from := 0; ; {
			i := strings.Index(text[from:], kw)
			if i < 0 {
				break
			}
			i += from
			from = i + 1
			if precededBy(text, i, "-") {
				continue
			}
			end := i + len(kw)
			if next := []rune(text[end:]); len(next) > 0 && isWordRune(next[0]) {
				continue // no \b after the keyword
			}
			if reNamerefFlags.MatchString(text[end:]) {
				return true
			}
		}
	}
	return false
}

// namesProtected: re.search(r"(?<![\w])(HOME|XDG_CONFIG_HOME|PATH|path|PAGER|GIT_\w*)(?![\w])", text): a whole word token.
func namesProtected(text string) bool {
	for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !isWordRune(r) }) {
		if w == "HOME" || w == "XDG_CONFIG_HOME" || w == "PATH" || w == "path" || w == "PAGER" || strings.HasPrefix(w, "GIT_") {
			return true
		}
	}
	return false
}

// builtinAssign is a port of Analyzer.builtin_assign.
func (a *Analyzer) builtinAssign(assigns, words, extra []string) error {
	bad := func(name string) bool { return protectedName(reSubscript.ReplaceAllString(name, "")) }
	dyn := func(name string) bool {
		return reDynChars.MatchString(name) || strings.Contains(reSubscript.ReplaceAllString(name, ""), "[")
	}
	denyAs := func(verb, what, name string) error {
		why := "it selects the git config, helpers or pager"
		if dyn(name) {
			why = "dynamic target name"
		}
		return a.assignBlock(verb, what, name, why)
	}
	deny := func(what, name string) error { return denyAs("assign", what, name) }
	all := append(append(append([]string(nil), assigns...), words...), extra...)
	for _, w := range all {
		m := reParamDefault.FindStringSubmatch(w)
		if m == nil {
			continue
		}
		if strings.HasPrefix(m[1], "!") {
			return deny("an indirect ${!VAR:=...} default", m[1])
		}
		if bad(m[1]) || dyn(m[1]) {
			return deny("a ${VAR:=...} default", m[1])
		}
	}
	if len(words) == 0 {
		return nil
	}
	if m := reSubscriptAsg.FindStringSubmatch(words[0]); m != nil && bad(m[1]) { // T-0788: `path[1]=x` is an assignment
		return deny("a subscript assignment", m[1])
	}
	name, rest := pyBasename(words[0]), words[1:]
	var targets []string
	switch {
	case name == "env": // T-0788: removing a protected name (env -u, env -i) strips the pane's git isolation
		return a.envUnset(rest, bad, dyn, denyAs)
	case name == "unset":
		for _, w := range rest {
			if !strings.HasPrefix(w, "-") && (bad(w) || dyn(w)) {
				return denyAs("unset", "unset", w)
			}
		}
	case name == "read" || name == "mapfile" || name == "readarray":
		argopt := "dnOsuCc"
		if name == "read" {
			argopt = "padnNtuiA"
		}
		skip := ""
		for _, w := range rest {
			switch {
			case skip != "":
				if skip == "a" || skip == "A" {
					targets = append(targets, w)
				}
				skip = ""
			case strings.HasPrefix(w, "-") && len(w) > 1:
				for k, c := range w[1:] {
					if c < 0x80 && strings.ContainsRune(argopt, c) { // the rest of the cluster (-aHOME) or the next word is its argument
						if tail := w[1+k+1:]; tail != "" {
							if c == 'a' || c == 'A' {
								targets = append(targets, tail)
							}
						} else {
							skip = string(c)
						}
						break
					}
				}
			default:
				targets = append(targets, w)
			}
		}
	case name == "printf" || name == "print":
		for j, w := range rest {
			if w == "-v" && j+1 < len(rest) {
				targets = append(targets, rest[j+1])
			} else if strings.HasPrefix(w, "-v") && len(w) > 2 {
				targets = append(targets, w[2:])
			} else if name == "print" && reClusterV.MatchString(w) && j+1 < len(rest) { // T-0788: cluster -rv NAME
				targets = append(targets, rest[j+1])
			}
		}
	case (name == "for" || name == "select") && len(rest) > 0:
		targets = append(targets, rest[0])
	case name == "set": // T-0788: `set -A path ...` assigns the array
		for j, w := range rest {
			if (w == "-A" || w == "+A" || w == "-a" || w == "+a") && j+1 < len(rest) {
				targets = append(targets, rest[j+1])
			}
		}
	case name == "zparseopts": // T-0788: -a ARRAY / -A ASSOC name the variable they assign
		for j, w := range rest {
			if (w == "-a" || w == "-A") && j+1 < len(rest) {
				targets = append(targets, rest[j+1])
			} else if (strings.HasPrefix(w, "-a") || strings.HasPrefix(w, "-A")) && len(w) > 2 {
				targets = append(targets, w[2:])
			}
		}
	case name == "wait":
		for j, w := range rest {
			if strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "p") {
				tail := w[strings.Index(w, "p")+1:] // -pNAME, or the next word
				if tail != "" {
					targets = append(targets, tail)
				} else if j+1 < len(rest) {
					targets = append(targets, rest[j+1])
				}
			}
		}
	case name == "getopts":
		n := 0
		for _, w := range rest {
			if !strings.HasPrefix(w, "-") && n < 2 { // optstring too: fail closed
				targets = append(targets, w)
				n++
			}
		}
	case name == "let":
		for _, w := range rest {
			if i := strings.Index(w, "="); i >= 0 {
				w = w[:i]
			}
			targets = append(targets, reLetTarget.FindAllString(w, -1)...)
		}
	case in(Declarers, name):
		isNameref := false
		for _, w := range rest {
			if !strings.HasPrefix(w, "-") {
				t, _, _ := strings.Cut(w, "=")
				targets = append(targets, t)
			} else if !strings.HasPrefix(w, "--") && strings.Contains(w, "n") {
				isNameref = true
			}
		}
		if isNameref {
			for _, w := range rest { // nameref: declare -n r=HOME makes `read r` assign HOME
				if !strings.HasPrefix(w, "-") && strings.Contains(w, "=") {
					_, v, _ := strings.Cut(w, "=")
					if bad(v) || dyn(v) {
						return deny(name+" -n", v)
					}
				}
			}
		}
	}
	for _, t := range targets {
		if bad(t) || dyn(t) {
			return deny(name, t)
		}
	}
	return nil
}

// envUnset ports the `env` branch of Analyzer.builtin_assign (T-0788): env -u/--unset NAME of a protected or dynamic
// name, and env -i / --ignore-environment / - (which clear every name), are refused. -S/--split-string re-parses its
// string as env options; -C/--chdir skips its directory argument.
func (a *Analyzer) envUnset(rest []string, bad, dyn func(string) bool, denyAs func(verb, what, name string) error) error {
	q := append([]string(nil), rest...)
	next := func() (string, bool) {
		if len(q) == 0 {
			return "", false
		}
		w := q[0]
		q = q[1:]
		return w, true
	}
	const clearEnv = "the whole environment"
	for {
		w, ok := next()
		if !ok {
			return nil
		}
		if w == "-C" || w == "--chdir" {
			next()
			continue
		}
		if w == "-S" || w == "--split-string" || strings.HasPrefix(w, "--split-string=") || (strings.HasPrefix(w, "-S") && !strings.HasPrefix(w, "--")) {
			var sval string
			switch {
			case strings.HasPrefix(w, "--") && strings.Contains(w, "="):
				sval = w[strings.Index(w, "=")+1:]
			case strings.HasPrefix(w, "-S") && w != "-S":
				sval = w[2:]
			default:
				sval, _ = next()
			}
			q = append(pyFields(sval), q...)
			continue
		}
		if w == "-i" || w == "--ignore-environment" || w == "-" {
			return denyAs("clear or unset", "env "+w, clearEnv)
		}
		var names []string
		switch {
		case w == "-u" || w == "--unset":
			n, _ := next()
			names = []string{n}
		case strings.HasPrefix(w, "--unset="):
			names = []string{w[len("--unset="):]}
		case strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && len(w) > 1:
			for k, c := range w[1:] {
				if c == 'i' {
					return denyAs("clear or unset", "env "+w, clearEnv)
				}
				if c == 'u' { // -uNAME, or the next word
					n := w[1+k+utf8.RuneLen(c):]
					if n == "" {
						n, _ = next()
					}
					names = []string{n}
					break
				}
			}
		case strings.HasPrefix(w, "--") || IsAssign(w):
			continue
		default:
			return nil // the command word
		}
		for _, n := range names {
			if bad(n) || dyn(n) {
				return denyAs("unset", "env -u", n)
			}
		}
	}
}
