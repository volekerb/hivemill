package guard

import (
	"os"
	"regexp"
	"strings"
)

// T-0783 (worker-guard.py): NO_PUSH_ROLES may not reach git config through a `.git` gitfile or through archive
// extraction into (or whose target cannot be bounded to) a .git-free scratchpad dir.

// fsMutators is worker-guard.py:FS_MUTATORS.
var fsMutators = set("ln", "mv", "cp", "install", "chmod", "chown", "rsync", "dd", "tee", "touch", "truncate", "sed", "perl", "patch")

// archiveWalkMax is ARCHIVE_WALK_MAX: entries hasGitBelow walks below an extraction dir before refusing.
var archiveWalkMax = 20000

// gitfilePath is a port of gitfile_path: p (absolute) is itself named `.git`.
func gitfilePath(p string) bool {
	for _, c := range uniq([]string{pyNormpath(p), pyRealpath(p)}) {
		if pyBasename(strings.TrimRight(c, "/")) == ".git" {
			return true
		}
	}
	return false
}

// hasGitBelow is a port of Analyzer.has_git_below: top or anything below it is named .git or is a symlink (never
// followed), the walk fails (unreadable dir, top missing or not a dir) or exceeds archiveWalkMax entries.
func hasGitBelow(top string) bool {
	seen := 0
	var walk func(dir string) bool
	walk = func(dir string) bool {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		seen += len(ents)
		for _, e := range ents {
			if e.Name() == ".git" || e.Type()&os.ModeSymlink != 0 || seen > archiveWalkMax {
				return true
			}
		}
		for _, e := range ents {
			if e.IsDir() && walk(pyJoin(dir, e.Name())) {
				return true
			}
		}
		return false
	}
	return walk(top)
}

