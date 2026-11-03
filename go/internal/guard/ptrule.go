package guard

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/shlex"
)

// Port of the PublicText class of worker-guard.py (G-021): the public-text and frozen-repo rule for git and gh
// writes (T-0887). The config comes from the guard export (guardexport.go), the checker from scrub.go / ptcheck.go:
// nothing is delegated to the Python hook any more. publictext.go holds the entry conditions and the Analyzer hook.

var (
	ptTextFlags = set("--title", "-t", "--body", "-b", "--subject", "--comment", "-c", "--notes", "-n", "--message", "-m",
		"--description", "--field-text")
	ptFileFlags = set("--body-file", "-F", "--notes-file", "--file", "--input")

	rePTRemote = regexp.MustCompile(`^(?:https?://(?:[^/@]+@)?[^/]+/|ssh://(?:[^/@]+@)?[^/]+/|git://[^/]+/|[` + reWord + `.-]+@[^:/]+:)` +
		`([` + reWord + `.-]+)/([` + reWord + `.-]+?)(?:\.git)?/?\n?\z`)
	rePTTarget    = regexp.MustCompile(`^(?:[^/]+/)?([` + reWord + `.-]+)/([` + reWord + `.-]+?)(?:\.git)?\z`)
	rePTStdinPath = regexp.MustCompile(`^/(?:dev/(?:stdin|fd/\p{Nd}+)|proc/[^/]+/fd/\p{Nd}+)\n?\z`)
	rePTRepos     = regexp.MustCompile(`^/?repos/([` + reWord + `.-]+)/([` + reWord + `.-]+)`)
	rePTRedirFd   = regexp.MustCompile(`\p{Nd}*>&\p{Nd}+|\p{Nd}*>[` + rxSpace + `]*/dev/null`)
)

type publicText struct {
	cwd     string
	dirs    map[string]struct{} // every directory a `cd` named
	pcwd    string              // where a git command runs
	pcwdOK  bool                // false where Python holds None: a `cd` target is only known at run time
	loaded  bool
	public  map[string]struct{}
	frozen  map[string]struct{}
	active  bool
	terms   string // the scrub_terms_file config row
	bodies  []Heredoc
	raw     string
	scanner *scrubScanner
}

// newPublicText is PublicText.__init__ without the DB read (loadConfig does that on first use).
func newPublicText(cwd, raw string) (*publicText, error) {
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, blockf("cannot determine the working directory: " + err.Error())
		}
		cwd = wd
	}
	return &publicText{cwd: cwd, dirs: map[string]struct{}{cwd: {}}, pcwd: cwd, pcwdOK: true, raw: raw}, nil
}

func pubBlock(msg string) error { return &PublicBlock{Msg: msg} }

// ptSlugs is PublicText.slugs.
func ptSlugs(v string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimFunc(s, pyIsSpace); s != "" {
			out[pyLowerFull(s)] = struct{}{}
		}
	}
	return out
}

// loadConfig reads public_text_repos, frozen_repos and scrub_terms_file once. No DB file: the rule is off.
func (pt *publicText) loadConfig() error {
	if pt.loaded {
		return nil
	}
	e, err := exportNeeded()
	if e == nil && err != nil {
		return err
	}
	cfg := map[string]string{}
	if e != nil {
		cfg = e.Config
	}
	pt.public, pt.frozen, pt.terms = ptSlugs(cfg["public_text_repos"]), ptSlugs(cfg["frozen_repos"]), cfg["scrub_terms_file"]
	pt.active = len(pt.public) > 0 || len(pt.frozen) > 0
	pt.loaded = true
	return nil
}

// ptNormpath is os.path.normpath: pyNormpath plus the POSIX rule that exactly two leading slashes stay two.
func ptNormpath(p string) string {
	out := pyNormpath(p)
	if strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///") && strings.HasPrefix(out, "/") && !strings.HasPrefix(out, "//") {
		return "/" + out
	}
	return out
}

