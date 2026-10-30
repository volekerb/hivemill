package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"factory/go/internal/shlex"
)

// PublicBlock is worker-guard.py:PublicBlock, the owner-terms / public-text refusal.
type PublicBlock struct{ Msg string }

func (b *PublicBlock) Error() string { return b.Msg }

// Port of worker-guard.py:terms_bash and its helpers (T-0600): a Bash command that would read the owner-terms
// file. The only source of the terms path here is the exported SCRUB_TERMS_FILE; Python also reads the
// `scrub_terms_file` key of the factory DB, which needs sqlite and stays with T6d (see docs/worker-guard-go.md).

const (
	termsVar = "SCRUB_TERMS_FILE"
	termsWhy = "the owner-terms file may only be read by ci/scrub_scan.py and ci/public_text_guard.py (run them with " +
		"SCRUB_TERMS_FILE as spawn exported it); ask your lead if you need a scan"
)

const (
	rxWord  = `\p{L}\p{N}_`
	rxSpace = `\t-\r\x1c-\x20\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
)

var (
	reTermsScanner = regexp.MustCompile(`(?:^|/)ci/(?:scrub_scan|public_text_guard)\.py\n?\z`)
	reTermsBrace   = regexp.MustCompile(`\{[^{}` + rxSpace + `]*,[^{}` + rxSpace + `]*\}|\{[` + rxWord + `]+\.\.[` + rxWord + `]+\}`)
	reTermsFds     = regexp.MustCompile(`(?:\p{Nd}*>&\p{Nd}*-?|&>)`)
	reTermsSubst   = regexp.MustCompile("\\$\\(|[`()]")
	reTermsSubst2  = regexp.MustCompile("\\$\\(|[`()'\"\\\\]")
	reRecFlag      = regexp.MustCompile(`^(?:-[A-Za-z]*[rRa][A-Za-z]*|--recursive|--dereference-recursive|--archive)\n?$`)
	reTermsSegBad  = regexp.MustCompile("`|\\$\\(|<")

	termsRecursive = set("rg", "find", "tar", "bsdtar", "zip", "rsync", "ag", "ack")
	termsGreps     = set("grep", "egrep", "fgrep", "cp", "scp")
	termsWrappers  = set("env", "command", "nice", "time", "nohup", "xargs")
	termsShells    = set("bash", "sh", "zsh", "dash", "ksh")
)

// isTermsEnvAssign is TERMS_ENV_ASSIGN.match: ^[A-Za-z_]\w*=
func isTermsEnvAssign(s string) bool {
	if s == "" || !(s[0] == '_' || s[0] >= 'A' && s[0] <= 'Z' || s[0] >= 'a' && s[0] <= 'z') {
		return false
	}
	for _, r := range s[1:] {
		if r == '=' {
			return true
		}
		if !isWord(r) {
			return false
		}
	}
	return false
}

// pyRealpath is os.path.realpath(p) (non-strict): symlinks of the existing part resolved, the rest kept.
func pyRealpath(p string) string {
	if !strings.HasPrefix(p, "/") {
		wd, err := os.Getwd()
		if err != nil {
			wd = "/"
		}
		p = pyJoin(wd, p)
	}
	resolved := "/"
	pending := strings.Split(p, "/")
	links := 0
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := pyJoin(resolved, c)
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		if links++; links > 40 { // a loop: Python keeps the path as it stands
			return pyJoin(next, strings.Join(pending, "/"))
		}
		target, err := os.Readlink(next)
		if err != nil {
			resolved = next
			continue
		}
		if strings.HasPrefix(target, "/") {
			resolved = "/"
		}
		pending = append(strings.Split(target, "/"), pending...)
	}
	return resolved
}

// termsPaths is a port of terms_paths(cwd) for the environment source.
func termsPaths() map[string]struct{} {
	out := map[string]struct{}{}
	v := strings.TrimFunc(os.Getenv(termsVar), pyIsSpace)
	if v != "" {
		v = expandUser(v)
		out[v] = struct{}{}
		out[pyNormpath(v)] = struct{}{}
		out[pyRealpath(v)] = struct{}{}
	}
	return out
}

