package guard

import (
	"strings"
)

// The ~/.config/factory and <live>/run/settings write detector (T-0212): cfg_cd, cfg_expand, cfg_code, cfg_hit,
// cfg_redirect, cfg_write of worker-guard.py. The detector's working directory is Analyzer.ccwd; ccwdKnown is
// false where Python holds None (a `cd` target known only at run time).

const cfgWhy = "it holds live_home, which decides what counts as the live home; only the owner changes it with " +
	"`factory init --live-home`"

// cfgExpand is a port of Analyzer.cfg_expand: a leading ~, $HOME or ${HOME} is the passwd home (never $HOME).
func cfgExpand(w string) string {
	for _, p := range []string{"~", "$HOME", "${HOME}"} {
		rest, ok := strings.CutPrefix(w, p)
		if ok && (rest == "" || rest[0] == '/' || rest == "\n") {
			return mustHost().pwDir + rest
		}
	}
	return w
}

// cfgCd is a port of Analyzer.cfg_cd: track the directory for the CFG_DIR write detector.
func (a *Analyzer) cfgCd(name string, rest []string) {
	var args []string
	for _, x := range rest {
		if !strings.HasPrefix(x, "-") || x == "-" {
			args = append(args, x)
		}
	}
	switch {
	case name == "popd" || (len(args) > 0 && (args[0] == "-" || dynamicDir(args[0]))):
		a.ccwdKnown = false
	case len(args) == 0:
		a.ccwd, a.ccwdKnown = mustHost().pwDir, true
	default:
		p := cfgExpand(args[0])
		if strings.HasPrefix(p, "/") || !a.ccwdKnown {
			a.ccwd = p
		} else {
			a.ccwd = pyJoin(a.ccwd, p)
		}
		a.ccwdKnown = true
	}
}

// valueOf is word.split("=", 1)[-1]: the part after the first "=", or the whole word without one.
func valueOf(word string) string {
	if k := strings.IndexByte(word, '='); k >= 0 {
		return word[k+1:]
	}
	return word
}

// cfgHit is a port of Analyzer.cfg_hit: word (or the value of a --opt=value / of=value word) names CFG_DIR or
// <live>/run/settings or something under them (or, with ancestor, a directory that contains one). A word with an
// expansion we cannot resolve counts when it spells `.config/factory` or `run/settings`.
func (a *Analyzer) cfgHit(word string, ancestor bool) bool {
	h := mustHost()
	for _, w := range uniq([]string{word, valueOf(word)}) {
		t := cfgExpand(w)
		if strings.ContainsAny(t, "$`*?[{") {
			if strings.Contains(t, ".config/factory") || strings.Contains(t, "run/settings") {
				return true
			}
			continue
		}
		if t == "" || (!strings.HasPrefix(t, "/") && !a.ccwdKnown) {
			continue
		}
		full := t
		if !strings.HasPrefix(t, "/") {
			full = pyJoin(a.ccwd, t)
		}
		p := pyRealpath(full)
		for _, d := range append([]string{h.cfgDir}, h.runSettings...) {
			if p == d || Under(p, d) || (ancestor && Under(d, p)) {
				return true
			}
		}
	}
	return false
}

// cfgRedirect is a port of Analyzer.cfg_redirect.
func (a *Analyzer) cfgRedirect(target string) error {
	if a.cfgHit(target, false) {
		return blockf("a redirect into ~/.config/factory (it holds live_home, which decides what counts as the live " +
			"home) or <live>/run/settings (the rendered role settings); only the owner changes them")
	}
	return nil
}

// anyInPlace is the in-place test of cfg_write / persist_write:
// re.match(r"^-[A-Za-z]*i", a) or a.startswith("--in-place") or a == "inplace".
func anyInPlace(rest []string) bool { return anyOf(rest, isInPlaceFlag) }

// editorWriter is `name in EDITORS - {"less", "more", "man", "view"}`.
func editorWriter(name string) bool {
	switch name {
	case "less", "more", "man", "view":
		return false
	}
	return in(Editors, name)
}

// isDestArg is the destination test shared by cfg_write and persist_write for cp/install/rsync/dd:
// j == len(args)-1 or a.startswith("of=") or args[j-1] in ("-t", "--target-directory") or
// a.startswith("--target-directory=").
func isDestArg(args []string, j int) bool {
	a := args[j]
	return j == len(args)-1 || strings.HasPrefix(a, "of=") ||
		(j > 0 && (args[j-1] == "-t" || args[j-1] == "--target-directory")) || strings.HasPrefix(a, "--target-directory=")
}

// cfgWrite is a port of Analyzer.cfg_write (T-0212) after its `factory init --live-home` block (factoryInitLiveHome):
// refuse Bash writes to <pw_dir>/.config/factory, the directory and live_home. Reads (cat, grep) stay allowed. NOT a
// boundary: the file can only ADD a live home and is ignored unless owned by the user and not group/other-writable.
func (a *Analyzer) cfgWrite(name string, rest []string) error {
	switch {
	case in(CfgWriters, name):
		var args []string
		for _, x := range rest {
			if x != "--" {
				args = append(args, x)
			}
		}
		destOnly := name == "cp" || name == "install" || name == "rsync" || name == "dd"
		for j, x := range args {
			if destOnly && !isDestArg(args, j) {
				continue
			}
			anc := in(CfgRemovers, name) || (name == "mv" && j < len(args)-1)
			if a.cfgHit(x, anc) {
				return blockf("`" + name + "` on ~/.config/factory: " + cfgWhy)
			}
		}
	case (name == "sed" || name == "perl" || name == "gawk" || in(Awks, name)) && (in(Awks, name) || anyInPlace(rest)):
		for _, x := range rest {
			if a.cfgHit(x, false) || CfgCode(x) {
				return blockf("`" + name + "` writing ~/.config/factory: " + cfgWhy)
			}
		}
	case interpreterFull(name) || editorWriter(name):
		for _, x := range rest {
			if a.cfgHit(x, false) || CfgCode(x) {
				return blockf("`" + name + "` naming ~/.config/factory (code there cannot be inspected; read it with cat): " + cfgWhy)
			}
		}
	}
	return nil
}