// ── target repo

// gitOut is PublicText.git_out: stripped stdout, "" when git fails or cannot run.
func ptGitOut(cwd string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimFunc(pyDecodeReplace(out), pyIsSpace)
}

// ptRemoteSlug is PT_REMOTE.match(url) turned into the lower-case OWNER/NAME, "" for none.
func ptRemoteSlug(url string) string {
	if m := rePTRemote.FindStringSubmatch(url); m != nil {
		return pyLowerFull(m[1] + "/" + m[2])
	}
	return ""
}

// remoteSlug is PublicText.remote_slug; ok false stands for cwd None.
func (pt *publicText) remoteSlug(cwd string, ok bool, remote string, push bool) string {
	if !ok || !isDir(cwd) {
		return ""
	}
	args := []string{"remote", "get-url"}
	if push {
		args = append(args, "--push")
	}
	if url := ptGitOut(cwd, append(args, remote)...); url != "" {
		return ptRemoteSlug(url)
	}
	return ""
}

// target is PublicText.target with an explicit repo.
func ptTarget(explicit string) string {
	if m := rePTTarget.FindStringSubmatch(strings.TrimFunc(explicit, pyIsSpace)); m != nil {
		return pyLowerFull(m[1] + "/" + m[2])
	}
	return ""
}

// ── text of one write

// flagValues is PublicText.flag_values: ([text values], [file paths]) of the flags in args.
func ptFlagValues(args []string, textFlags, fileFlags map[string]struct{}) (texts, files []string) {
	isFlag := func(a string) bool { return in(textFlags, a) || in(fileFlags, a) }
	for j := 0; j < len(args); j++ {
		a := args[j]
		var val string
		have := false
		switch {
		case strings.HasPrefix(a, "--") && strings.Contains(a, "=") && isFlag(a[:strings.IndexByte(a, '=')]):
			i := strings.IndexByte(a, '=')
			a, val, have = a[:i], a[i+1:], true
		case isFlag(a):
			if j+1 < len(args) {
				val = args[j+1]
				j++
			}
			have = true
		case len(a) >= 2 && a[0] == '-' && allASCIILetters(a[1:]):
			letters := a[1:]
			for k := 0; k < len(letters); k++ {
				if !isFlag("-" + string(letters[k])) {
					continue
				}
				if rest := letters[k+1:]; rest != "" {
					if rest[0] == '=' {
						rest = strings.TrimLeft(rest, "=")
					}
					val = rest
				} else if j+1 < len(args) {
					val = args[j+1]
					j++
				}
				have = true
				a = "-" + string(letters[k])
				break
			}
		}
		if have {
			if in(textFlags, a) {
				texts = append(texts, val)
			} else {
				files = append(files, val)
			}
		}
	}
	return
}

func allASCIILetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isASCIILetter(s[i]) {
			return false
		}
	}
	return true
}

// ptDynamic is PT_DYNAMIC.search: a backtick, or $ before one of A-Za-z_{(@*#?!0-9$-.
func ptDynamic(s string) bool {
	if strings.Contains(s, "`") {
		return true
	}
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && strings.IndexByte("{(@*#?!$-_", s[i+1]) >= 0 || s[i] == '$' && (isASCIILetter(s[i+1]) || s[i+1] >= '0' && s[i+1] <= '9') {
			return true
		}
	}
	return false
}

