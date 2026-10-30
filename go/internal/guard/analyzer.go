package guard

import (
	"os"
	"sort"
	"strings"
)

// persistWhy is worker-guard.py:PERSIST_WHY.
const persistWhy = "it would persist config into the live box (git config keys like core.fsmonitor or diff.external, and " +
	"shell init files, run later as the tick and other agents); only the owner changes ~/.gitconfig, " +
	"~/.config/git and shell rc files"

// shellLike is worker-guard.py:SHELL_LIKE.
var shellLike = func() map[string]struct{} {
	m := map[string]struct{}{"source": {}, ".": {}, "eval": {}}
	for k := range Shells {
		m[k] = struct{}{}
	}
	return m
}()

// Analyzer is a port of worker-guard.py:Analyzer. The public-text rule (PublicText, pt) is the Python hook's: see
// ptCommand (publictext.go).
type Analyzer struct {
	Cwd  string
	Role string
	HQ   bool

	gcwd string // directory a git command would run in, moved by `cd` (git rule only)
	gdyn bool   // true after a `cd` into a directory that is only known at run time
	pt   bool   // Analyzer.pt is set: the public-text rule looks at git/gh writes (delegated, see ptCommand)

	vars      map[string]string // NAME=value assigned earlier in the command: lets the persist detector resolve $NAME
	ccwd      string            // like gcwd, for the CFG_DIR write detector
	fsChanged bool              // T-0783: an earlier command on this line may have changed the filesystem
	ccwdKnown bool              // false where Python holds None: a `cd` target is only known at run time

	skipPriv         bool
	unitRule         bool // HQ mode: block mutating systemctl (workers keep the factory unit rule)
	text             string
	ranFactory       bool
	scrubWithFactory string
}

// NewAnalyzer is a port of Analyzer.__init__.
// Python's `cwd or os.getcwd()` raises when the cwd is gone; that is exit 2, so an error here is a block.
func NewAnalyzer(cwd, role string, hq bool) (*Analyzer, error) {
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, blockf("cannot determine the working directory: " + err.Error())
		}
		cwd = wd
	}
	return &Analyzer{Cwd: cwd, Role: role, HQ: hq, gcwd: cwd, vars: map[string]string{}, ccwd: cwd, ccwdKnown: true}, nil
}

// soft is a port of Analyzer.soft applied to the result of the call: in HQ mode only a PRIV or public-text block (and a
// delegation, which must never turn into an allow) counts, so parse trouble never blocks.
func (a *Analyzer) soft(err error) error {
	if err == nil || IsPrivBlock(err) || IsDelegate(err) || isPublicBlock(err) {
		return err
	}
	if a.HQ {
		return nil
	}
	return err
}

// ───────────────────────────── PRIV helpers ─────────────────────────────

// PrivWord is a port of worker-guard.py:priv_word. ok is false where Python returns None.
func PrivWord(w string, globs bool) (string, bool) {
	base := pyBasename(strings.TrimRight(w, "/"))
	if in(PrivWords, base) {
		return base, true
	}
	if globs && base != "[" && base != "[[" && strings.ContainsAny(base, "*?[") {
		var words []string
		for p := range PrivWords {
			words = append(words, p)
		}
		sort.Strings(words)
		for _, p := range words {
			if fnmatchCase(p, base) {
				return p, true
			}
		}
	}
	return "", false
}

// PrivSubst is a port of worker-guard.py:priv_subst.
func PrivSubst(w string) (string, bool) {
	if !strings.HasPrefix(w, "$(") && !strings.HasPrefix(w, "`") {
		return "", false
	}
	return FindPrivSubst(w)
}

// PrivInCode is a port of worker-guard.py:priv_in_code: re.search(r"sudo|doas|pkexec|docker|su", m.group(0)).
func PrivInCode(text string) (string, bool) {
	m, ok := FindPrivCode(text)
	if !ok {
		return "", false
	}
	for i := 0; i < len(m); i++ {
		for _, w := range []string{"sudo", "doas", "pkexec", "docker", "su"} {
			if strings.HasPrefix(m[i:], w) {
				return w, true
			}
		}
	}
	return "", false
}