// alphaCluster is re.match(r"^-?[A-Za-z]+$" or r"^-[A-Za-z]+$", s): `$` also matches before one trailing newline.
func alphaCluster(s string, dashOptional bool) bool {
	s = strings.TrimSuffix(s, "\n")
	if strings.HasPrefix(s, "-") {
		s = s[1:]
	} else if !dashOptional {
		return false
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// isPythonName is re.match(r"^python[\d.]*$", s).
func isPythonName(s string) bool {
	s = strings.TrimSuffix(s, "\n")
	rest, ok := strings.CutPrefix(s, "python")
	if !ok {
		return false
	}
	for _, r := range rest {
		if r != '.' && !isDecimal(r) {
			return false
		}
	}
	return true
}

func lastOf(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}
	return xs[len(xs)-1:]
}

// archiveTarget is a port of Analyzer.archive_target.
func (a *Analyzer) archiveTarget(name, d string) error {
	t := cfgExpand(substVars(d, a.vars))
	if strings.ContainsAny(t, "$`*?[{") || (!strings.HasPrefix(t, "/") && !a.ccwdKnown) {
		return blockf("`" + name + "` into " + pyRepr(d) + ": the target cannot be resolved, so it may be a .git dir; " +
			"give a literal directory in your scratchpad")
	}
	if !strings.HasPrefix(t, "/") {
		t = pyJoin(a.ccwd, t)
	}
	t = pyRealpath(t)
	if !inScratchpad(t) {
		return blockf("`" + name + "` into " + pyRepr(d) + ": extraction may only target a directory inside your scratchpad (an archive " +
			"member .git/config or hook elsewhere runs as git): " + persistWhy)
	}
	if a.fsChanged {
		return blockf("`" + name + "` into " + pyRepr(d) + ": an earlier command on this line may have planted a symlink or .git under the " +
			"target (ln, mv, cp, install, chmod, a write or redirect); extract in its own command, first")
	}
	if containsStr(strings.Split(t, "/"), ".git") || hasGitBelow(t) {
		return blockf("`" + name + "` into " + pyRepr(d) + ": a .git dir (or a dir holding one at any depth) would receive config/hooks " +
			"that run as git: " + persistWhy)
	}
	return nil
}

// archiveExtract is a port of Analyzer.archive_extract: it reports whether the command extracts.
func (a *Analyzer) archiveExtract(name string, rest []string) (bool, error) {
	var args []string
	for _, x := range rest {
		if x != "--" {
			args = append(args, x)
		}
	}
	var dirs []string
	extract := false
	const why = "name the target (tar -C, unzip -d, cpio -D) as a scratchpad dir that has no .git"
	isTar := name == "tar" || name == "gtar" || name == "bsdtar"
	switch {
	case isTar:
		for j, x := range args {
			short := alphaCluster(x, true) && (j == 0 || strings.HasPrefix(x, "-")) && !strings.HasPrefix(x, "--")
			if x == "--extract" || x == "--get" || (short && strings.Contains(x, "x")) {
				extract = true
			}
			switch {
			case (x == "-C" || x == "--directory") && j+1 < len(args):
				dirs = append(dirs, args[j+1])
			case strings.HasPrefix(x, "--directory="):
				dirs = append(dirs, x[len("--directory="):])
			case short && strings.Contains(x, "C"):
				tail := x[strings.Index(x, "C")+1:]
				if tail != "" {
					dirs = append(dirs, tail)
				} else {
					for _, y := range args[j+1:] {
						if !strings.HasPrefix(y, "-") {
							dirs = append(dirs, y)
						}
					}
				}
			case strings.HasPrefix(x, "-C") && len(x) > 2:
				dirs = append(dirs, x[2:])
			}
		}
	case name == "unzip":
		extract = !anyOf(args, func(x string) bool { return in(unzipNoExtract, x) })
		for j, x := range args {
			if x == "-d" && j+1 < len(args) {
				dirs = append(dirs, args[j+1])
			} else if strings.HasPrefix(x, "-d") && len(x) > 2 {
				dirs = append(dirs, x[2:])
			}
		}
	case name == "cpio":
		var pos []string
		for _, x := range args {
			if !strings.HasPrefix(x, "-") {
				pos = append(pos, x)
			}
		}
		for j, x := range args {
			short := alphaCluster(x, false)
			if x == "--extract" || (short && strings.Contains(x, "i") && !strings.Contains(x, "t")) {
				extract = true
			}
			if x == "--pass-through" || (short && strings.Contains(x, "p")) {
				extract = true
				dirs = append(dirs, lastOf(pos)...)
			}
			if (x == "-D" || x == "--directory") && j+1 < len(args) {
				dirs = append(dirs, args[j+1])
			} else if strings.HasPrefix(x, "--directory=") {
				dirs = append(dirs, x[len("--directory="):])
			}
		}
	case name == "rsync":
		var pos []string
		for _, x := range args {
			if !strings.HasPrefix(x, "-") {
				pos = append(pos, x)
			}
		}
		if len(pos) >= 2 {
			extract, dirs = true, lastOf(pos)
		}
	case name == "7z" || name == "7za" || name == "7zr":
		extract = len(args) > 0 && (args[0] == "x" || args[0] == "e")
		for _, x := range args {
			if strings.HasPrefix(x, "-o") && len(x) > 2 {
				dirs = append(dirs, x[2:])
			}
		}
	case isPythonName(name):
		mod, modSet, code, codeSet, k := "", false, "", false, 0
	pyopts:
		for k < len(args) && strings.HasPrefix(args[k], "-") && len(args[k]) > 1 {
			cl := []rune(args[k])
			k++
			if strings.HasPrefix(args[k-1], "--") { // T-0808 F2: skip a known long option (and its value); an unknown one fails closed
				base, _, hasEq := strings.Cut(args[k-1], "=")
				switch {
				case in(pyLongValue, base):
					if !hasEq {
						k++ // `--opt value` takes the next argument; `--opt=value` carries its own
					}
				case !in(pyLongFlag, base):
					// unknown arity: block only when a later argument names an archive module (`--bogus -m pytest` stays allowed)
					if anyOf(args[min(k, len(args)):], func(x string) bool { return strings.HasSuffix(x, "zipfile") || strings.HasSuffix(x, "tarfile") }) {
						return false, blockf("`" + name + "` with the unknown option " + pyRepr(args[k-1]) + " before -m zipfile/tarfile: the target " +
							"cannot be bounded; drop the option or extract with unzip -d")
					}
					break pyopts
				}
				continue
			}
			// a short-flag cluster: -I, -Bm, -Im zipfile, -mzipfile. T-0808 F1: walk it char by char; only a bare c/m ends the
			// loop, and -W/-X/-Q (value-taking) consume the rest of the cluster or the next argument as their value
		cluster:
			for i := 1; i < len(cl); i++ {
				tail := string(cl[i+1:])
				switch cl[i] {
				case 'm':
					if tail != "" {
						mod, modSet = tail, true
					} else {
						if k < len(args) {
							mod, modSet = args[k], true
						} else {
							mod, modSet = "", false
						}
						k++
					}
					break cluster
				case 'c':
					code, codeSet = tail, true
					if tail == "" && k < len(args) {
						code = args[k]
					}
					break cluster
				case 'W', 'X', 'Q': // takes a value
					if tail == "" {
						k++
					}
					break cluster
				}
			}
			if modSet || codeSet {
				break
			}
		}
		if codeSet && pyExtractCode.MatchString(code) {
			return false, blockf("`" + name + " -c` code that extracts an archive cannot be bounded (it may write .git/config or hooks): " +
				why + "; use unzip -d or tar -C")
		}
		if modSet && (mod == "zipfile" || mod == "tarfile") {
			for j := k; j < len(args); j++ {
				if args[j] == "-e" || args[j] == "--extract" {
					extract = true
					if j+2 < len(args) {
						dirs = append(dirs, args[j+2])
					}
				}
			}
		}
	case name == "ar":
		var ops, outs []string
		k, opseen := 0, false
		for _, x := range args {
			if strings.HasPrefix(x, "@") { // T-0808 F3: a response file adds options the hook cannot see, even as an option value
				return false, blockf("`ar @file`: the response file's options cannot be inspected; pass the options on the command line")
			}
		}
		for k < len(args) {
			x := args[k]
			k++
			switch {
			case x == "--plugin" || x == "--target" || x == "--output":
				if x == "--output" && k < len(args) {
					outs = append(outs, args[k])
				}
				k++
			case strings.HasPrefix(x, "--output="):
				outs = append(outs, x[len("--output="):])
			case x == "--extract":
				ops = append(ops, "x")
			case strings.HasPrefix(x, "--"):
			case !opseen: // -x, -xv, -X32_64: any cluster naming x counts; the first non-option argument is the operation
				ops = append(ops, x)
				opseen = !strings.HasPrefix(x, "-")
			}
		}
		if anyOf(ops, func(o string) bool { return strings.Contains(o, "x") }) {
			extract = true
			dirs = append(dirs, outs...)
			if len(dirs) == 0 && a.ccwdKnown && a.ccwd != "" { // members land in the current directory
				dirs = []string{a.ccwd}
			}
		}
	case name == "pax":
		extract = anyOf(args, func(x string) bool { return x == "-r" || (alphaCluster(x, false) && strings.Contains(x, "r")) })
	case name == "jar":
		extract = len(args) > 0 && strings.Contains(args[0], "x")
		if extract {
			return false, blockf("`jar` extraction writes into the current directory under names the hook cannot see: " +
				strings.ReplaceAll(why, "(tar -C, unzip -d, cpio -D) ", "") + "; use unzip -d")
		}
	}
	if isTar && extract && anyOf(args, func(x string) bool {
		return in(tarRewrite, x) || strings.HasPrefix(x, "--transform=") || strings.HasPrefix(x, "--xform=") ||
			strings.HasPrefix(x, "--to-command=") || (alphaCluster(x, true) && strings.Contains(x, "P") && !strings.HasPrefix(x, "--"))
	}) {
		return false, blockf("`" + name + "` option that rewrites member names (-P, --transform, --to-command) can escape the -C target " +
			"into a .git dir: " + why)
	}
	if !extract {
		return false, nil
	}
	if len(dirs) == 0 {
		return false, blockf("`" + name + "` extracting without a target directory cannot be bounded (an archive may carry .git/config " +
			"or hooks): " + why)
	}
	for _, d := range dirs {
		if err := a.archiveTarget(name, d); err != nil {
			return false, err
		}
	}
	return true, nil
}

var (
	// T-0808 F2: python long options that may precede -m (value-taking ones named separately); any other fails closed
	pyLongFlag  = set("--version", "--help", "--help-env", "--help-xoptions", "--help-all")
	pyLongValue = set("--check-hash-based-pycs")
	// T-0808 F4: python -c code that extracts. \s is Python's unicode \s: ASCII whitespace plus \v, \x1c-\x1f, \x85 and Z*.
	pyExtractCode  = regexp.MustCompile(`extractall|extract[\s\x0b\p{Z}\x{1c}-\x{1f}\x{85}]*\(|unpack_archive`)
	unzipNoExtract = set("-l", "-v", "-t", "-p", "-c", "-z", "-Z")
	tarRewrite     = set("--absolute-names", "--transform", "--xform", "--to-command", "--one-top-level")
)

// pyInlineCode is py_inline_code (T-0808 F5): a python command line carries its program inline: -c (bare, in a cluster such
// as -Bc, attached code too), or `-` / no program at all (heredoc or pipe on stdin). -m module and a script file are not inline.
func pyInlineCode(args []string) bool {
	k := 0
	for k < len(args) && strings.HasPrefix(args[k], "-") && len(args[k]) > 1 {
		cl := args[k]
		k++
		if strings.HasPrefix(cl, "--") {
			if in(pyLongFlag, cl) {
				return false // --version/--help print and exit: no program runs
			}
			if base, _, hasEq := strings.Cut(cl, "="); !hasEq && in(pyLongValue, base) {
				k++
			}
			continue
		}
		rs := []rune(cl)
	cluster:
		for i := 1; i < len(rs); i++ {
			switch rs[i] {
			case 'c':
				return true
			case 'm', 'V', 'h', '?':
				return false // -m module, or -V/-h/-? print and exit
			case 'W', 'X', 'Q':
				if i+1 >= len(rs) {
					k++
				}
				break cluster
			}
		}
	}
	return k >= len(args) || args[k] == "-"
}

// inlineInterpreter is inline_interpreter (T-0808 F5): an interpreter invocation whose program is inline (stdin heredoc or
// pipe for every interpreter, -c for python; other options are not tightened, T-0953).
func inlineInterpreter(name string, rest []string) bool {
	if !interpreterFull(name) {
		return false
	}
	if isPythonName(name) {
		return pyInlineCode(rest)
	}
	return len(rest) == 0 || rest[0] == "-"
}