type inode struct{ dev, ino uint64 }

// termsIDs is a port of terms_ids; ok is false where Python returns None (a spelling cannot be stat'ed).
func termsIDs(paths map[string]struct{}) (map[inode]struct{}, bool) {
	ids := map[inode]struct{}{}
	for p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, false
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, false
		}
		ids[inode{uint64(sys.Dev), uint64(sys.Ino)}] = struct{}{}
	}
	return ids, true
}

// isPythonCmd is re.fullmatch(r"(?:.*/)?python3?", s) (`.` does not match a newline).
func isPythonCmd(s string) bool {
	for _, base := range []string{"python", "python3"} {
		if s == base {
			return true
		}
		if pre, ok := strings.CutSuffix(s, "/"+base); ok && !strings.Contains(pre, "\n") {
			return true
		}
	}
	return false
}

// termsSegmentOK is a port of terms_segment_ok.
func termsSegmentOK(seg string) bool {
	if reTermsSegBad.MatchString(seg) {
		return false
	}
	toks, err := shlex.Split(seg)
	if err != nil {
		return false
	}
	for len(toks) > 0 && (isTermsEnvAssign(toks[0]) || toks[0] == "env" || (strings.HasPrefix(toks[0], "-") && len(toks) > 1)) {
		toks = toks[1:]
	}
	if len(toks) > 0 && isPythonCmd(toks[0]) {
		toks = toks[1:]
	}
	return len(toks) > 0 && reTermsScanner.MatchString(toks[0])
}

func hasMagic(s string) bool { return strings.ContainsAny(s, "*?[") }

// pyGlob is glob.glob(pattern): a pattern without magic is returned when the path exists; Go's matcher is
// used otherwise (a superset of Python's, which skips dot files for a leading `*`: a wider match only blocks more).
func pyGlob(pattern string) []string {
	if !hasMagic(pattern) {
		if _, err := os.Lstat(pattern); err == nil {
			return []string{pattern}
		}
		return nil
	}
	m, err := filepath.Glob(strings.ReplaceAll(pattern, "[!", "[^"))
	if err != nil {
		return nil
	}
	return m
}

func baseNames(paths map[string]struct{}) map[string]struct{} {
	names := map[string]struct{}{}
	for p := range paths {
		names[pyBasename(p)] = struct{}{}
	}
	return names
}

// termsTokenHits is a port of terms_token_hits.
func termsTokenHits(tok, cwd string, paths map[string]struct{}, ids map[inode]struct{}, idsOK bool) bool {
	names := baseNames(paths)
	cands := []string{tok}
	if k := strings.IndexByte(tok, '='); k >= 0 {
		cands = append(cands, tok[k+1:])
	}
	for _, c := range cands {
		c = strings.TrimLeft(c, "<>(")
		if c == "" {
			continue
		}
		full := pyJoin(cwd, expandUser(c))
		found := pyGlob(full)
		if len(found) == 0 {
			found = []string{full}
		}
		for _, f := range found {
			real := pyRealpath(f)
			if in(paths, real) || in(names, pyBasename(real)) {
				return true
			}
			st, err := os.Stat(real)
			if err != nil {
				continue
			}
			if sys, ok := st.Sys().(*syscall.Stat_t); ok && idsOK {
				if _, hit := ids[inode{uint64(sys.Dev), uint64(sys.Ino)}]; hit {
					return true
				}
			}
		}
		if !idsOK && hasMagic(c) {
			for p := range paths {
				if fnmatchCase(pyBasename(p), pyBasename(c)) {
					return true
				}
			}
		}
	}
	return false
}

// termsRecDirflag is a port of terms_rec_dirflag.
func termsRecDirflag(toks []string) bool {
	for i, t := range toks {
		if t == "--directories=recurse" || t == "-drecurse" || (t == "recurse" && i > 0 && (toks[i-1] == "-d" || toks[i-1] == "--directories")) {
			return true
		}
	}
	return false
}