// sedAlt2At matches /[gIpmM]*e[gIpmM]*(?:\s|;|$) at byte i (the second alternative of SED_E and of the sed
// herdr check).
func sedAlt2At(s string, i int) bool {
	if i >= len(s) || s[i] != '/' {
		return false
	}
	flag := func(c byte) bool { return strings.IndexByte("gIpmM", c) >= 0 }
	j := i + 1
	for j < len(s) && flag(s[j]) {
		j++
	}
	if j >= len(s) || s[j] != 'e' {
		return false
	}
	j++
	for j < len(s) && flag(s[j]) {
		j++
	}
	return j >= len(s) || s[j] == ';' || isSpaceAt(s, j)
}

// sedE is a port of SED_E.search(a) (worker-guard.py:458):
//
//	(?:^|[;{}]|\d|\$|,)\s*e\s+(.*)|/[gIpmM]*e[gIpmM]*(?:\s|;|$)     (re.S)
//
// It returns whether it matched and group 1 (empty when the second alternative matched).
func sedE(s string) (matched bool, group1 string) {
	for i := 0; i <= len(s); i++ {
		starts := []int{}
		if i == 0 {
			starts = append(starts, 0)
		}
		if i < len(s) {
			r := nextRune(s, i)
			if strings.IndexByte(";{}$,", s[i]) >= 0 || isDecimal(r) {
				starts = append(starts, i+runeLen(s, i))
			}
		}
		for _, p := range starts {
			j := skipSpace(s, p)
			if j < len(s) && s[j] == 'e' {
				k := skipSpace(s, j+1)
				if k > j+1 {
					return true, s[k:]
				}
			}
		}
		if sedAlt2At(s, i) {
			return true, ""
		}
	}
	return false, ""
}

// awkRun is a port of AWK_RUN.search(a): system\s*\(|\|\s*&?\s*["']|["']\s*\|
func awkRun(s string) bool {
	quote := func(i int) bool { return i < len(s) && (s[i] == '"' || s[i] == '\'') }
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "system"):
			if j := skipSpace(s, i+6); j < len(s) && s[j] == '(' {
				return true
			}
		case s[i] == '|':
			j := skipSpace(s, i+1)
			if j < len(s) && s[j] == '&' {
				j = skipSpace(s, j+1)
			}
			if quote(j) {
				return true
			}
		case quote(i):
			if j := skipSpace(s, i+1); j < len(s) && s[j] == '|' {
				return true
			}
		}
	}
	return false
}

func hasSpace(s string) bool {
	for _, r := range s {
		if pyIsSpace(r) {
			return true
		}
	}
	return false
}

// ───────────────────────────── script ─────────────────────────────

type pcmd struct {
	words []string
	piped bool
	hs    []string
}

// redirSubstMatches is the finditer of r">[>|]?\s*[^\s;|&]*(?:\$\(|`)[^;|&\n]*" at worker-guard.py:973.
func redirSubstMatches(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		end := -1
		if s[i] == '>' {
			opts := []int{0}
			if i+1 < len(s) && (s[i+1] == '>' || s[i+1] == '|') {
				opts = []int{1, 0}
			}
			for _, o := range opts {
				j := skipSpace(s, i+1+o)
				k := j
				for k < len(s) && s[k] != ';' && s[k] != '|' && s[k] != '&' && !isSpaceAt(s, k) {
					k += runeLen(s, k)
				}
				q, mlen := -1, 0
				for p := j; p < k; p++ {
					if strings.HasPrefix(s[p:], "$(") {
						q, mlen = p, 2
						break
					}
					if s[p] == '`' {
						q, mlen = p, 1
						break
					}
				}
				if q < 0 {
					continue
				}
				end = len(s)
				if x := strings.IndexAny(s[q+mlen:], ";|&\n"); x >= 0 {
					end = q + mlen + x
				}
				break
			}
		}
		if end < 0 {
			i++
			continue
		}
		out = append(out, s[i:end])
		i = end
	}
	return out
}

// Script is a port of Analyzer.script (depth 0 entry).
func (a *Analyzer) Script(text string) error { return a.script(text, 0) }