// ptCatHeredoc is PT_CAT_HEREDOC.match(value).group(3): the body of a whole `$(cat <<'EOF' ... EOF)`.
func ptCatHeredoc(v string) (string, bool) {
	if !strings.HasPrefix(v, "$(") {
		return "", false
	}
	i := skipSpace(v, 2)
	if !strings.HasPrefix(v[i:], "cat") {
		return "", false
	}
	i += 3
	j := skipSpace(v, i)
	if j == i || !strings.HasPrefix(v[j:], "<<") {
		return "", false
	}
	i = j + 2
	if i < len(v) && v[i] == '-' {
		i++
	}
	i = skipSpace(v, i)
	quote := byte(0)
	if i < len(v) && (v[i] == '\'' || v[i] == '"') {
		quote = v[i]
		i++
	}
	ds := i
	for i < len(v) {
		r, size := nextRuneSize(v, i)
		if !isWord(r) {
			break
		}
		i += size
	}
	if i == ds {
		return "", false
	}
	delim := v[ds:i]
	if quote != 0 {
		if i >= len(v) || v[i] != quote {
			return "", false
		}
		i++
	}
	for i < len(v) && (v[i] == ' ' || v[i] == '\t') {
		i++
	}
	if i >= len(v) || v[i] != '\n' {
		return "", false
	}
	start := i + 1
	for k := start; k < len(v); k++ {
		if v[k] != '\n' {
			continue
		}
		p := k + 1
		for p < len(v) && (v[p] == ' ' || v[p] == '\t') {
			p++
		}
		if !strings.HasPrefix(v[p:], delim) {
			continue
		}
		p += len(delim)
		for p < len(v) && (v[p] == ' ' || v[p] == '\t') {
			p++
		}
		// \n?\s*\)\Z: whitespace, one ")" and the end
		q := skipSpace(v, p)
		if q == len(v)-1 && v[q] == ')' {
			return v[start:k], true
		}
	}
	return "", false
}

func nextRuneSize(s string, i int) (rune, int) {
	r := nextRune(s, i)
	return r, runeLen(s, i)
}

// piece is PublicText.piece.
func ptPiece(value, label string) (string, error) {
	if body, ok := ptCatHeredoc(value); ok {
		return body, nil
	}
	if ptDynamic(value) {
		return "", pubBlock("dynamic text in " + label + " cannot be inspected: write the text to a file and pass --body-file/-F")
	}
	return value, nil
}

// collect is PublicText.collect.
func (pt *publicText) collect(texts, files, headWords []string) ([]string, error) {
	var out []string
	for _, t := range texts {
		p, err := ptPiece(t, "a text flag")
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	for _, f := range files {
		if f == "-" || rePTStdinPath.MatchString(ptNormpath(f)) { // /dev/stdin, /dev/fd/N, /proc/*/fd/N are the heredoc payload
			var found []string
			for _, h := range pt.bodies {
				last := pyFields(h.Head[strings.LastIndexAny(h.Head, ";&|(")+1:]) // re.split(r"[;&|(]", h)[-1]
				ok := true
				for _, w := range headWords {
					if !contains(last, w) {
						ok = false
						break
					}
				}
				if ok {
					found = append(found, h.Body)
				}
			}
			if len(found) == 0 {
				return nil, pubBlock("stdin text cannot be inspected: write the text to a file and pass --body-file/-F")
			}
			out = append(out, found...)
			continue
		}
		if strings.TrimFunc(f, pyIsSpace) == "" {
			return nil, pubBlock("an empty or process-substitution text file (-F <(...)) cannot be inspected: write the text to a file first and pass --body-file/-F")
		}
		path := f
		if !strings.HasPrefix(f, "/") {
			base := pt.cwd
			if pt.pcwdOK {
				base = pt.pcwd
			}
			path = pyJoin(base, f)
		}
		text, err := pt.bodyFileText(f, path, headWords)
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, nil
}

func contains(ws []string, w string) bool {
	for _, x := range ws {
		if x == w {
			return true
		}
	}
	return false
}

// standalone is PublicText.standalone: raw is one plain `head ...` command (no chain, pipe, background, substitution,
// process substitution or newline outside quotes), so nothing in it can change the file between check and write.
func ptStandalone(raw, head string) bool {
	s := rePTRedirFd.ReplaceAllString(strings.TrimFunc(raw, pyIsSpace), " ")
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			}
		case q == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				q = 0
			} else if c == '`' || strings.HasPrefix(s[i:], "$(") {
				return false
			}
		case c == '\\':
			if i+1 < len(s) && s[i+1] == '\n' {
				return false
			}
			i++
		case c == '\'' || c == '"':
			q = c
		case strings.IndexByte(";&|\n`", c) >= 0 || strings.HasPrefix(s[i:], "$(") || strings.HasPrefix(s[i:], "<(") || strings.HasPrefix(s[i:], ">("):
			return false
		}
	}
	if q != 0 {
		return false
	}
	words, err := shlex.Split(s)
	if err != nil {
		return false
	}
	if len(words) > 0 && IsAssign(words[0]) {
		var kept []string
		for _, w := range words {
			if !IsAssign(w) {
				kept = append(kept, w)
			}
		}
		words = kept
	}
	return len(words) > 0 && pyBasename(words[0]) == head
}

