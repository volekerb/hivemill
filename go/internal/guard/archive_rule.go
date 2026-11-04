package guard

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
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
	archiveSizeMax = int64(256 << 20) // ARCHIVE_SIZE_MAX: largest archive file the hook lists before refusing

	tarListRE   = regexp.MustCompile(`^-?[A-Za-z]*t[A-Za-z]*\n?$`)
	tarCreateRE = regexp.MustCompile(`^-?[A-Za-z]*[cxru][A-Za-z]*\n?$`)
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

// listingOnly is a port of Analyzer.listing_only: true for a command that cannot create a file (cd and plain readers,
// tar -t, unzip -l).
func listingOnly(words []string) bool {
	name, args := pyBasename(words[0]), words[1:]
	switch name {
	case "cd", "pushd", "popd", "ls", "cat", "echo", "printf", "true", "pwd", "test", "[", "file", "stat", "head":
		return !anyOf(args, func(x string) bool { return x == ">" || x == ">>" })
	case "tar", "gtar", "bsdtar":
		first := lastOfFirst(args)
		listed := first != "" && tarListRE.MatchString(first) && !tarCreateRE.MatchString(first)
		return listed || containsStr(args, "--list")
	case "unzip":
		return unzipListingOpts(args)
	}
	return false
}

func lastOfFirst(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
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

// archiveNames is a port of archive_names: (member, link target) for each member, read with the stdlib (never
// extracted). The error's text is the Python exception class name the message prints.
type archiveMember struct{ name, link string }

type archiveErr struct{ class string }

func (e *archiveErr) Error() string { return e.class }

func archiveNames(path string, zipped bool) ([]archiveMember, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if errors.Is(err, fs.ErrPermission) {
			return nil, &archiveErr{"PermissionError"}
		}
		return nil, &archiveErr{"OSError"}
	}
	defer f.Close()
	var out []archiveMember
	if zipped {
		zr, err := zip.OpenReader(path)
		if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
			return nil, &archiveErr{"BadZipFile"}
		}
		defer zr.Close()
		if len(zr.File) > archiveWalkMax {
			return nil, &archiveErr{"RuntimeError"}
		}
		for _, zf := range zr.File {
			out = append(out, archiveMember{zf.Name, ""})
		}
		return out, nil
	}
	br := bufio.NewReader(f)
	head, _ := br.Peek(5)
	var src io.Reader = br
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b, 0x08}):
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, &archiveErr{"ReadError"}
		}
		src = gz
	case bytes.HasPrefix(head, []byte("BZh91")):
		src = bzip2.NewReader(br)
	case bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z'}), bytes.HasPrefix(head, []byte{0x5d, 0, 0, 0x80}):
		return nil, &archiveErr{"NotImplementedError"} // xz/lzma: no stdlib decoder, so the list cannot be had (fails closed)
	}
	sr := bufio.NewReader(src)
	for { // concatenated archives: list every part, as tar -i extracts
		if _, perr := sr.Peek(1); perr != nil {
			break
		}
		tr := tar.NewReader(sr)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				if len(out) == 0 { // Python (ignore_zeros) skips junk blocks, finds no member and raises ValueError
					return nil, &archiveErr{"ValueError"}
				}
				return nil, &archiveErr{"ReadError"} // Go cannot resume past a bad header: tighter than Python, never looser
			}
			link := ""
			if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
				link = h.Linkname
			}
			out = append(out, archiveMember{h.Name, link})
			if len(out) > archiveWalkMax {
				return nil, &archiveErr{"RuntimeError"}
			}
		}
	}
	if len(out) == 0 { // an archive with no readable member is not a tar the hook can vouch for
		return nil, &archiveErr{"ValueError"}
	}
	return out, nil
}

func splitMember(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' })
}

// dotGit is c.lower() == ".git" (only ASCII can lower-case to those letters in Python).
func dotGit(c string) bool {
	if len(c) != 4 {
		return false
	}
	b := []byte(c)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b) == ".git"
}

// archiveMembers is a port of Analyzer.archive_members.
func (a *Analyzer) archiveMembers(name, arch string) error {
	const why = "extract an archive you have listed (tar -tf, unzip -l) that has no .git member and no config+HEAD pair"
	path := a.archivePath(arch)
	if path == "" {
		return blockf("`" + name + "` extracting from stdin, an expansion or an unresolvable archive name cannot be listed first " +
			"(it may carry .git/config): " + why)
	}
	zipped := name == "unzip"
	// unzip opens <name>, then <name>.zip, then <name>.ZIP: list every candidate that exists, not just the first
	cands := []string{path}
	if zipped {
		cands = append(cands, path+".zip", path+".ZIP")
	}
	var found []string
	for _, c := range cands {
		if _, err := os.Lstat(c); err == nil {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		found = []string{path}
	}
	var members []archiveMember
	for _, p := range found {
		if st, err := os.Stat(p); err == nil && (!st.Mode().IsRegular() || st.Size() > archiveSizeMax) {
			return blockf("`" + name + "` archive " + pyRepr(p) + " is not a regular file under 256 MB, so it is not " +
				"listed (a FIFO or a bomb would hang the hook): " + why)
		}
		ms, err := archiveNames(p, zipped)
		if err != nil {
			var ae *archiveErr
			if errors.Is(err, fs.ErrNotExist) {
				if !a.mayCreateBefore && !a.fsChanged {
					return nil // the extraction is the first command on the line and the file does not exist: it extracts nothing
				}
				return blockf("`" + name + "` archive " + pyRepr(p) + " does not exist yet; an earlier command on this line may create it, so " +
					"its members cannot be listed: run the extraction in its own command, after the archive exists")
			}
			cls := "OSError"
			if errors.As(err, &ae) {
				cls = ae.class
			}
			return blockf("`" + name + "` archive " + pyRepr(p) + " cannot be listed (" + cls + "), so a .git/config member cannot be " +
				"ruled out: " + why)
		}
		members = append(members, ms...)
	}
	bare := map[string]map[string]bool{}
	for _, m := range members {
		var parts []string
		for _, c := range splitMember(m.name) {
			if c != "." {
				parts = append(parts, c)
			}
		}
		linkGit := false
		if m.link != "" {
			for _, c := range pySplitSlash(m.link) {
				linkGit = linkGit || dotGit(c)
			}
		}
		if anyOf(parts, dotGit) || linkGit {
			return blockf("`" + name + "` archive " + pyRepr(found[len(found)-1]) + " has a member " + pyRepr(m.name) + " under a .git path: its config/hooks would run as " +
				"git (" + persistWhy + "); " + why)
		}
		if n := len(parts); n > 0 && (parts[n-1] == "config" || parts[n-1] == "HEAD") {
			k := strings.Join(parts[:n-1], "/")
			if bare[k] == nil {
				bare[k] = map[string]bool{}
			}
			bare[k][parts[n-1]] = true
		}
	}
	for _, v := range bare {
		if v["config"] && v["HEAD"] {
			return blockf("`" + name + "` archive " + pyRepr(found[len(found)-1]) + " has config and HEAD in one directory (a bare repo layout whose config " +
				"git runs: core.fsmonitor, core.hooksPath); " + why)
		}
	}
	return nil
}

// pySplitSlash is re.split(r"[/\\]", s): empty parts kept.
func pySplitSlash(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' || s[i] == '\\' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
