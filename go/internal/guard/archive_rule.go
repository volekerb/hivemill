package guard

import (
	"os"
	"strings"
)

// T-0805 (worker-guard.py): one grammar for tar/gtar/bsdtar arguments (parse_tar) and rule E for a NO_PUSH_ROLES
// extraction by tar, gtar, bsdtar or unzip: E1 every option is in a fixed allowlist, E2 the archive resolves to one
// regular file, E3 its member list (never extracted by the hook) has no .git member. Python is the reference.

var (
	tarShortOK     = "xfvzjJCopk"
	tarLongFlag    = set("--extract", "--gzip", "--bzip2", "--xz", "--verbose", "--no-same-owner", "--keep-old-files")
	unzipFlagOK    = set("-o", "-n", "-q", "-qq")
	archiveEnvList = []string{"TAR_OPTIONS", "POSIXLY_CORRECT", "UNZIP", "UNZIPOPT", "UNZIP_OPTS", "TAR_READER_OPTIONS"}
)

// isTarName reports the tar spellings the extraction rule covers.
func isTarName(n string) bool { return n == "tar" || n == "gtar" || n == "bsdtar" }

// parseTar is a port of parse_tar: (extract, archives, dirs, problems). A word it cannot classify counts as extract
// mode (fail closed); problems lists every reason the argv is outside the E1 allowlist.
func parseTar(args []string) (extract bool, archives, dirs, problems []string) {
	var marks []int
	k := 0
	for k < len(args) {
		a, j := args[k], k
		k++
		switch {
		case a == "--":
			problems = append(problems, "a literal `--` (the hook and tar would disagree on what follows)")
		case strings.HasPrefix(a, "--"):
			opt, val, eq := a, "", false
			if i := strings.IndexByte(a, '='); i >= 0 {
				opt, val, eq = a[:i], a[i+1:], true
			}
			extract = extract || (len(opt) >= 5 && strings.HasPrefix("--extract", opt)) || (len(opt) >= 4 && strings.HasPrefix("--get", opt))
			if opt == "--file" || opt == "--directory" {
				if !eq {
					if k >= len(args) {
						problems = append(problems, opt+" without a value")
						continue
					}
					val = args[k]
					k++
				}
				if opt == "--file" {
					archives = append(archives, val)
				} else {
					dirs = append(dirs, val)
				}
			} else if !in(tarLongFlag, opt) || eq {
				problems = append(problems, "option "+pyRepr(a))
			}
		case strings.HasPrefix(a, "-C") && len(a) > 2: // -CDIR: the same target as -C DIR
			dirs = append(dirs, a[2:])
		case (strings.HasPrefix(a, "-") && len(a) > 1) || (j == 0 && a != "" && isASCIILetter(a[0])):
			letters := strings.TrimLeft(a, "-")
			values := []int{}
			for i := 0; i < len(letters); i++ {
				if letters[i] == 'f' || letters[i] == 'C' {
					values = append(values, i)
				}
			}
			bad := letters == "" || len(values) > 1 || (len(values) > 0 && values[0] != len(letters)-1)
			for i := 0; i < len(letters) && !bad; i++ {
				if !isASCIILetter(letters[i]) || !(strings.IndexByte(tarShortOK, letters[i]) >= 0 || letters[i] == 't') {
					bad = true
				}
			}
			if bad {
				extract = true // unclassifiable: assume tar extracts
				problems = append(problems, "option "+pyRepr(a))
				continue
			}
			extract = extract || strings.Contains(letters, "x")
			for i := 0; i < len(letters); i++ {
				if strings.IndexByte(tarShortOK, letters[i]) < 0 {
					problems = append(problems, "option "+pyRepr(a))
				}
			}
			if len(values) > 0 {
				last := letters[len(letters)-1]
				if k >= len(args) {
					problems = append(problems, "-"+string(last)+" without a value")
					continue
				}
				if last == 'f' {
					archives = append(archives, args[k])
				} else {
					dirs = append(dirs, args[k])
				}
				k++
			}
		default:
			marks = append(marks, len(dirs)) // a member name: -C only applies to the names after it
		}
	}
	if len(archives) > 1 {
		problems = append(problems, "more than one -f/--file")
	}
	for _, m := range marks {
		if m < len(dirs) {
			problems = append(problems, "a member name before -C (GNU tar applies -C only to later names)")
			break
		}
	}
	if len(dirs) > 1 {
		problems = append(problems, "more than one -C/--directory (GNU tar chains them)")
	}
	return
}

// archiveEnvOnLine is ARCHIVE_ENV_RE.search(re.sub(r"[\'\"\\]", "", text)): \b(name)\b in the quote-stripped text.
func archiveEnvOnLine(text string) bool {
	t := strings.NewReplacer("'", "", "\"", "", "\\", "").Replace(text)
	for _, n := range archiveEnvList {
		for from := 0; ; {
			i := strings.Index(t[from:], n)
			if i < 0 {
				break
			}
			i += from
			if boundary(t, i) && boundary(t, i+len(n)) {
				return true
			}
			from = i + 1
		}
	}
	return false
}