func (a *Analyzer) script(text string, depth int) error {
	if depth > MaxDepth {
		return blockf("nesting too deep")
	}
	if depth == 0 {
		a.text = text
		if !a.HQ { // a redirect whose target is built by a substitution and ends in a protected name
			for _, m := range redirSubstMatches(text) {
				if PersistCode(m) {
					return blockf("a redirect built by a substitution into a protected config/init file: " + persistWhy)
				}
			}
		}
	}
	var nbodies []Heredoc // T-0771: the heredoc bodies of this call only
	text, extras, err := Preprocess(text, a.HQ, &nbodies)
	if err != nil {
		return err
	}
	if a.noPush() {
		bodies := make([]string, len(nbodies))
		for i, b := range nbodies {
			bodies[i] = b.Body
		}
		if err := a.callAssign(text, bodies); err != nil {
			return err
		}
	}
	for _, x := range extras {
		switch {
		case x.Kind == "sh":
			if err := a.soft(a.script(x.Body, depth+1)); err != nil {
				return err
			}
		case x.Kind == "icode":
			if hit, ok := PrivInCode(x.Body); ok {
				return &PrivBlock{Word: hit}
			}
			if !a.HQ && PersistCode(x.Body) {
				return blockf("interpreter code naming a protected config/init file: " + persistWhy)
			}
		case !a.HQ && strings.Contains(x.Body, "herdr"):
			return blockf("herdr in an interpreter heredoc")
		}
	}
	toks, err := ScriptTokens(text)
	if err != nil {
		return err
	}
	subs, err := ExtractSubs(text)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if err := a.soft(a.script(sub, depth+1)); err != nil {
			return err
		}
	}
	var cmds []pcmd
	var cur, hs []string
	piped, dollar := false, false
	type red struct {
		idx    int
		target string
	}
	var reds []red
	var stack []pcmd
	assignOrReserved := func(w string) bool { return IsAssign(w) || in(Reserved, w) }
	for i := 0; i < len(toks); {
		tok := toks[i]
		nxt, hasNxt := "", false
		if i+1 < len(toks) {
			nxt, hasNxt = toks[i+1], true
		}
		if tok == "$" && hasNxt && nxt == "(" && allOf(cur, assignOrReserved) {
			depthP, hit, hitSet := 0, "", false
			for _, t := range toks[i+1:] {
				if IsPunct(t) {
					depthP += strings.Count(t, "(") - strings.Count(t, ")")
					if depthP <= 0 {
						break
					}
				} else if !hitSet {
					hit, hitSet = PrivWord(t, !a.HQ)
				}
			}
			if hitSet {
				return &PrivBlock{Word: hit}
			}
			if !a.HQ {
				return &DynamicBlock{Msg: "command substitution as the command word could expand to herdr or claude"}
			}
			dollar = true
			i++
			continue
		}
		if strings.HasSuffix(tok, "$") && hasNxt && nxt == "(" {
			tok = tok[:len(tok)-1]
			dollar = true
			if tok == "" {
				i++
				continue
			}
		}
		if !IsPunct(tok) {
			cur = append(cur, tok)
			i++
			continue
		}
		if (strings.Contains(tok, "<") || strings.Contains(tok, ">")) && !strings.Contains(tok, "(") {
			if hasNxt && !IsPunct(nxt) {
				if tok == "<<<" {
					hs = append(hs[:len(hs):len(hs)], nxt)
				} else if strings.Contains(tok, ">") && (!strings.Contains(tok, "&") ||
					((tok == "&>" || tok == "&>>" || tok == ">&") && !isFdOrDash(nxt))) {
					reds = append(reds, red{len(cmds), nxt}) // `>&2` / `>&-` duplicate or close an fd: no file target
				}
				i++
			}
		} else {
			if !a.HQ && (strings.Contains(tok, "<") || strings.Contains(tok, ">")) && anyOf(cur, func(w string) bool { return in(shellLike, pyBasename(w)) }) {
				return blockf("shell reading a process substitution: content cannot be inspected")
			}
			for _, piece := range splitParens(tok) {
				switch {
				case piece == "(" && dollar:
					stack = append(stack, pcmd{cur, piped, hs})
					cur, piped, hs = nil, false, nil
				case piece == ")" && len(stack) > 0:
					cmds = append(cmds, pcmd{cur, piped, hs})
					top := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					cur, piped, hs = top.words, top.piped, top.hs
					if anyOf(cur, func(w string) bool { return !assignOrReserved(w) }) {
						cur = append(cur, "$SUB") // the substitution's value is an argument of that command
					}
				default:
					cmds = append(cmds, pcmd{cur, piped, hs})
					cur, piped, hs = nil, strings.Contains(piece, "|"), nil
				}
				dollar = false
			}
		}
		i++
	}
	for len(stack) > 0 { // unclosed $( ): analyse what we have
		cmds = append(cmds, pcmd{cur, piped, hs})
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		cur, piped, hs = top.words, top.piped, top.hs
	}
	cmds = append(cmds, pcmd{cur, piped, hs})
	for n, c := range cmds {
		if !a.HQ {
			for _, r := range reds {
				if r.idx == n {
					if err := a.cfgRedirect(r.target); err != nil {
						return err
					}
					if err := a.persistRedirect(r.target); err != nil {
						return err
					}
				}
			}
		}
		if err := a.soft(a.cmd(c.words, depth, c.piped, false, c.hs, nil)); err != nil {
			return err
		}
		for _, r := range reds {
			if r.idx == n && r.target != "/dev/null" {
				a.fsChanged = true
			}
		}
	}
	return nil
}

