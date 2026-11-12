package guard

import (
	"os/user"
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

var reTildeName = regexp.MustCompile("^~([^/$`\\\\'\"]*)")

// tildeExpand is a port of tilde_expand (T-0983): cfgExpand, plus `~name` (that user's passwd home) and `~+` (cwd, when
// cwdKnown). A word that cannot be resolved (no such user, `~-`, a `~+` with an unknown cwd) is returned unchanged, as
// on main, so every caller joins it to the cwd as before; tildeUnres says which words those are.
func tildeExpand(w, cwd string, cwdKnown bool) string {
	w = cfgExpand(w)
	m := reTildeName.FindStringSubmatch(w)
	if m == nil || m[1] == "" {
		return w
	}
	name, rest := m[1], w[len(m[0]):]
	if !(rest == "" || rest[0] == '/' || rest == "\n") {
		return w
	}
	if name == "+" && cwdKnown {
		return cwd + rest
	}
	if name != "+" && name != "-" {
		if u, err := user.Lookup(name); err == nil && u.HomeDir != "" {
			return u.HomeDir + rest
		}
	}
	return w
}

// tildeUnres is a port of tilde_unres (T-0983): w starts with a `~` form tildeExpand cannot resolve. Only rule A's extra
// tightening asks (protHit, cfgHit, the cd tracker, the Edit path).
func tildeUnres(w string, cwdKnown bool) bool {
	w = cfgExpand(w)
	m := reTildeName.FindStringSubmatch(w)
	if m == nil || m[1] == "" {
		return false
	}
	name, rest := m[1], w[len(m[0]):]
	if !(rest == "" || rest[0] == '/' || rest == "\n") {
		return false
	}
	switch name {
	case "+":
		return !cwdKnown
	case "-":
		return true
	}
	u, err := user.Lookup(name)
	return err != nil || u.HomeDir == ""
}

// cfgExpand (method) is Analyzer.cfg_expand: tildeExpand against the tracked cwd.
func (a *Analyzer) cfgExpand(w string) string { return tildeExpand(w, a.ccwd, a.ccwdKnown) }

var reCdHomeFactory = regexp.MustCompile(`^\$(?:\{HOME\}|HOME)(/.*)?$`)

// cdHomeFactoryWord is cd_home_factory_word: `$HOME/factory` and `${HOME}/factory` for ccwd tracking.
func cdHomeFactoryWord(w string) string {
	factory := pyJoin(mustHost().pwDir, "factory")
	if w == "$HOME/factory" || w == "${HOME}/factory" {
		return factory
	}
	m := reCdHomeFactory.FindStringSubmatch(w)
	if m == nil || m[1] == "" {
		return ""
	}
	tail := pyNormpath(pyJoin("/", strings.TrimPrefix(m[1], "/")))
	if tail == "/factory" {
		return factory
	}
	return ""
}

// cfgCd is a port of Analyzer.cfg_cd: track the directory for the CFG_DIR write detector.
func (a *Analyzer) cfgCd(name string, rest []string) {
	var args []string
	for _, x := range rest {
		if !strings.HasPrefix(x, "-") || x == "-" {
			args = append(args, x)
		}
	}
	a.cwdUnres = false
	switch {
	case name == "popd":
		a.ccwdKnown = false
	case len(args) == 0:
		a.ccwd, a.ccwdKnown = mustHost().pwDir, true
	case args[0] == "-":
		a.ccwdKnown = false
	case cdHomeFactoryWord(args[0]) != "":
		a.ccwd, a.ccwdKnown = cdHomeFactoryWord(args[0]), true
	case dynamicDir(args[0]):
		a.ccwdKnown = false
	default:
		p := a.cfgExpand(args[0])
		if tildeUnres(args[0], a.ccwdKnown) { // `cd ~nosuchuser`, `cd ~-`: as on main, a path relative to the known cwd
			a.cwdUnres = true // the extractor / downloader rules still refuse a cwd that is not really known
		}
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
		w = substVars(w, a.vars)
		t := a.cfgExpand(w)
		if tildeUnres(w, a.ccwdKnown) && (strings.Contains(w, ".config/factory") || strings.Contains(w, "run/settings") ||
			strings.Contains(w, "guard-export") || strings.Contains(w, ".local/lib/factory") ||
			strings.Contains(w, "run/guard_impl") || strings.Contains(w, "run/cli_impl") || hasLocalBin(w)) {
			return true // rule A: an unresolvable `~name/...` that spells a protected name counts
		}
		if strings.ContainsAny(t, "$`*?[{") {
			if strings.Contains(t, ".config/factory") || strings.Contains(t, "run/settings") || strings.Contains(t, "guard-export") ||
				strings.Contains(t, ".local/lib/factory") || strings.Contains(t, "run/guard_impl") || strings.Contains(t, "run/cli_impl") || hasLocalBin(t) {
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
		dirs := append([]string{h.cfgDir, h.libDir}, h.runSettings...)
		for _, d := range append(dirs, h.runGuardImpl...) {
			if p == d || Under(p, d) || (ancestor && Under(d, p)) {
				return true
			}
		}
		for _, q := range []string{p, pyNormpath(full)} { // T-0957: the spelled path too (a symlink in ~/.local/bin)
			for _, d := range h.binDirs {
				if q == d || Under(q, d) || (ancestor && Under(d, q)) {
					return true
				}
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

// hasLocalBin is the T-0957 regex \.local/bin(?![A-Za-z0-9_]).
func hasLocalBin(s string) bool {
	const k = ".local/bin"
	for i := 0; i+len(k) <= len(s); i++ {
		if hasAt(s, i, k) {
			n := nextRune(s, i+len(k))
			if !(n >= 0 && asciiWord(n)) {
				return true
			}
		}
	}
	return false
}

// inBinDir is Python's in_bin_dir (block message only).
func (a *Analyzer) inBinDir(w string) bool {
	t := a.cfgExpand(substVars(w, a.vars))
	if t == "" || strings.ContainsAny(t, "$`*?[{") || (!strings.HasPrefix(t, "/") && !a.ccwdKnown) {
		return hasLocalBin(t)
	}
	full := t
	if !strings.HasPrefix(t, "/") {
		full = pyJoin(a.ccwd, t)
	}
	for _, q := range []string{pyRealpath(full), pyNormpath(full)} {
		for _, d := range h0().binDirs {
			if q == d || Under(q, d) {
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
			"decisions), <live>/run/guard_impl or ~/.local/lib/factory (the guard implementation flag and binary) or " +
			"~/.local/bin (first on the tick's PATH); only the owner and the tick change them")
	}
	return nil
}

// cfgPath is Analyzer.cfg_path: the realpath of a word for the export checks, "", false when unresolvable.
func (a *Analyzer) cfgPath(w string) (string, bool) {
	t := a.cfgExpand(substVars(w, a.vars))
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
			for _, e := range braceExpand(a.cfgExpand(substVars(w, a.vars))) {
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
				if strings.Contains(x, "guard_impl") || strings.Contains(x, "cli_impl") || strings.Contains(x, ".local/lib/factory") || a.inGuardImpl(x) {
					return blockf("`" + name + "` on <live>/run/guard_impl or ~/.local/lib/factory: they pick and hold the " +
						"guard implementation; only the owner and `factory` change them")
				}
				if a.inBinDir(x) {
					return blockf("`" + name + "` on ~/.local/bin: it is first on the tick's PATH, so a file there runs with " +
						"the tick's credentials; only the owner changes it")
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

func h0() *hostInfo { return mustHost() }

// inGuardImpl is Python's in_guard_impl (block message only): x resolves to run/guard_impl, to libDir or to a file under libDir.
func (a *Analyzer) inGuardImpl(x string) bool {
	p, ok := a.cfgPath(x)
	if !ok {
		return false
	}
	if Under(p, h0().libDir) {
		return true
	}
	for _, d := range h0().runGuardImpl {
		if p == d {
			return true
		}
	}
	return false
}