// bodyFileText is PublicText.body_file_text: the file is read at hook time, so the command must be exactly one call.
func (pt *publicText) bodyFileText(f, path string, headWords []string) (string, error) {
	if !ptStandalone(pt.raw, headWords[0]) {
		return "", pubBlock("text file " + pyRepr(f) + " is read before the command runs, so the command must be only the " + headWords[0] +
			" call (no ; && || | & $( ` <( >( or newline): write the file in a separate command first")
	}
	real := pyRealpath(path)
	for _, p := range []string{path, real} {
		if st, err := os.Lstat(p); err == nil && st.Mode()&fs.ModeSymlink != 0 {
			return "", pubBlock("text file " + pyRepr(f) + " is a symlink: pass a regular --body-file/-F")
		}
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", pubBlock("cannot read text file " + pyRepr(f) + " (" + pyStrerror(err) + "): pass a readable --body-file/-F")
	}
	if !st.Mode().IsRegular() { // a FIFO or device would hang the hook or read the wrong bytes
		return "", pubBlock("text file " + pyRepr(f) + " is not a regular file: pass a regular --body-file/-F")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Nlink > 1 {
		return "", pubBlock("text file " + pyRepr(f) + " has other hard links, so its content may change elsewhere: pass a file with one link")
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return "", pubBlock("cannot read text file " + pyRepr(f) + " (" + pyStrerror(err) + "): pass a readable --body-file/-F")
	}
	return pyDecodeReplace(b), nil
}

// ── terms + checker

// guard is PublicText.guard: the scanner with the owner terms, or the refusal that says why there is none.
func (pt *publicText) guard() (*scrubScanner, error) {
	if pt.scanner != nil {
		return pt.scanner, nil
	}
	if root := ptRepoRoot(); root != "" { // any failure to load the checker fails closed
		for _, f := range []string{"public_text_guard.py", "scrub_scan.py"} {
			if _, err := os.ReadFile(root + "/ci/" + f); err != nil {
				name := "OSError"
				switch {
				case errors.Is(err, fs.ErrNotExist):
					name = "FileNotFoundError"
				case errors.Is(err, fs.ErrPermission):
					name = "PermissionError"
				}
				return nil, pubBlock("public-text-guard cannot be loaded (" + name + "); fix ci/public_text_guard.py")
			}
		}
	}
	terms, err := loadTermsOrFail(pt.terms)
	if err != nil {
		return nil, pubBlock(err.Error())
	}
	pt.scanner = newScrubScanner(terms)
	return pt.scanner, nil
}

// ptRepoRoot is the checkout the hook belongs to (dirname^3 of the hook), "" when --py-hook was not given.
func ptRepoRoot() string {
	h := mustHost()
	if h.hookReal == "" {
		return ""
	}
	return pyDirname(pyDirname(pyDirname(h.hookReal)))
}

type ptPush struct{ repo, base, head string }

// verdict is PublicText.verdict.
func (pt *publicText) verdict(texts, refs []string, pushes []ptPush) error {
	sc, err := pt.guard()
	if err != nil {
		return err
	}
	var ids []string
	for _, t := range texts {
		ids = append(ids, sc.scanLines(t)...)
	}
	for _, r := range refs {
		ids = append(ids, sc.scanLines(r)...)
	}
	for _, p := range pushes {
		root := ptRepoRoot()
		if root == "" {
			return pubBlock("cannot check the push (ci/scrub_allowlist.txt cannot be located: the shim must pass --py-hook <abs path of roles/hooks/worker-guard.py>)")
		}
		hits, err := checkPush(p.repo, p.base, p.head, sc, root+"/ci/scrub_allowlist.txt")
		if err != nil {
			var ue *usageError
			if errors.As(err, &ue) {
				return pubBlock("cannot check the push (" + ue.Msg + ")")
			}
			return err
		}
		ids = append(ids, hits...)
	}
	if len(ids) > 0 {
		uniq := map[string]struct{}{}
		for _, id := range ids {
			uniq[id] = struct{}{}
		}
		sorted := make([]string, 0, len(uniq))
		for id := range uniq {
			sorted = append(sorted, id)
		}
		sort.Strings(sorted)
		return pubBlock("public text: " + strconv.Itoa(len(ids)) + " hits: " + strings.Join(sorted, ", "))
	}
	return nil
}

// ── per command

// also is PublicText.also: slug plus the push URL's repo and the repos of the other directories seen in the command.
func (pt *publicText) also(slug, remote string, extra bool) []string {
	pushed := pt.remoteSlug(pt.pcwd, pt.pcwdOK, remote, true) // a pushurl may differ from the fetch url
	base := []string{slug}
	if pushed != "" && pushed != slug {
		base = append(base, pushed)
	}
	if !extra {
		return base
	}
	var dirs []string
	for d := range pt.dirs {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		if s := pt.remoteSlug(d, true, remote, false); s != "" {
			base = append(base, s)
		}
	}
	return base
}

func (pt *publicText) frozenHit(slug, remote string, extra bool) error {
	for _, s := range pt.also(slug, remote, extra) {
		if s != "" && in(pt.frozen, s) {
			return pubBlock("repo " + s + " is frozen, G-021; work in the public repo")
		}
	}
	return nil
}

func (pt *publicText) applies(slug, remote string, extra bool) bool {
	if len(pt.public) == 0 {
		return false
	}
	ss := pt.also(slug, remote, extra)
	if ss[0] == "" {
		return true
	}
	for _, s := range ss {
		if in(pt.public, s) {
			return true
		}
	}
	return false
}

// cd is PublicText.cd.
func (pt *publicText) cd(rest []string) {
	tgt, have := "", false
	for _, a := range rest {
		if !strings.HasPrefix(a, "-") {
			tgt, have = a, true
			break
		}
	}
	if !have || ptDynamic(tgt) || tgt == "-" || !pt.pcwdOK {
		if tgt != "" {
			pt.pcwdOK = false
		}
		return
	}
	pt.pcwd = ptNormpath(pyJoin(pt.pcwd, expandUser(tgt)))
	pt.dirs[pt.pcwd] = struct{}{}
}

// ptURLRemote is re.match(r"[\w.-]+://|[\w.-]+@", remote).
func ptURLRemote(remote string) bool {
	i := 0
	for i < len(remote) {
		r, size := nextRuneSize(remote, i)
		if !(isWord(r) || r == '.' || r == '-') {
			break
		}
		i += size
	}
	return i > 0 && (strings.HasPrefix(remote[i:], "://") || strings.HasPrefix(remote[i:], "@"))
}

// ptPushArgs is PublicText.push_args.
func ptPushArgs(rest []string) (pos, opts []string) {
	var repo *string
	for j := 0; j < len(rest); {
		a := rest[j]
		if in(set("--repo", "-o", "--push-option", "--receive-pack", "--exec"), a) && j+1 < len(rest) {
			opts = append(opts, a)
			if a == "--repo" {
				r := rest[j+1]
				repo = &r
			}
			j += 2
			continue
		}
		if strings.HasPrefix(a, "--repo=") {
			r := a[len("--repo="):]
			repo = &r
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			opts = append(opts, a)
		} else {
			pos = append(pos, a)
		}
		j++
	}
	if repo != nil { // --repo replaces the remote: every positional is a refspec
		pos = append([]string{*repo}, pos...)
	}
	return
}

// gitWalk is the option walk at the top of PublicText.git: the directory of the command, the index of the
// subcommand and whether `-c alias.*` hides it. --git-dir adds its directory to the ones the rule knows even when
// the subcommand is no write, so the Analyzer hook runs it for every git command.
func (pt *publicText) gitWalk(args []string) (cwd string, cwdOK bool, j int, alias bool) {
	cwd, cwdOK = pt.pcwd, pt.pcwdOK
	for j < len(args) && strings.HasPrefix(args[j], "-") {
		a := args[j]
		switch {
		case a == "-C" && j+1 < len(args) && cwdOK:
			cwd = ptNormpath(pyJoin(cwd, args[j+1]))
			j++
		case a == "-C":
			cwdOK = false // -C without a value or from an unknown directory
		case a == "--git-dir" || a == "--work-tree" || strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree="):
			eq := strings.Contains(a, "=")
			val := ""
			if eq {
				val = a[strings.IndexByte(a, '=')+1:]
			} else if j+1 < len(args) {
				val = args[j+1]
			}
			if strings.HasPrefix(a, "--git-dir") && val != "" && cwdOK {
				pt.dirs[ptNormpath(pyJoin(cwd, val))] = struct{}{}
			}
			if !eq {
				j++
			}
		case a == "-c" || a == "--namespace" || a == "--exec-path":
			if a == "-c" && j+1 < len(args) && strings.HasPrefix(pyLowerFull(args[j+1]), "alias.") {
				alias = true
			}
			j++
		}
		j++
	}
	return
}

// git is the rest of PublicText.git, after gitWalk.
func (pt *publicText) git(args []string, cwd string, cwdOK bool, j int, alias bool) error {
	if j >= len(args) || !(in(ptGitSubs, args[j]) || alias) {
		return nil
	}
	sub, rest := args[j], args[j+1:]
	if sub == "tag" && !anyOf(rest, annotatedTagFlag) {
		return nil // a lightweight local tag is not a write to the remote; its name is checked when it is pushed
	}
	remote := "origin"
	if sub == "push" {
		if pos, _ := ptPushArgs(rest); len(pos) > 0 {
			remote = pos[0]
		}
	}
	direct := sub == "push" && remote != "" && ptURLRemote(remote)
	slug := ""
	if direct {
		slug = ptRemoteSlug(remote)
	} else {
		slug = pt.remoteSlug(cwd, cwdOK, remote, false)
	}
	if err := pt.frozenHit(slug, remote, !direct); err != nil {
		return err
	}
	if !pt.applies(slug, remote, !direct) {
		return nil
	}
	if alias {
		return pubBlock("git -c alias.* hides the subcommand from the public-text check: run the real subcommand")
	}
	texts, files := ptFlagValues(rest, set("-m", "--message"), set("-F", "--file"))
	if sub == "push" {
		return pt.push(cwd, cwdOK, remote, rest)
	}
	pieces, err := pt.collect(texts, files, []string{"git", sub})
	if err != nil {
		return err
	}
	var refs []string
	if sub == "tag" {
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				refs = append(refs, a)
				break
			}
		}
	}
	return pt.verdict(pieces, refs, nil)
}

