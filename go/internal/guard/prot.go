package guard

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

// T-0925: the protected trees for workers (ports of prot_trees, prot_path, prot_glob, prot_edit_path, opt_values,
// Analyzer.prot_hit, prot_redirect and prot_write of worker-guard.py). One layer over the cfg_* detector: ~/.local/go
// and ~/.config/go, the guard trees, and four trees inside the live checkout (go/, roles/hooks, roles/settings, bin/).
// Reads and runs stay allowed; a worktree is not under them.

var protLiveSubdirs = [][]string{{"go"}, {"roles", "hooks"}, {"roles", "settings"}, {"bin"}}

var protMarkers = []string{".local/go", ".config/go", ".local/lib/factory", ".config/factory", "roles/hooks", "roles/settings",
	"run/settings", "run/guard_impl", "run/cli_impl"}

const protWhy = "it is a protected tree for workers (~/.local/go and ~/.config/go: the Go toolchain and `go env -w`; ~/.local/bin: first on the tick's PATH; " +
	"~/.config/factory, ~/.local/lib/factory, <live>/run/settings, <live>/run/guard_impl: the guard; <live>/go, " +
	"<live>/bin, <live>/roles/hooks, <live>/roles/settings: what the tick builds, runs and renders); only the owner " +
	"and the tick change them. Work in your worktree"