func archiveEnvInEnviron() bool {
	for _, n := range archiveEnvList {
		if _, ok := os.LookupEnv(n); ok {
			return true
		}
	}
	return false
}

// archiveRuleE is a port of Analyzer.archive_rule_e. It reports whether the command extracts.
func (a *Analyzer) archiveRuleE(name string, args []string) (bool, error) {
	const why = "name a scratchpad target (tar -C, unzip -d), use only -x -f -v -z -j -J -C -o -p -k (unzip -d -o -n -q) " +
		"and an archive that has no .git member"
	const why0 = "name the target (tar -C, unzip -d, cpio -D) as a scratchpad dir that has no .git" // the T-0783 text
	zipped := name == "unzip"
	var extract bool
	var tarch, tdirs, tproblems []string
	if zipped {
		extract = !unzipListingOpts(args)
	} else {
		extract, tarch, tdirs, tproblems = parseTar(args)
	}
	if !extract {
		return false, nil
	}
	if archiveEnvOnLine(a.text) || archiveEnvInEnviron() {
		return false, blockf("`" + name + "` extraction with " + strings.Join(archiveEnvList, "/") + " on the line or in the environment: they change " +
			"the options or names tar/unzip use, so the listing the hook read may not match; unset it. " + why)
	}
	if !zipped && anyOf(args, func(x string) bool {
		return in(tarRewrite, x) || strings.HasPrefix(x, "--transform=") || strings.HasPrefix(x, "--xform=") ||
			strings.HasPrefix(x, "--to-command=") || (alphaCluster(x, true) && strings.Contains(x, "P") && !strings.HasPrefix(x, "--"))
	}) {
		return false, blockf("`" + name + "` option that rewrites member names (-P, --transform, --to-command) can escape the -C target " +
			"into a .git dir: " + why0)
	}
	bad := func(what string) error {
		return blockf("`" + name + "` extraction with " + what + ": names on disk may differ from the listing the hook read, so a " +
			".git/config member cannot be ruled out; " + why)
	}
	var dirs, pos []string
	arch := ""
	if !zipped {
		if len(tproblems) > 0 {
			return false, bad(tproblems[0])
		}
		dirs = tdirs
		if len(tarch) > 0 {
			arch = tarch[0]
		}
	} else {
		for k := 0; k < len(args); {
			x := args[k]
			k++
			switch {
			case x == "--":
				return false, bad("a literal `--` (the hook and unzip would disagree on what follows)")
			case x == "-d":
				if k >= len(args) {
					return false, bad("-d without a directory")
				}
				dirs = append(dirs, args[k])
				k++
			case strings.HasPrefix(x, "-d") && len(x) > 2: // -dDIR: the same target as -d DIR
				dirs = append(dirs, x[2:])
			case strings.HasPrefix(x, "-") && len(x) > 1:
				if !in(unzipFlagOK, x) {
					return false, bad("option " + pyRepr(x))
				}
			default:
				pos = append(pos, x)
			}
		}
		if len(pos) > 0 {
			arch = pos[0]
		}
	}
	if len(dirs) == 0 {
		return false, blockf("`" + name + "` extracting without a target directory cannot be bounded (an archive may carry .git/config " +
			"or hooks): " + why0)
	}
	for _, d := range dirs {
		if err := a.archiveTarget(name, d); err != nil {
			return false, err
		}
	}
	if err := a.archiveMembers(name, arch); err != nil {
		return false, err
	}
	return true, nil
}

// archivePath is a port of Analyzer.archive_path: the absolute path of the archive named arch, or "" when it is stdin,
// a glob, built from an expansion, a remote name (host:path) or relative with an unknown cwd.
func (a *Analyzer) archivePath(arch string) string {
	if arch == "" || arch == "-" || strings.ContainsAny(arch, "$`*?[{~\\") || (!strings.HasPrefix(arch, "/") && !a.ccwdKnown) {
		return ""
	}
	first, _, _ := strings.Cut(arch, "/")
	if strings.Contains(first, ":") {
		return ""
	}
	if strings.HasPrefix(arch, "/") {
		return arch
	}
	return pyJoin(a.ccwd, arch)
}

// archiveMembers is the member-list step of rule E3. Go does not read archives: the list needs Python's tarfile and
// zipfile (the reference the spec names), so every extraction that passes the checks above is delegated to the Python
// hook. Delegating before any Lstat/Stat sends missing-file, FIFO and oversize cases there too; Go cannot disagree
// with its own reference. RunPython exits 2 when python3 or the hook is missing or fails, so this never allows.
func (a *Analyzer) archiveMembers(name, arch string) error {
	const why = "extract an archive you have listed (tar -tf, unzip -l) that has no .git member and no config+HEAD pair"
	if a.archivePath(arch) == "" {
		return blockf("`" + name + "` extracting from stdin, an expansion or an unresolvable archive name cannot be listed first " +
			"(it may carry .git/config): " + why)
	}
	return delegate("archive member list")
}
