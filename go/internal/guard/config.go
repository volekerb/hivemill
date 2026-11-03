package guard

import (
	"regexp"
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

// exportGlob is a port of Analyzer.export_glob (T-0883): a glob word that can expand to the export file or its temp
// sibling.
func (a *Analyzer) exportGlob(t string) bool {
	if strings.ContainsAny(t, "$`") || !strings.ContainsAny(t, "*?[") {
		return false
	}
	if !strings.HasPrefix(t, "/") {
		if !a.ccwdKnown {
			return false
		}
		t = pyJoin(a.ccwd, t)
	}
	for _, pat := range braceExpand(pyNormpath(t)) {
		for _, d := range mustHost().runExport {
			if fnmatchCase(d, pat) || fnmatchCase(d+".tmp1", pat) {
				return true
			}
		}
	}
	return false
}

// cfgHit is a port of Analyzer.cfg_hit: word (or the value of a --opt=value / of=value word) names CFG_DIR or
// <live>/run/settings or something under them (or, with ancestor, a directory that contains one). A word with an
// expansion we cannot resolve counts when it spells `.config/factory` or `run/settings`.
func (a *Analyzer) cfgHit(word string, ancestor bool) bool {
	h := mustHost()
	for _, w := range uniq([]string{word, valueOf(word)}) {
		t := cfgExpand(substVars(w, a.vars))
		if strings.ContainsAny(t, "$`*?[{") {
			if strings.Contains(t, ".config/factory") || strings.Contains(t, "run/settings") || strings.Contains(t, "guard-export") {
				return true
			}
			if a.exportGlob(t) {
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
		for _, d := range h.runExport { // the file itself and its temp sibling <file>.tmp<pid>
			if p == d || strings.HasPrefix(p, d+".") || (ancestor && Under(d, p)) {
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
			"home), <live>/run/settings (the rendered role settings) or <live>/run/guard-export.json (the guard's own " +
			"decisions); only the owner and the tick change them")
	}
	return nil
}

// cfgPath is Analyzer.cfg_path: the realpath of a word for the export checks, "", false when unresolvable.
func (a *Analyzer) cfgPath(w string) (string, bool) {
	t := cfgExpand(substVars(w, a.vars))
	if t == "" || strings.ContainsAny(t, "$`*?[{") || (!strings.HasPrefix(t, "/") && !a.ccwdKnown) {
		return "", false
	}
	if !strings.HasPrefix(t, "/") {
		t = pyJoin(a.ccwd, t)
	}
	return pyRealpath(t), true
}

// exportVerbs is EXPORT_VERBS: the verbs rule R covers (T-0883).
var exportVerbs = set("cp", "mv", "ln", "install", "rsync", "rm", "rmdir", "unlink", "shred", "chmod", "chown",
	"truncate", "touch", "dd", "tar", "tee")

var runDirRe = regexp.MustCompile(`/run(?:/|$)`)

// exportAncestors is EXPORT_ANCESTORS: every directory above the export (run/, the live home, ~, its parents, /).
func exportAncestors() map[string]bool {
	out := map[string]bool{}
	for _, d := range mustHost().runExport {
		for p := pyDirname(d); !out[p]; p = pyDirname(p) {
			out[p] = true
		}
	}
	return out
}

// exportRule is Analyzer.export_rule (T-0883, rule R): ANY argument of an exportVerbs verb that names the export, its
// temp sibling or an ancestor directory (R1), is an option whose value does (R2), is a glob that can expand to one of
// them (R3), or is an unresolvable expansion spelling guard-export or a /run directory (R4). No argument position
// is guessed.
func (a *Analyzer) exportRule(name string, args []string) bool {
	if !in(exportVerbs, name) {
		return false
	}
	anc := exportAncestors()
	for _, x := range args {
		words := []string{x, valueOf(x)}
		if m := shortOptValue.FindStringSubmatch(x); m != nil {
			words = append(words, m[1])
		}
		for _, w := range uniq(words) {
			for _, e := range braceExpand(cfgExpand(substVars(w, a.vars))) {
				if strings.ContainsAny(e, "$`") {
					if strings.Contains(e, "guard-export") || runDirRe.MatchString(e) {
						return true
					}
					continue
				}
				if e == "" || (!strings.HasPrefix(e, "/") && !a.ccwdKnown) {
					continue
				}
				full := e
				if !strings.HasPrefix(e, "/") {
					full = pyJoin(a.ccwd, e)
				}
				if strings.ContainsAny(e, "*?[") {
					pat := pyNormpath(full)
					for _, d := range mustHost().runExport {
						if fnmatchCase(d, pat) || fnmatchCase(d+".tmp1", pat) {
							return true
						}
					}
					for d := range anc {
						if fnmatchCase(d, pat) || fnmatchCase(d+".tmp1", pat) {
							return true
						}
					}
					continue
				}
				p := pyRealpath(full)
				if anc[p] {
					return true
				}
				for _, d := range mustHost().runExport {
					if p == d || strings.HasPrefix(p, d+".") {
						return true
					}
				}
			}
		}
	}
	return false
}

// shortOptValue is re.match(r"^-[A-Za-z]*?[tC](.+)$"): the value glued to a short -t / -C option or cluster.
var shortOptValue = regexp.MustCompile(`^-[A-Za-z]*?[tC](.+)$`)

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
	if a.exportRule(name, dropDashDash(rest)) {
		return blockf("`" + name + "` on <live>/run/guard-export.json: it holds the guard's own decisions; " +
			"only the tick writes it")
	}
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
				if strings.Contains(x, "guard-export") {
					return blockf("`" + name + "` on <live>/run/guard-export.json: it holds the guard's own decisions; only the tick writes it")
				}
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

// dropDashDash is [a for a in rest if a != "--"].
func dropDashDash(rest []string) []string {
	var out []string
	for _, x := range rest {
		if x != "--" {
			out = append(out, x)
		}
	}
	return out
}
