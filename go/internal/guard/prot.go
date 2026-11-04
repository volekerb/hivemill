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
	"run/settings", "run/guard_impl"}

const protWhy = "it is a protected tree for workers (~/.local/go and ~/.config/go: the Go toolchain and `go env -w`; " +
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
	for _, home := range homes {
		base = append(base, pyJoin(pyJoin(home, "run"), "settings"), pyJoin(pyJoin(home, "run"), "guard_impl"))
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
	for _, d := range protTrees() {
		parts := strings.Split(strings.Trim(d, "/"), "/")
		n := len(pat)
		if len(parts) < n {
			n = len(parts)
		}
		if !(len(pat) >= len(parts) || ancestor) {
			continue
		}
		ok := true
		for i := 0; i < n; i++ {
			if !fnmatchCase(parts[i], pat[i]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// protEditPath is prot_edit_path (persist_edit side, gap g).
func protEditPath(p string) bool {
	if strings.ContainsAny(p, "$`") {
		for _, m := range protMarkers {
			if strings.Contains(p, m) {
				return true
			}
		}
		return false
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
		if inStrs(longs, opt) {
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
		t := cfgExpand(substVars(w, a.vars))
		for _, e := range braceExpand(t) {
			if strings.ContainsAny(e, "$`") {
				for _, m := range protMarkers {
					if strings.Contains(e, m) {
						return true
					}
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
	protTarLong = []string{"--extract", "--get", "--ex", "--ext", "--extr", "--extra", "--extrac", "--ge"}
	protCloneV  = set("-b", "--branch", "--depth", "-o", "--origin", "--template", "--reference", "--reference-if-able",
		"-c", "--config", "-j", "--jobs", "--filter", "--separate-git-dir", "-u", "--upload-pack",
		"--server-option", "--shallow-since", "--shallow-exclude", "--bundle-uri")
	protGitOpt = set("-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--super-prefix")
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
func (a *Analyzer) protWrite(name string, rest []string) error {
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
	cwdWrite := func(strict bool, base string, known bool) error {
		if !known {
			if strict {
				return block(unknownDir)
			}
		} else if protPath(base, false) {
			return block("the working directory, which is in a protected tree")
		}
		return nil
	}
	switch {
	case in(CfgWriters, name):
		destOnly := name == "cp" || name == "install" || name == "rsync" || name == "dd"
		if !destOnly && name == "ln" {
			for _, x := range args {
				if x == "--symbolic" || (reSymShort.MatchString(x) && strings.Contains(x, "s")) {
					destOnly = true
				}
			}
		}
		for j, x := range args {
			if destOnly && !isDestArg(args, j) {
				continue
			}
			if err := hit(x, in(CfgRemovers, name) || (name == "mv" && j < len(args)-1), a.ccwd, a.ccwdKnown); err != nil {
				return err
			}
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
			cdir = cfgExpand(cdir)
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
		}
	case in(protCCNames, name):
		for k, x := range args {
			switch {
			case (x == "-o" || x == "--output") && k+1 < len(args):
				if err := hitc(args[k+1]); err != nil {
					return err
				}
			case strings.HasPrefix(x, "--output="):
				if err := hitc(x); err != nil {
					return err
				}
			case strings.HasPrefix(x, "-o") && len(x) > 2:
				if err := hitc(x[2:]); err != nil {
					return err
				}
			}
		}
	case name == "tar" || name == "gtar" || name == "bsdtar":
		targs := append([]string{}, args...)
		if len(targs) > 0 && reTarOld.MatchString(targs[0]) {
			targs[0] = "-" + targs[0]
		}
		extract := false
		var top []string
		for _, x := range targs {
			if strings.HasPrefix(x, "-") && !strings.HasPrefix(x, "--") && strings.Contains(x, "x") {
				extract = true
			}
			opt, val, _ := strings.Cut(x, "=")
			if inStrs(protTarLong, opt) {
				extract = true
			}
			if strings.HasPrefix(x, "--one-top-level=") {
				top = append(top, val)
			}
		}
		if extract {
			base, known := a.ccwd, a.ccwdKnown
			for _, d := range optValues(targs, "C", "--directory") {
				if err := hit(d, false, base, known); err != nil {
					return err
				}
				d = cfgExpand(d)
				if strings.HasPrefix(d, "/") {
					base, known = d, true
				} else if known {
					base = pyJoin(base, d)
				}
			}
			for _, v := range top {
				if err := hit(v, false, base, known); err != nil {
					return err
				}
			}
			if !known {
				return block(unknownDir)
			}
			return hit(base, false, base, true)
		}
		for _, v := range optValues(targs, "f", "--file") {
			if err := hitc(v); err != nil {
				return err
			}
		}
	case name == "unzip":
		var dirs []string
		for k, x := range args {
			if x == "-d" && k+1 < len(args) {
				dirs = append(dirs, args[k+1])
			} else if strings.HasPrefix(x, "-d") && len(x) > 2 {
				dirs = append(dirs, x[2:])
			}
		}
		if !unzipListingOpts(rest) {
			for _, d := range dirs {
				if err := hitc(d); err != nil {
					return err
				}
			}
			if len(dirs) == 0 {
				return cwdWrite(true, a.ccwd, a.ccwdKnown)
			}
		}
	case name == "7z" || name == "7za" || name == "7zr":
		if len(args) > 0 && (args[0] == "x" || args[0] == "e") {
			var outs []string
			for _, x := range args {
				if strings.HasPrefix(x, "-o") && len(x) > 2 {
					outs = append(outs, x[2:])
				}
			}
			for _, v := range outs {
				if err := hitc(v); err != nil {
					return err
				}
			}
			if len(outs) == 0 {
				return cwdWrite(true, a.ccwd, a.ccwdKnown)
			}
		}
	case name == "git":
		base, known, k := a.ccwd, a.ccwdKnown, 0
		for k < len(args) && strings.HasPrefix(args[k], "-") {
			x := args[k]
			k++
			if x == "-C" && k < len(args) {
				args[k] = cfgExpand(args[k])
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
		if k < len(args) && args[k] == "clone" {
			var pos []string
			j := k + 1
			for j < len(args) {
				x := args[j]
				j++
				switch {
				case in(protCloneV, x):
					if x == "--separate-git-dir" && j < len(args) {
						if err := hit(args[j], false, base, known); err != nil {
							return err
						}
					}
					j++
				case strings.HasPrefix(x, "--separate-git-dir="):
					if err := hit(x, false, base, known); err != nil {
						return err
					}
				case !strings.HasPrefix(x, "-"):
					pos = append(pos, x)
				}
			}
			if len(pos) >= 2 {
				return hit(pos[1], false, base, known)
			}
			return cwdWrite(false, base, known)
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