// protTrees is prot_trees: the protected trees, each spelled and resolved. The live homes are the passwd-derived ones,
// or the WORKER_GUARD_LIVE_HOME override (tests only); never the hook's own repo root.
func protTrees() []string {
	h := mustHost()
	homes := h.liveHomes
	if o := os.Getenv("WORKER_GUARD_LIVE_HOME"); o != "" {
		homes = []string{pyRealpath(o)}
	}
	base := []string{h.cfgDir, h.libDir, pyJoin(pyJoin(h.pwDir, ".config"), "factory"),
		pyJoin(pyJoin(pyJoin(h.pwDir, ".local"), "lib"), "factory"), pyJoin(pyJoin(h.pwDir, ".local"), "go"),
		pyJoin(pyJoin(h.pwDir, ".config"), "go")}
	base = append(base, h.binDirs...) // T-0957 family
	for _, home := range homes {
		base = append(base, pyJoin(pyJoin(home, "run"), "settings"), pyJoin(pyJoin(home, "run"), "guard_impl"), pyJoin(pyJoin(home, "run"), "cli_impl"))
		for _, sub := range protLiveSubdirs {
			p := home
			for _, c := range sub {
				p = pyJoin(p, c)
			}
			base = append(base, p)
		}
	}
	seen := map[string]struct{}{}
	for _, b := range base {
		seen[pyNormpath(b)] = struct{}{}
		seen[pyRealpath(b)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// protPath is prot_path: absolute, glob-free p (spelled or resolved) is a protected tree, inside one, or (ancestor)
// holds one.
func protPath(p string, ancestor bool) bool {
	trees := protTrees()
	for _, c := range uniq([]string{pyNormpath(p), pyRealpath(p)}) {
		for _, d := range trees {
			if c == d || Under(c, d) || (ancestor && Under(d, c)) {
				return true
			}
		}
	}
	return false
}

// protGlob is prot_glob: glob pattern p (absolute) can match a protected tree, a path inside one or (ancestor) a
// path holding one.
func protGlob(p string, ancestor bool) bool {
	pat := strings.Split(strings.Trim(pyNormpath(p), "/"), "/")
	var m func(i, j int, parts []string) bool
	m = func(i, j int, parts []string) bool {
		if j == len(parts) {
			return true // the whole tree is matched: the pattern names it or something inside it
		}
		if i == len(pat) {
			return ancestor
		}
		if pat[i] == "**" { // B6: any number of components, zero included
			return m(i+1, j, parts) || m(i, j+1, parts)
		}
		return fnmatchCase(parts[j], pat[i]) && m(i+1, j+1, parts)
	}
	for _, d := range protTrees() {
		if m(0, 0, strings.Split(strings.Trim(d, "/"), "/")) {
			return true
		}
	}
	return false
}

// protEditPath is prot_edit_path (persist_edit side, gap g).
func protEditPath(p string, spelled bool) bool {
	if spelled || strings.ContainsAny(p, "$`") {
		for _, m := range protMarkers {
			if strings.Contains(p, m) {
				return true
			}
		}
		return hasLocalBin(p)
	}
	return protPath(p, false)
}

// optValues is opt_values: the values of the options named in longs (exact word: --output X, --output=X, -o X) and of
// the single-dash letters (curl -o X, -oX, -sSLo X: glued, or the next word), in order.
func optValues(args []string, letters string, longs ...string) []string {
	var out []string
	k := 0
	for k < len(args) {
		a := args[k]
		k++
		opt, val, eq := strings.Cut(a, "=")
		if inStrs(longs, opt) || longOpt(opt, longs...) {
			if eq {
				out = append(out, val)
			} else if k < len(args) {
				out = append(out, args[k])
				k++
			}
		} else if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 1 {
			for i := 1; i < len(a); i++ {
				if strings.IndexByte(letters, a[i]) >= 0 {
					if a[i+1:] != "" {
						out = append(out, a[i+1:])
					} else if k < len(args) {
						out = append(out, args[k])
						k++
					}
					break
				}
			}
		}
	}
	return out
}

// longOpt is long_opt (B5): GNU getopt accepts a unique prefix of a long option. True when opt (a `--word` without any
// =value) is one of names or a prefix of one, at least one letter long after the dashes; an ambiguous prefix counts too.
func longOpt(opt string, names ...string) bool {
	if !strings.HasPrefix(opt, "--") || len(opt) < 3 {
		return false
	}
	for _, n := range names {
		if strings.HasPrefix(n, opt) {
			return true
		}
	}
	return false
}

// protDestIndexes is prot_dest_indexes (B1): the words that can be the destination of cp/install/ln -s/rsync/dd when
// options may trail it.
func protDestIndexes(args []string) map[int]bool {
	n := len(args)
	isopt := make([]bool, n)
	var nonopt []int
	for i, a := range args {
		isopt[i] = strings.HasPrefix(a, "-") && len(a) > 1
		if !isopt[i] {
			nonopt = append(nonopt, i)
		}
	}
	res := map[int]bool{}
	if n > 0 {
		res[n-1] = true
	}
	if len(nonopt) > 0 {
		res[nonopt[len(nonopt)-1]] = true
	}
	t := n
	for t > 0 && (isopt[t-1] || (t > 1 && isopt[t-2])) {
		t--
	}
	var pre []int
	for _, j := range nonopt {
		if j < t {
			pre = append(pre, j)
		}
	}
	if len(pre) >= 2 {
		res[pre[len(pre)-1]] = true
	}
	return res
}

// targetDirValues is target_dir_values (N2): the -t/--target-directory values of cp/install/ln in every spelling.
func targetDirValues(args []string) []string {
	var out []string
	k := 0
	for k < len(args) {
		a := args[k]
		k++
		if strings.HasPrefix(a, "--") {
			opt, val, eq := strings.Cut(a, "=")
			if longOpt(opt, "--target-directory") {
				if eq {
					out = append(out, val)
				} else if k < len(args) {
					out = append(out, args[k])
					k++
				}
			}
		} else if strings.HasPrefix(a, "-") && len(a) > 1 {
		letters:
			for i := 1; i < len(a); i++ {
				switch {
				case a[i] == 't':
					if a[i+1:] != "" {
						out = append(out, a[i+1:])
					} else if k < len(args) {
						out = append(out, args[k])
						k++
					}
					break letters
				case strings.IndexByte("gmoS", a[i]) >= 0:
					break letters
				}
			}
		}
	}
	return out
}

const tarValueLetters = "fCgTXFLbHVIKN" // tar --help: the short options that take a value

// tarModeLetters is tar_mode_letters (N2): the letters of a bundle that can be modes.
func tarModeLetters(word string, old bool) string {
	letters := word[1:]
	if old {
		return letters
	}
	if i := strings.IndexAny(letters, tarValueLetters); i >= 0 {
		return letters[:i+1]
	}
	return letters
}

func inStrs(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// protHit is Analyzer.prot_hit. base is the directory a relative word is joined to; baseKnown=false is Python's None.
func (a *Analyzer) protHit(word string, ancestor bool, base string, baseKnown bool) bool {
	for _, w := range uniq([]string{word, valueOf(word)}) {
		w = substVars(w, a.vars)
		t := a.cfgExpand(w)
		unres := tildeUnres(w, baseKnown)
		for _, e := range braceExpand(t) {
			if unres {
				for _, m := range protMarkers {
					if strings.Contains(e, m) {
						return true // rule A: an unresolvable `~name/...` that spells a protected tree name counts
					}
				}
				if hasLocalBin(e) {
					return true
				}
			}
			if strings.ContainsAny(e, "$`") {
				for _, m := range protMarkers {
					if strings.Contains(e, m) {
						return true
					}
				}
				if hasLocalBin(e) {
					return true
				}
				continue
			}
			if e == "" || e == "-" || (!strings.HasPrefix(e, "/") && !baseKnown) {
				continue
			}
			full := e
			if !strings.HasPrefix(e, "/") {
				full = pyJoin(base, e)
			}
			if hasGlob(e) {
				if protGlob(full, ancestor) {
					return true
				}
			} else if protPath(full, ancestor) {
				return true
			}
		}
	}
	return false
}

func (a *Analyzer) protRedirect(target string) error {
	if a.protHit(target, false, a.ccwd, a.ccwdKnown) {
		return blockf("a redirect into a protected tree: " + protWhy)
	}
	return nil
}

var (
	reGoEnvWU   = regexp.MustCompile(`^--?[wu](=|$)`)
	reCurlOJ    = regexp.MustCompile(`^-[A-Za-z]*[OJ]`)
	reInPlace   = regexp.MustCompile(`^-[A-Za-z]*i`)
	reSymShort  = regexp.MustCompile(`^-[A-Za-z]+$`)
	reTarOld    = regexp.MustCompile(`^[A-Za-z]+$`)
	protCCNames = set("gcc", "g++", "cc", "c++", "clang", "clang++", "ld", "as", "rustc")
	protCloneV  = set("-b", "--branch", "--depth", "-o", "--origin", "--template", "--reference", "--reference-if-able",
		"-c", "--config", "-j", "--jobs", "--filter", "--separate-git-dir", "-u", "--upload-pack",
		"--server-option", "--shallow-since", "--shallow-exclude", "--bundle-uri")
	protGitOpt = set("-c", "--attr-source", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--super-prefix")
)

// unzipListingOpts is unzip_listing: unzip reads options only before the zip name.
func unzipListingOpts(args []string) bool {
	k := 0
	for k < len(args) {
		x := args[k]
		if in(unzipNoExtract, x) {
			return true
		}
		if x == "-d" {
			k++
		} else if !strings.HasPrefix(x, "-") || x == "-" {
			return false
		}
		k++
	}
	return false
}

// protWrite is Analyzer.prot_write.
func (a *Analyzer) protWrite(name string, rest []string, assigns []string) error {
	args := dropDashDash(rest)
	block := func(what string) error { return blockf("`" + name + "` on " + what + ": " + protWhy) }
	const tree = "a protected tree"
	const unknownDir = "a directory known only at run time (a `cd` into an unresolved path, then an extractor)"
	hit := func(w string, anc bool, base string, known bool) error {
		if w != "" && a.protHit(w, anc, base, known) {
			return block(tree)
		}
		return nil
	}
	hitc := func(w string) error { return hit(w, false, a.ccwd, a.ccwdKnown) }
	cwdWrite := func(strict bool, base string, known bool, anc ...bool) error {
		if strict && a.cwdUnres && base == a.ccwd && known == a.ccwdKnown { // N3: the cwd is only the known stand-in of an unresolved `cd`
			return block(unknownDir)
		}
		if !known {
			if strict {
				return block(unknownDir)
			}
		} else if protPath(base, len(anc) > 0 && anc[0]) {
			if len(anc) > 0 && anc[0] {
				return block("the working directory, which is in a protected tree or holds one (the archive content is unknown)")
			}
			return block("the working directory, which is in a protected tree")
		}
		return nil
	}
	switch {
	case in(CfgWriters, name):
		destOnly := name == "cp" || name == "install" || name == "rsync" || name == "dd"
		if !destOnly && name == "ln" {
			for _, x := range args {
				if longOpt(x, "--symbolic") || (reSymShort.MatchString(x) && strings.Contains(x, "s")) {
					destOnly = true
				}
			}
		}
		if destOnly && (name == "cp" || name == "install" || name == "ln") {
			if tvals := targetDirValues(args); len(tvals) > 0 { // N2: -t names the destination; the rest are sources
				for _, v := range tvals {
					if err := hitc(v); err != nil {
						return err
					}
				}
				return a.protParentCopy(name, args, tvals)
			}
		}
		var dests map[int]bool
		if destOnly {
			dests = protDestIndexes(args)
		}
		for j, x := range args {
			if destOnly && !(dests[j] || isDestArg(args, j) || (longOpt(strings.SplitN(x, "=", 2)[0], "--target-directory") && strings.Contains(x, "="))) {
				continue
			}
			if err := hit(x, in(CfgRemovers, name) || (name == "mv" && j < len(args)-1), a.ccwd, a.ccwdKnown); err != nil {
				return err
			}
		}
		if name == "cp" || name == "mv" || name == "ln" || name == "install" || name == "rsync" {
			return a.protParentCopy(name, args, nil)
		}
	case (name == "sed" || name == "perl" || name == "gawk") && protInPlace(args):
		for _, x := range args {
			if err := hitc(x); err != nil {
				return err
			}
		}
	case name == "curl":
		for _, v := range optValues(args, "oDc", "--output", "--dump-header", "--cookie-jar", "--trace", "--trace-ascii", "--stderr") {
			if err := hitc(v); err != nil {
				return err
			}
		}
		dirs := optValues(args, "", "--output-dir")
		for _, d := range dirs {
			if err := hitc(d); err != nil {
				return err
			}
		}
		if len(dirs) == 0 {
			remote := false
			for _, x := range args {
				if reCurlOJ.MatchString(x) || x == "--remote-name" || x == "--remote-name-all" || x == "--remote-header-name" {
					remote = true
				}
			}
			if remote {
				return cwdWrite(false, a.ccwd, a.ccwdKnown)
			}
		}
	case name == "wget":
		outs := optValues(args, "O", "--output-document")
		for _, v := range append(append([]string{}, outs...), optValues(args, "oa", "--output-file", "--append-output", "--save-cookies")...) {
			if err := hitc(v); err != nil {
				return err
			}
		}
		prefix := optValues(args, "P", "--directory-prefix")
		for _, v := range prefix {
			if err := hitc(v); err != nil {
				return err
			}
		}
		if len(outs) == 0 && len(prefix) == 0 {
			return cwdWrite(false, a.ccwd, a.ccwdKnown)
		}
	case name == "go":
		sub := ""
		for k := 0; k < len(args); k++ { // the first word that is not an option (or the value of -C)
			if args[k] == "-C" || args[k] == "--C" {
				k++
			} else if !strings.HasPrefix(args[k], "-") {
				sub = args[k]
				break
			}
		}
		if sub == "env" {
			for _, x := range args {
				if reGoEnvWU.MatchString(x) {
					return block("`go env -w|-u` (it writes ~/.config/go/env)")
				}
			}
		}
		base, known := a.ccwd, a.ccwdKnown
		if cds := optValues(args, "", "-C", "--C"); len(cds) > 0 {
			cdir := cds[len(cds)-1]
			if err := hitc(cdir); err != nil {
				return err
			}
			cdir = a.cfgExpand(cdir)
			if !strings.HasPrefix(cdir, "/") && known {
				base = pyJoin(a.ccwd, cdir)
			} else if strings.HasPrefix(cdir, "/") {
				base, known = cdir, true
			} else {
				known = false
			}
		}
		if sub == "build" || sub == "test" || sub == "install" {
			for _, v := range optValues(args, "", "-o", "--o") {
				if err := hit(v, false, base, known); err != nil {
					return err
				}
			}
			if sub == "install" {
				for _, asn := range assigns {
					if v, ok := strings.CutPrefix(asn, "GOBIN="); ok {
						if err := hit(v, false, base, known); err != nil {
							return err
						}
					}
				}
			}
		}
	case in(protCCNames, name):
		for k, x := range args {
			switch {
			case (x == "-o" || longOpt(x, "--output")) && k+1 < len(args):
				if err := hitc(args[k+1]); err != nil {
					return err
				}
			case strings.Contains(x, "=") && longOpt(strings.SplitN(x, "=", 2)[0], "--output"):
				if err := hitc(x); err != nil {
					return err
				}
			case strings.HasPrefix(x, "-o") && len(x) > 2:
				if err := hitc(x[2:]); err != nil {
					return err
				}
			}
			if name == "gcc" {
				switch {
				case x == "-MF" && k+1 < len(args):
					if err := hitc(args[k+1]); err != nil {
						return err
					}
				case strings.HasPrefix(x, "-MF") && len(x) > 3:
					if err := hitc(x[3:]); err != nil {
						return err
					}
				}
			}
		}
	case name == "gzip":
		stdout := false
		decompress := false
		for _, x := range args {
			if x == "-c" || x == "--stdout" || x == "--to-stdout" || (reSymShort.MatchString(x) && strings.Contains(x, "c")) {
				stdout = true
			}
			if x == "-d" || x == "--decompress" || longOpt(x, "--decompress") ||
				(reSymShort.MatchString(x) && strings.Contains(x, "d")) {
				decompress = true
			}
		}
		if decompress {
			if stdout {
				break
			}
			var pos []string
			for _, x := range args {
				if !strings.HasPrefix(x, "-") {
					pos = append(pos, x)
				}
			}
			for _, f := range pos {
				if err := hitc(f); err != nil {
					return err
				}
				if strings.HasSuffix(f, ".gz") {
					if err := hitc(f[:len(f)-3]); err != nil {
						return err
					}
				}
			}
			if len(pos) == 0 {
				break
			}
		}
	case name == "cpio":
		extract, listMode := false, false
		var dirs []string
		for j, x := range args {
			short := reSymShort.MatchString(x)
			if x == "--extract" || (short && strings.Contains(x, "i")) {
				extract = true
			}
			if x == "-t" || x == "--list" || x == "--to-stdout" || x == "--only-verify-crc" || (short && strings.Contains(x, "t")) {
				listMode = true
			}
			if (x == "-D" || x == "--directory") && j+1 < len(args) {
				dirs = append(dirs, args[j+1])
			} else if strings.HasPrefix(x, "--directory=") {
				dirs = append(dirs, x[len("--directory="):])
			}
		}
		if extract && !listMode {
			for _, d := range dirs {
				if err := hit(d, true, a.ccwd, a.ccwdKnown); err != nil {
					return err
				}
			}
			if len(dirs) == 0 {
				return cwdWrite(true, a.ccwd, a.ccwdKnown, true)
			}
		}
	case name == "cargo":
		sub := ""
		for k := 0; k < len(args); k++ {
			if !strings.HasPrefix(args[k], "-") {
				sub = args[k]
				break
			}
		}
		if sub == "build" || sub == "install" || sub == "run" || sub == "doc" || sub == "test" || sub == "bench" {
			for _, v := range optValues(args, "", "--target-dir") {
				if err := hit(v, true, a.ccwd, a.ccwdKnown); err != nil {
					return err
				}
			}
			for _, x := range args {
				opt, val, eq := strings.Cut(x, "=")
				if eq && longOpt(opt, "--target-dir") {
					if err := hit(val, true, a.ccwd, a.ccwdKnown); err != nil {
						return err
					}
				}
			}
		}
	case name == "tar" || name == "gtar" || name == "bsdtar":
		targs := append([]string{}, args...)
		old := len(targs) > 0 && reTarOld.MatchString(targs[0])
		if old {
			targs[0] = "-" + targs[0]
		}
		extract, writesArchive := false, false
		var top []string
		for i, x := range targs {
			modes := ""
			if strings.HasPrefix(x, "-") && !strings.HasPrefix(x, "--") {
				modes = tarModeLetters(x, old && i == 0)
			}
			if strings.Contains(modes, "x") {
				extract = true
			}
			opt, val, _ := strings.Cut(x, "=")
			if longOpt(opt, "--extract", "--get") {
				extract = true
			}
			if longOpt(opt, "--create", "--append", "--update", "--catenate", "--concatenate") {
				writesArchive = true
			}
			if strings.ContainsAny(modes, "cruA") {
				writesArchive = true
			}
			if longOpt(opt, "--one-top-level") {
				top = append(top, val)
			}
		}
		if extract {
			base, known, unres := a.ccwd, a.ccwdKnown, a.cwdUnres
			for _, d := range tarDirs(targs) {
				if err := hit(d, true, base, known); err != nil { // T-0983: an ancestor of a tree too
					return err
				}
				d = a.cfgExpand(d)
				if strings.HasPrefix(d, "/") {
					base, known, unres = d, true, false
				} else if known {
					base = pyJoin(base, d)
				}
			}
			for _, v := range top {
				if err := hit(v, true, base, known); err != nil {
					return err
				}
			}
			if !known || unres {
				return block(unknownDir)
			}
			return hit(base, true, base, true)
		}
		if writesArchive { // B3: create, append and update write the archive; list and diff only read it
			fargs := targs
			if old {
				fargs = targs[1:]
			}
			for _, v := range optValues(fargs, "f", "--file") {
				if err := hitc(v); err != nil {
					return err
				}
			}
			if old && strings.Contains(targs[0], "f") { // `tar cfb a.tar 20 x`: the value letters take the next words
				n := 0
				for _, ch := range targs[0] {
					if strings.ContainsRune(tarValueLetters, ch) {
						n++
					}
				}
				for _, v := range targs[1:min(1+n, len(targs))] {
					if !strings.HasPrefix(v, "-") {
						if err := hitc(v); err != nil {
							return err
						}
					}
				}
			}
		}
	case name == "unzip":
		dirs := unzipDirs(args)
		if !unzipListingOpts(rest) {
			for _, d := range dirs {
				if err := hit(d, true, a.ccwd, a.ccwdKnown); err != nil {
					return err
				}
			}
			if len(dirs) == 0 {
				return cwdWrite(true, a.ccwd, a.ccwdKnown, true)
			}
		}
	case name == "env":
		for _, v := range optValues(args, "C", "--chdir") {
			if err := hitc(v); err != nil {
				return err
			}
		}
		var gobin []string
		i := 0
		for i < len(args) {
			if IsAssign(args[i]) {
				if v, ok := strings.CutPrefix(args[i], "GOBIN="); ok {
					gobin = append(gobin, v)
				}
				i++
			} else if args[i] == "--" {
				i++
				break
			} else if strings.HasPrefix(args[i], "-") {
				i++
			} else {
				break
			}
		}
		sub := args[i:]
		if len(gobin) > 0 && len(sub) >= 2 && pyBasename(sub[0]) == "go" && sub[1] == "install" {
			for _, g := range gobin {
				if err := hitc(g); err != nil {
					return err
				}
			}
		}
	case name == "find":
		return a.protFind(args, func(w string) error { return hit(w, true, a.ccwd, a.ccwdKnown) },
			func() error { return cwdWrite(false, a.ccwd, a.ccwdKnown, true) })
	case name == "7z" || name == "7za" || name == "7zr":
		first := ""
		for _, x := range args {
			if !strings.HasPrefix(x, "-") {
				first = x
				break
			}
		}
		if first == "x" || first == "e" {
			var outs []string
			for _, x := range args {
				if strings.HasPrefix(x, "-o") && len(x) > 2 {
					outs = append(outs, x[2:])
				}
			}
			for _, v := range outs {
				if err := hit(v, true, a.ccwd, a.ccwdKnown); err != nil {
					return err
				}
			}
			if len(outs) == 0 {
				return cwdWrite(true, a.ccwd, a.ccwdKnown, true)
			}
		}
	case name == "git":
		base, known, k := a.ccwd, a.ccwdKnown, 0
		for k < len(args) && strings.HasPrefix(args[k], "-") {
			x := args[k]
			k++
			if x == "-C" && k < len(args) {
				args[k] = a.cfgExpand(args[k])
				switch {
				case strings.HasPrefix(args[k], "/"):
					base, known = args[k], true
				case !known:
					known = false
				default:
					base = pyJoin(base, args[k])
				}
				k++
			} else if in(protGitOpt, x) && !strings.Contains(x, "=") {
				k++
			}
		}
		if k < len(args) && args[k] == "init" { // G1: `git init <dir>` creates a repository (and its hooks) there
			valued := set("--template", "-b", "--initial-branch", "--object-format", "--ref-format", "--separate-git-dir")
			var pos []string
			j := k + 1
			for j < len(args) {
				x := args[j]
				j++
				switch {
				case in(valued, x):
					if x == "--separate-git-dir" && j < len(args) {
						if err := hit(args[j], false, base, known); err != nil {
							return err
						}
					}
					j++
				case strings.Contains(x, "=") && longOpt(strings.SplitN(x, "=", 2)[0], "--separate-git-dir"):
					if err := hit(x, false, base, known); err != nil {
						return err
					}
				case !strings.HasPrefix(x, "-"):
					pos = append(pos, x)
				}
			}
			for _, d := range pos {
				if err := hit(d, false, base, known); err != nil {
					return err
				}
			}
			if len(pos) == 0 {
				return cwdWrite(false, base, known)
			}
			return nil
		}
		if k+1 < len(args) && args[k] == "worktree" && (args[k+1] == "add" || args[k+1] == "move" || args[k+1] == "repair") {
			// G1: the new worktree's directory (and the existing ones `move`/`repair` name)
			j := k + 2
			for j < len(args) {
				x := args[j]
				j++
				if x == "-b" || x == "-B" || x == "--reason" {
					j++
				} else if !strings.HasPrefix(x, "-") {
					if err := hit(x, false, base, known); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if k < len(args) && args[k] == "clone" {
			var pos []string
			unknown := false
			j := k + 1
			for j < len(args) {
				x := args[j]
				j++
				switch {
				case in(protCloneV, x) || longOpt(x, "--separate-git-dir"):
					if longOpt(x, "--separate-git-dir") && j < len(args) {
						if err := hit(args[j], false, base, known); err != nil {
							return err
						}
					}
					j++
				case strings.Contains(x, "=") && longOpt(strings.SplitN(x, "=", 2)[0], "--separate-git-dir"):
					if err := hit(x, false, base, known); err != nil {
						return err
					}
				case !strings.HasPrefix(x, "-"):
					pos = append(pos, x)
				case strings.HasPrefix(x, "--") && !strings.Contains(x, "="):
					unknown = true // N1: a `--word` we do not know may take a value word
				}
			}
			if len(pos) > 1 {
				for _, d := range pos[1:] { // N1: pos[0] is the repository
					if err := hit(d, false, base, known); err != nil {
						return err
					}
				}
			}
			if len(pos) < 2 || unknown {
				return cwdWrite(false, base, known)
			}
			return nil
		}
	}
	return nil
}

func protInPlace(args []string) bool {
	for _, x := range args {
		if reInPlace.MatchString(x) || strings.HasPrefix(x, "--in-place") {
			return true
		}
	}
	return false
}

// tarDirs is tar_dirs: -C/--directory targets, also in a bundle; over-approximate.
func tarDirs(args []string) []string {
	var out []string
	k := 0
	for k < len(args) {
		a := args[k]
		k++
		if strings.HasPrefix(a, "--") {
			opt, val, eq := strings.Cut(a, "=")
			if longOpt(opt, "--directory") {
				if eq {
					out = append(out, val)
				} else if k < len(args) {
					out = append(out, args[k])
					k++
				}
			}
		} else if strings.HasPrefix(a, "-") && strings.Contains(a[1:], "C") {
			letters := a[1:]
			rem := letters[strings.Index(letters, "C")+1:]
			if rem != "" {
				out = append(out, rem)
			}
			n := 0
			for _, ch := range letters {
				if ch == 'C' || ch == 'f' {
					n++
				}
			}
			for _, w := range args[k:min(k+n, len(args))] {
				if !strings.HasPrefix(w, "-") {
					out = append(out, w)
				}
			}
		}
	}
	return out
}

// unzipDirs is unzip_dirs: -d targets, also bundled.
func unzipDirs(args []string) []string {
	var out []string
	for k, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a[1:], "d") {
			rem := a[strings.Index(a, "d")+1:]
			if rem != "" {
				out = append(out, rem)
			} else if k+1 < len(args) {
				out = append(out, args[k+1])
			}
		}
	}
	return out
}

// isFindReader: a bare read-only name or its /usr/bin or /bin path (N2: never judged by the basename).
func isFindReader(w string) bool {
	if findReaders[w] {
		return true
	}
	for _, d := range []string{"/usr/bin/", "/bin/"} {
		if strings.HasPrefix(w, d) && findReaders[w[len(d):]] {
			return true
		}
	}
	return false
}

var findReaders = map[string]bool{"ls": true, "cat": true, "grep": true, "wc": true, "stat": true, "file": true}

// protParentCopy is Analyzer.prot_parent_copy (T-0983 form B): cp/mv/ln/install/rsync into a directory that holds a
// protected tree is blocked when the source's basename is the next component toward one, or cannot be known.
func (a *Analyzer) protParentCopy(name string, args, tvals []string) error {
	var nonopt []int
	for j, x := range args {
		if !(strings.HasPrefix(x, "-") && len(x) > 1) {
			nonopt = append(nonopt, j)
		}
	}
	var destWords []string
	var srcIdx []int
	switch {
	case len(tvals) > 0:
		destWords = tvals
		for _, j := range nonopt {
			if !inStrs(tvals, args[j]) {
				srcIdx = append(srcIdx, j)
			}
		}
	case len(nonopt) >= 2:
		di := protDestIndexes(args) // an option value may trail the destination (`cp a dest -S .bak`)
		for j := range args {
			if di[j] {
				destWords = append(destWords, args[j])
			}
		}
		for _, j := range nonopt {
			if !di[j] {
				srcIdx = append(srcIdx, j)
			}
		}
	default:
		return nil
	}
	notarget := false
	for _, x := range args {
		if !strings.HasPrefix(x, "-") {
			continue
		}
		if longOpt(x, "--no-target-directory", "--parents", "--relative") ||
			(reSymShort.MatchString(x) && (strings.Contains(x, "T") || (name == "rsync" && strings.Contains(x, "R")))) {
			notarget = true
		}
	}
	for _, d := range destWords {
		if d == "" || strings.HasPrefix(d, "of=") {
			continue
		}
		t0 := a.cfgExpand(substVars(d, a.vars))
		for _, t := range braceExpand(t0) {
			if strings.ContainsAny(t, "$`") || (!strings.HasPrefix(t, "/") && !a.ccwdKnown) {
				continue // the unresolvable destination keeps main's rule (protHit above)
			}
			full := t
			if !strings.HasPrefix(t, "/") {
				full = pyJoin(a.ccwd, t)
			}
			nxt, anyName := protNextNames(full)
			if !anyName && len(nxt) == 0 {
				continue // not a strict ancestor of any protected tree
			}
			for _, j := range srcIdx {
				src := a.cfgExpand(substVars(args[j], a.vars))
				base := pyBasename(strings.TrimRight(src, "/"))
				unknown := notarget || anyName || base == "" || base == "." || base == ".." || strings.HasSuffix(src, "/.") ||
					strings.ContainsAny(src, "$`*?[{") || (name == "rsync" && (strings.HasSuffix(src, "/") || strings.Contains(src, "/./")))
				if unknown || in(nxt, base) {
					what := "not known"
					if !unknown {
						what = pyRepr(base)
					}
					return blockf("`" + name + "` on a directory that holds a protected tree, with the source " + pyRepr(args[j]) +
						" (it is, or can contain, one of them: the name of what it creates there is " + what + "): " + protWhy)
				}
			}
		}
	}
	return nil
}

// protNextNames is prot_next_names: the names an entry created directly inside p would need to have to be, or hold, a
// protected tree. anyName=true when that cannot be told (a `**` in p).
func protNextNames(p string) (map[string]struct{}, bool) {
	out := map[string]struct{}{}
	cands := []string{pyNormpath(p)}
	if !hasGlob(p) {
		cands = append(cands, pyRealpath(p))
	}
	for _, c := range uniq(cands) {
		var pat []string
		if c != "/" {
			pat = strings.Split(strings.Trim(c, "/"), "/")
		}
		if containsStr(pat, "**") {
			return nil, true
		}
		for _, d := range protTrees() {
			parts := strings.Split(strings.Trim(d, "/"), "/")
			if len(parts) <= len(pat) {
				continue
			}
			ok := true
			for i, y := range pat {
				if !fnmatchCase(parts[i], y) {
					ok = false
					break
				}
			}
			if ok {
				out[parts[len(pat)]] = struct{}{}
			}
		}
	}
	return out, false
}

// protFind is Analyzer.prot_find (T-0983 form C): a find that deletes, runs a writer or deleter or writes a file writes
// to every start path.
func (a *Analyzer) protFind(args []string, hit func(string) error, cwdHit func() error) error {
	i := 0
	for i < len(args) && (args[i] == "-H" || args[i] == "-L" || args[i] == "-P" || strings.HasPrefix(args[i], "-O") || strings.HasPrefix(args[i], "-D")) {
		if args[i] == "-D" {
			i += 2
		} else {
			i++
		}
	}
	var starts, files []string
	for i < len(args) && !(strings.HasPrefix(args[i], "-") || args[i] == "(" || args[i] == "!" || args[i] == ")" || args[i] == ",") {
		starts = append(starts, args[i])
		i++
	}
	expr := args[min(i, len(args)):]
	writes := false
	for k, x := range expr {
		switch {
		case x == "-delete":
			writes = true
		case (x == "-fprint" || x == "-fprint0" || x == "-fls" || x == "-fprintf") && k+1 < len(expr):
			writes = true
			files = append(files, expr[k+1])
		case (x == "-exec" || x == "-execdir" || x == "-ok" || x == "-okdir") && k+1 < len(expr):
			cmd := expr[k+1]
			// B1: against a protected tree any command counts as a writer unless it is on the closed read-only list;
			// N2: the whole word: a bare listed name or its /usr/bin or /bin path (./cat and /tmp/grep are not readers)
			if !isFindReader(cmd) {
				writes = true
			}
		}
	}
	if !writes {
		return nil
	}
	for _, w := range append(append([]string{}, starts...), files...) {
		if err := hit(w); err != nil {
			return err
		}
	}
	if len(starts) == 0 {
		return cwdHit()
	}
	return nil
}