func allOf(ws []string, f func(string) bool) bool {
	for _, w := range ws {
		if !f(w) {
			return false
		}
	}
	return true
}

func anyOf(ws []string, f func(string) bool) bool {
	for _, w := range ws {
		if f(w) {
			return true
		}
	}
	return false
}

// isFdOrDash is re.match(r"^([0-9]+|-)$", s): ASCII digits or "-", `$` also before a final newline.
func isFdOrDash(s string) bool {
	s = strings.TrimSuffix(s, "\n")
	if s == "-" {
		return true
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// splitParens is re.findall(r"\(|\)|[^()]+", tok).
func splitParens(tok string) []string {
	var out []string
	start := -1
	for i := 0; i < len(tok); i++ {
		if tok[i] == '(' || tok[i] == ')' {
			if start >= 0 {
				out = append(out, tok[start:i])
				start = -1
			}
			out = append(out, tok[i:i+1])
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, tok[start:])
	}
	return out
}

// ───────────────────────────── priv ─────────────────────────────

func (a *Analyzer) priv(words []string, depth int, piped bool, hstrs []string) error {
	if a.skipPriv {
		return nil
	}
	if depth > MaxDepth {
		return blockf("nesting too deep")
	}
	words = StripPrefix(words)
	if len(words) == 0 {
		return nil
	}
	w, rest := words[0], words[1:]
	if hit, ok := PrivWord(w, !a.HQ); ok {
		return &PrivBlock{Word: hit}
	}
	if hit, ok := PrivSubst(w); ok {
		return &PrivBlock{Word: hit}
	}
	name := pyBasename(w)
	if name == "systemd-run" {
		return &UnitBlock{Msg: "systemd-run"}
	}
	if name == "systemctl" && a.unitRule {
		for _, x := range rest {
			if !strings.HasPrefix(x, "-") && in(UnitMutating, x) {
				return &UnitBlock{Msg: "systemctl " + x}
			}
		}
	}
	if name == "find" {
		for j, x := range rest {
			if in(FindExec, x) {
				end := len(rest)
				for k := j + 1; k < len(rest); k++ {
					if rest[k] == ";" || rest[k] == "+" {
						end = k
						break
					}
				}
				if err := a.priv(rest[j+1:end], depth+1, false, nil); err != nil {
					return err
				}
			}
		}
		return nil
	}
	switch {
	case name == "trap" && len(rest) > 0:
		payload := ""
		for _, x := range rest {
			if !strings.HasPrefix(x, "-") {
				payload = x
				break
			}
		}
		if err := a.soft(a.script(payload, depth+1)); err != nil {
			return err
		}
	case name == "git":
		if err := a.gitPriv(rest, depth); err != nil {
			return err
		}
	case name == "gh":
		if hit := ghRepoChange(rest); hit != "" {
			return &RepoProtBlock{Msg: hit}
		}
	case name == "sed":
		for _, x := range rest {
			if m, g1 := sedE(x); m {
				code := g1
				if code == "" {
					code = strings.ReplaceAll(x, "/", " ")
				}
				if hit, ok := PrivInCode(code); ok {
					return &PrivBlock{Word: hit}
				}
			}
		}
	case in(Awks, name):
		for _, x := range rest {
			if awkRun(x) {
				if hit, ok := PrivInCode(x); ok {
					return &PrivBlock{Word: hit}
				}
			}
		}
	}
	if vopts, ok := WrapValueOpts[name]; ok {
		if name == "command" && len(rest) > 0 && (rest[0] == "-v" || rest[0] == "-V" || rest[0] == "-pv" || rest[0] == "-pV") {
			return nil
		}
		i := 0
	opts:
		for i < len(rest) {
			x := rest[i]
			switch {
			case x == "--":
				i++
				break opts
			case name == "env" && IsAssign(x):
				i++
			case name == "env" && (x == "-S" || x == "--split-string" || strings.HasPrefix(x, "--split-string=")):
				payload := ""
				if k := strings.IndexByte(x, '='); k >= 0 {
					payload = x[k+1:]
					i++
				} else {
					if i+1 < len(rest) {
						payload = rest[i+1]
					}
					i += 2
				}
				if err := a.soft(a.script(payload, depth+1)); err != nil {
					return err
				}
			case strings.HasPrefix(x, "-") && len(x) > 1:
				if in(vopts, x) {
					i += 2
				} else {
					i++
				}
			default:
				break opts
			}
		}
		if name == "timeout" {
			i++ // the duration
		}
		var sub []string
		if i < len(rest) {
			sub = rest[i:]
		}
		if name == "watch" && len(sub) > 0 && hasSpace(sub[0]) {
			if err := a.soft(a.script(strings.Join(sub, " "), depth+1)); err != nil {
				return err
			}
		}
		return a.priv(sub, depth+1, false, nil)
	}
	if in(Wrappers, name) && !in(WrapData, name) { // other exec wrappers: any later word is suspect
		for _, x := range rest {
			if hasSpace(x) {
				if err := a.soft(a.script(x, depth+1)); err != nil {
					return err
				}
			} else if hit, ok := PrivWord(x, !a.HQ); ok {
				return &PrivBlock{Word: hit}
			}
		}
		return nil
	}
	if !a.HQ {
		return nil
	}
	switch {
	case in(Shells, name):
		return a.soft(a.shell(rest, depth, false, hstrs))
	case name == "eval":
		if !strings.Contains(strings.Join(rest, " "), "$") {
			return a.soft(a.script(strings.Join(rest, " "), depth+1))
		}
	case (name == "source" || name == ".") && len(rest) > 0:
		return a.soft(a.file(rest[0], depth))
	case interpreterFull(name):
		return a.interpPriv(name, rest)
	}
	return nil
}

// gitPriv is a port of Analyzer.git_priv: git runs shell text from `-c alias.x=!cmd`, `-c core.pager=cmd` and
// `git config <key> <value>`.
func (a *Analyzer) gitPriv(rest []string, depth int) error {
	var pairs []string
	for j := 0; j+1 < len(rest); j++ {
		if rest[j] == "-c" {
			pairs = append(pairs, rest[j+1])
		}
	}
	for k, x := range rest {
		if x != "config" {
			continue
		}
		var args []string
		for _, y := range rest[k+1:] {
			if !strings.HasPrefix(y, "-") {
				args = append(args, y)
			}
		}
		for j := 0; j+1 < len(args); j++ {
			pairs = append(pairs, args[j]+"="+args[j+1])
		}
		for _, y := range args {
			if strings.Contains(y, "=") {
				pairs = append(pairs, y)
			}
		}
		break
	}
	for _, kv := range pairs {
		key, val := kv, ""
		if k := strings.IndexByte(kv, '='); k >= 0 {
			key, val = kv[:k], kv[k+1:]
		}
		if strings.HasPrefix(val, "!") {
			if err := a.soft(a.script(val[1:], depth+1)); err != nil {
				return err
			}
		} else if in(GitCmdKeys, pyLower(key)) {
			if err := a.soft(a.script(val, depth+1)); err != nil {
				return err
			}
		}
	}
	return nil
}

// pyLower is str.lower for the keys GitCmdKeys holds (ASCII after lowering; others cannot match anyway).
func pyLower(s string) string { return strings.ToLower(s) }

// interpPriv is a port of Analyzer.interp_priv.
func (a *Analyzer) interpPriv(name string, rest []string) error {
	_, ci, unknown := InterpArgs(name, rest)
	var texts []string
	if unknown { // cannot tell which word is the code: look at every code-looking word
		for _, x := range rest {
			if hasSpace(x) || strings.ContainsAny(x, "\"'();") {
				texts = append(texts, x)
			}
		}
	} else if ci >= 0 {
		texts = []string{rest[ci]}
	}
	for _, t := range texts {
		if hit, ok := PrivInCode(t); ok {
			return &PrivBlock{Word: hit}
		}
	}
	return nil
}