var reRefsPrefix = regexp.MustCompile(`^refs/(?:heads|tags)/`)

// push is PublicText.push.
func (pt *publicText) push(cwd string, cwdOK bool, remote string, rest []string) error {
	if !cwdOK || !isDir(cwd) {
		return pubBlock("push from a directory known only at run time: cd to a static path first")
	}
	pos, opts := ptPushArgs(rest)
	var specs []string
	if len(pos) > 1 {
		specs = pos[1:]
	}
	has := func(names ...string) bool {
		for _, o := range opts {
			for _, n := range names {
				if o == n {
					return true
				}
			}
		}
		return false
	}
	del := has("--delete", "-d")
	if has("--all", "--mirror", "--tags") && len(specs) == 0 {
		sel := []string{"refs/heads", "refs/tags"}
		if has("--all") {
			sel = []string{"refs/heads"}
		} else if has("--tags") {
			sel = []string{"refs/tags"}
		}
		for _, n := range pyFields(ptGitOut(cwd, append([]string{"for-each-ref", "--format=%(refname)"}, sel...)...)) {
			specs = append(specs, n+":"+n)
		}
	}
	if len(specs) == 0 {
		cur := ptGitOut(cwd, "symbolic-ref", "--short", "HEAD")
		if cur == "" {
			return pubBlock("push with no refspec from a detached HEAD: name the branch to push")
		}
		specs = []string{cur}
	}
	var refs []string
	var pushes []ptPush
	for _, spec := range specs {
		s := strings.TrimLeft(spec, "+")
		src, dst := s, s
		if i := strings.IndexByte(s, ':'); i >= 0 {
			src, dst = s[:i], s[i+1:]
		}
		if del {
			src, dst = "", s
		}
		dstName := reRefsPrefix.ReplaceAllString(dst, "")
		if (dst == "" || dst == "HEAD") && src != "" {
			if dstName = ptGitOut(cwd, "symbolic-ref", "--short", "HEAD"); dstName == "" {
				dstName = src
			}
		}
		if dstName != "" {
			refs = append(refs, dstName)
		}
		if src == "" {
			continue // a delete publishes no commits
		}
		head := ptGitOut(cwd, "rev-parse", "--verify", "--quiet", src+"^{commit}")
		if head == "" {
			return pubBlock("cannot resolve push source " + pyRepr(src) + ": push an existing branch or commit")
		}
		base := ptGitOut(cwd, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+dstName+"^{commit}")
		if base == "" {
			base = ptGitOut(cwd, "merge-base", head, "origin/main")
		}
		if base == "" {
			return pubBlock("no base to check the push against: fetch origin so origin/main exists")
		}
		pushes = append(pushes, ptPush{cwd, base, head})
	}
	return pt.verdict(nil, refs, pushes)
}