// termsRecursiveHits is a port of terms_recursive_hits.
func termsRecursiveHits(toks []string, cwd string, paths map[string]struct{}) bool {
	toks = append([]string(nil), toks...)
	xargs := false
	for len(toks) > 0 && (isTermsEnvAssign(toks[0]) || in(termsWrappers, pyBasename(toks[0]))) {
		xargs = xargs || pyBasename(toks[0]) == "xargs"
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return false
	}
	name := pyBasename(toks[0])
	if !(in(termsRecursive, name) || (in(termsGreps, name) &&
		(anyOf(toks[1:], reRecFlag.MatchString) || termsRecDirflag(toks[1:])))) {
		return false
	}
	if xargs { // the paths arrive on stdin, so the target cannot be judged: fail closed
		return true
	}
	var reals []string
	for p := range paths {
		reals = append(reals, pyRealpath(p))
	}
	var targets []string
	anyPath := false
	for _, t := range toks[1:] {
		if strings.HasPrefix(t, "-") && name != "tar" {
			continue
		}
		cs := []string{t}
		if k := strings.IndexByte(t, '='); k >= 0 {
			cs = append(cs, t[k+1:])
		}
		seen := map[string]bool{}
		for _, c := range cs {
			if seen[c] {
				continue
			}
			seen[c] = true
			full := pyJoin(cwd, expandUser(c))
			found := pyGlob(full)
			if len(found) == 0 {
				found = []string{full}
			}
			for _, f := range found {
				if _, err := os.Lstat(f); err == nil {
					anyPath = true
					targets = append(targets, pyRealpath(f))
				}
			}
		}
	}
	if !anyPath {
		targets = append(targets, pyRealpath(cwd))
	}
	for _, t := range targets {
		for _, r := range reals {
			if strings.HasPrefix(r, strings.TrimRight(t, "/")+"/") {
				return true
			}
		}
	}
	return false
}

// termsExpand is a port of terms_expand: $VAR / ${VAR} from env, $HOME from the hook's env; unknown names stay.
func termsExpand(text string, env map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '$' {
			b.WriteByte(text[i])
			i++
			continue
		}
		name, end := "", -1
		if i+1 < len(text) && text[i+1] == '{' {
			j := i + 2
			for j < len(text) && wordAt(text, j) {
				j += runeLen(text, j)
			}
			if j > i+2 && j < len(text) && text[j] == '}' {
				name, end = text[i+2:j], j+1
			}
		}
		if end < 0 {
			j := i + 1
			for j < len(text) && wordAt(text, j) {
				j += runeLen(text, j)
			}
			if j > i+1 {
				name, end = text[i+1:j], j
			}
		}
		if end < 0 {
			b.WriteByte('$')
			i++
			continue
		}
		if v, ok := env[name]; ok {
			b.WriteString(v)
		} else if v, ok := os.LookupEnv(name); ok && name == "HOME" {
			b.WriteString(v)
		} else {
			b.WriteString(text[i:end])
		}
		i = end
	}
	return b.String()
}