// gh is PublicText.gh.
func (pt *publicText) gh(a []string) error {
	repo := ""
	for k, x := range a {
		switch {
		case (x == "-R" || x == "--repo") && k+1 < len(a):
			repo = a[k+1]
		case strings.HasPrefix(x, "--repo="):
			repo = x[len("--repo="):]
		case strings.HasPrefix(x, "-R") && len(x) > 2 && !strings.ContainsFunc(x[2:], pyIsSpace):
			repo = x[2:]
		}
	}
	var pos []string
	for _, x := range a {
		if !strings.HasPrefix(x, "-") {
			pos = append(pos, x)
		}
	}
	if len(pos) < 2 && !(len(pos) > 0 && pos[0] == "api") {
		return nil
	}
	if pos[0] == "api" {
		return pt.ghAPI(a, repo)
	}
	if subs, ok := ptGhWrites[pos[0]]; !ok || !in(subs, pos[1]) {
		return nil
	}
	slug := ""
	if repo != "" {
		slug = ptTarget(repo)
	} else {
		slug = pt.remoteSlug(pt.pcwd, pt.pcwdOK, "origin", false)
	}
	if err := pt.frozenHit(slug, "origin", repo == ""); err != nil {
		return err
	}
	if !pt.applies(slug, "origin", repo == "") {
		return nil
	}
	tf := ptTextFlags
	if pos[0] == "pr" && pos[1] == "review" { // --comment / -c is a switch there
		tf = map[string]struct{}{}
		for k := range ptTextFlags {
			if k != "--comment" && k != "-c" {
				tf[k] = struct{}{}
			}
		}
	}
	texts, files := ptFlagValues(a, tf, ptFileFlags)
	var refs []string
	for k, x := range a {
		if (x == "--head" || x == "-H") && k+1 < len(a) {
			v := a[k+1]
			refs = append(refs, v[strings.LastIndexByte(v, ':')+1:])
		}
	}
	if pos[0] == "release" && len(pos) > 2 {
		refs = append(refs, pos[2])
	}
	pieces, err := pt.collect(texts, files, []string{"gh", pos[0]})
	if err != nil {
		return err
	}
	for _, r := range refs {
		if ptDynamic(r) {
			return pubBlock("dynamic ref name cannot be inspected: write the name out")
		}
	}
	return pt.verdict(pieces, refs, nil)
}

// ghAPI is PublicText.gh_api.
func (pt *publicText) ghAPI(a []string, repo string) error {
	method, endpoint := "", ""
	haveEndpoint := false
	var texts, files []string
	for j := 0; j < len(a); j++ {
		x := a[j]
		switch {
		case (x == "-X" || x == "--method") && j+1 < len(a):
			method = strings.ToUpper(a[j+1])
			j++
		case strings.HasPrefix(x, "--method="):
			method = strings.ToUpper(x[len("--method="):])
		case in(set("-f", "-F", "--field", "--raw-field"), x) && j+1 < len(a):
			kv := a[j+1]
			j++
			v := ""
			if i := strings.IndexByte(kv, '='); i >= 0 {
				v = kv[i+1:]
			}
			if (x == "-F" || x == "--field") && strings.HasPrefix(v, "@") {
				files = append(files, v[1:])
			} else {
				texts = append(texts, v)
			}
		case (strings.HasPrefix(x, "--field=") || strings.HasPrefix(x, "--raw-field=")) && !strings.Contains(x, "\n"):
			kv := x[strings.IndexByte(x, '=')+1:]
			v := ""
			if i := strings.IndexByte(kv, '='); i >= 0 {
				v = kv[i+1:]
			}
			texts = append(texts, v)
		case x == "--input" && j+1 < len(a):
			files = append(files, a[j+1])
			j++
		case strings.HasPrefix(x, "--input="):
			files = append(files, x[len("--input="):])
		case in(set("-H", "--header", "-q", "--jq", "-t", "--template", "--hostname", "--cache", "-R", "--repo"), x) && j+1 < len(a):
			j++
		case !strings.HasPrefix(x, "-") && x != "api" && !haveEndpoint:
			endpoint, haveEndpoint = x, true
		}
	}
	hasBody := len(texts) > 0 || len(files) > 0
	if !(hasBody || method == "POST" || method == "PATCH" || method == "PUT" || method == "DELETE") || method == "GET" {
		return nil
	}
	m := rePTRepos.FindStringSubmatch(endpoint)
	slug := ""
	switch {
	case m != nil:
		slug = pyLowerFull(m[1] + "/" + m[2])
	case repo != "":
		slug = ptTarget(repo)
	default:
		slug = pt.remoteSlug(pt.pcwd, pt.pcwdOK, "origin", false)
	}
	extra := m == nil && repo == ""
	if err := pt.frozenHit(slug, "origin", extra); err != nil {
		return err
	}
	if !pt.applies(slug, "origin", extra) {
		return nil
	}
	pieces, err := pt.collect(texts, files, []string{"gh", "api"})
	if err != nil {
		return err
	}
	if endpoint != "" && m == nil {
		pieces = append(pieces, endpoint)
	}
	return pt.verdict(pieces, nil, nil)
}