// termsSplit is a port of terms_split: split on ; & | and newlines outside quotes.
func termsSplit(cmd string) []string {
	var segs []string
	var cur []byte
	var quote byte
	for i := 0; i < len(cmd); {
		c := cmd[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(cmd) {
				cur = append(cur, c)
				i++
				c = cmd[i]
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\' && i+1 < len(cmd):
			cur = append(cur, c)
			i++
			c = cmd[i]
		case c == ';' || c == '&' || c == '|' || c == '\n':
			segs = append(segs, string(cur))
			cur = nil
			i++
			continue
		}
		cur = append(cur, c)
		i++
	}
	return append(segs, string(cur))
}

// TermsBash is a port of terms_bash(cmd, cwd): nil when the command does not read the owner-terms file.
func TermsBash(cmd, cwd string) error { return termsBash(cmd, cwd, map[string]string{}, 0) }

func termsBash(cmd, cwd string, env map[string]string, depth int) error {
	paths := termsPaths()
	if len(paths) == 0 {
		return nil
	}
	ids, idsOK := termsIDs(paths)
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	home := os.Getenv("HOME")
	if home == "" {
		home = expandUser("~")
	}
	names := map[string]struct{}{"scrub": {}, "terms": {}}
	for p := range paths {
		for _, n := range []string{pyBasename(p), pyBasename(filepath.Dir(p))} {
			if n != "" {
				names[strings.ToLower(n)] = struct{}{}
			}
		}
	}
	if strings.Contains(cmd, "${!") || reTermsBrace.MatchString(cmd) {
		low := strings.ToLower(cmd)
		for n := range names {
			if strings.Contains(low, n) {
				return &PublicBlock{Msg: "variable indirection or brace expansion next to the owner-terms file is refused: " + termsWhy}
			}
		}
	}
	for _, rawSeg := range termsSplit(reTermsFds.ReplaceAllString(cmd, " ")) {
		mentions := strings.Contains(rawSeg, termsVar)
		for p := range paths {
			mentions = mentions || strings.Contains(rawSeg, p)
		}
		if mentions && !termsSegmentOK(rawSeg) {
			return &PublicBlock{Msg: "reading the owner-terms file is refused: " + termsWhy}
		}
		seg := termsExpand(rawSeg, env)
		toks, err := shlex.Split(reTermsSubst.ReplaceAllString(seg, " "))
		if err != nil { // unbalanced quote from a split inside quotes: judge the raw words instead
			toks = pyFields(reTermsSubst2.ReplaceAllString(seg, " "))
		}
		for len(toks) > 0 && toks[0] == "export" && len(toks) > 1 {
			toks = toks[1:]
		}
		for _, t := range toks { // VAR=value (also as a command prefix) feeds later expansion
			if !isTermsEnvAssign(t) {
				break
			}
			k := strings.IndexByte(t, '=')
			env[t[:k]] = expandUser(t[k+1:])
		}
		if len(toks) > 0 && (toks[0] == "cd" || toks[0] == "pushd") {
			if len(toks) > 1 {
				cwd = pyRealpath(pyJoin(cwd, expandUser(toks[1])))
			} else {
				cwd = pyRealpath(pyJoin(cwd, home))
			}
			continue
		}
		if termsSegmentOK(seg) {
			continue
		}
		core := append([]string(nil), toks...)
		for len(core) > 0 && (isTermsEnvAssign(core[0]) || (in(termsWrappers, pyBasename(core[0])) && pyBasename(core[0]) != "xargs")) {
			core = core[1:]
		}
		if len(core) > 0 && core[0] == "eval" && depth < 4 {
			if err := termsBash(strings.Join(core[1:], " "), cwd, copyEnv(env), depth+1); err != nil {
				return err
			}
		}
		if len(core) > 0 && in(termsShells, pyBasename(core[0])) && depth < 4 { // bash -c 'payload' is its own script
			for i := 1; i < len(core); i++ {
				if isShellDashC(core[i]) && i+1 < len(core) {
					if err := termsBash(core[i+1], cwd, copyEnv(env), depth+1); err != nil {
						return err
					}
					break
				}
			}
		}
		if termsRecursiveHits(toks, cwd, paths) {
			return &PublicBlock{Msg: "recursive or archive reads of the directory holding the owner-terms file are refused: " + termsWhy}
		}
		for _, t := range toks {
			if termsTokenHits(t, cwd, paths, ids, idsOK) {
				return &PublicBlock{Msg: "reading the owner-terms file is refused: " + termsWhy}
			}
		}
	}
	return nil
}

func copyEnv(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// isShellDashC is re.fullmatch(r"-[A-Za-z]*c[A-Za-z]*", t).
func isShellDashC(t string) bool { return isDashC(t) }
