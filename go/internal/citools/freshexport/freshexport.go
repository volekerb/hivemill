// Package freshexport is the Go port of ci/fresh_export.py: export a ref as ONE commit in a new directory and
// scan it.
//
//	ci-fresh-export --out DIR --author "Name <email>" [--ref origin/main] [--date YYYY-MM-DDTHH:MM:SS+0000]
//	                [--history-allowlist FILE] [--public-repo OWNER/NAME] [--repo DIR]
//
// `git archive <ref>` (tracked files only) is extracted into --out, committed as the only commit of a new `main`
// branch under --author with a fixed UTC date, checked structurally, and gated by `ci/scrub_scan.py --history`.
// It never adds a remote, never pushes and never touches the source repository. Exit 0 = gate clean, 1 = hits,
// 2 = usage error or refusal (--out left empty).
//
// Until the Go scrub scanner exists (G-048 P5b) the gate runs ci/scrub_scan.py through python3; see FindCIDir.
package freshexport

import (
	"archive/tar"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Slug tokens of the tree, substituted by --public-repo (built at runtime so this file is not rewritten by its own export).
var (
	TokenRepo  = "__PUBLIC_" + "REPO__"
	TokenOwner = "__PUBLIC_" + "OWNER__"
)

const (
	overlay = "ci/public-overlay"
	message = "Initial public release"
)

var (
	dateRe    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\+0000$`)
	authorRe  = regexp.MustCompile(`^\s*([^<>]+?)\s*<([^<>\s]+@[^<>\s]+)>\s*$`)
	ownerRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	originRe  = regexp.MustCompile(`^(?i:https?://(?:[^/@\s]*@)?github\.com/|git@github\.com:|ssh://git@github\.com/)([^/\s]+)/([^/\s]+?)(?i:\.git)?/?$`)
	modesOK   = map[string]bool{"100644": true, "100755": true, "120000": true}
	forbidTop = map[string]bool{"exports": true, "state": true, "run": true, "worktrees": true, "db": true}
)

// Refused is a usage error or a refusal (exit 2).
type Refused struct{ Msg string }

func (r Refused) Error() string { return r.Msg }

// Now is the clock; tests replace it.
var Now = time.Now

// FindCIDir returns the ci/ directory holding scrub_scan.py: the nearest one walking up from the working
// directory, else <repo>/ci.
func FindCIDir(repo string) (string, error) {
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; d = filepath.Dir(d) {
			if fileExists(filepath.Join(d, "ci", "scrub_scan.py")) {
				return filepath.Join(d, "ci"), nil
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	if fileExists(filepath.Join(repo, "ci", "scrub_scan.py")) {
		return filepath.Join(repo, "ci"), nil
	}
	return "", errors.New("cannot find ci/scrub_scan.py: run from a factory checkout or pass --ci-dir")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// GitEnv is the caller's environment minus git's own variables, plus the scanner's hardening (no user/system
// config, no replace objects), plus extra.
func GitEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	return append(env, extra...)
}

type gitResult struct {
	out, err string
	rc       int
}

func git(cwd string, extra []string, args ...string) (gitResult, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	cmd.Env = GitEnv(extra...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	res := gitResult{out: o.String(), err: e.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.rc = ee.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("cannot run git: %v", err)
	}
	return res, nil
}

func gitOK(cwd string, extra []string, args ...string) (string, error) {
	r, err := git(cwd, extra, args...)
	if err != nil {
		return "", err
	}
	if r.rc != 0 {
		return "", fmt.Errorf("git %s failed in %s: %s. Check --repo and --ref", strings.Join(args, " "), cwd, tail(strings.TrimSpace(r.err), 500))
	}
	return r.out, nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// realpath resolves p component by component: ".." is applied after the preceding component has been resolved
// through any symlink (never lexically first), and a component that does not exist is kept as is. A dangling
// link therefore still resolves to where it would land, so callers can check containment.
func realpath(p string) string {
	r, _ := realpathErr(p)
	return r
}

// errLoop reports a symlink chain that cannot be resolved (a loop or an unreadable link).
var errLoop = errors.New("symlink chain cannot be resolved (loop or unreadable link)")

// realpathErr is realpath that also reports a chain it could not resolve. The returned path is always cleaned;
// on error it is the cleaned input, and containment callers must refuse (fail closed) rather than trust it.
func realpathErr(p string) (string, error) {
	abs := p
	if !filepath.IsAbs(p) {
		var err error
		if abs, err = filepath.Abs(p); err != nil {
			return filepath.Clean(p), errLoop
		}
	}
	todo := strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator))
	cur := string(filepath.Separator)
	hops := 0
	for len(todo) > 0 {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, c)
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		hops++
		link, err := os.Readlink(next)
		if err != nil || hops > 255 {
			return filepath.Clean(abs), errLoop
		}
		if filepath.IsAbs(link) {
			cur = string(filepath.Separator)
		}
		todo = append(strings.Split(link, string(filepath.Separator)), todo...)
	}
	return cur, nil
}

// LiveHomes are the live factory homes: <passwd home>/factory plus the single safe line of
// <passwd home>/.config/factory/live_home (a regular file owned by the user, not group/other writable).
func LiveHomes() map[string]bool {
	return liveHomesAt(HomeBase(), os.Getuid())
}

// HomeOverride replaces the passwd home in tests, so they never read the live home.
var HomeOverride string

// HomeBase is the passwd home directory of the user (never $HOME unless the passwd entry is unusable).
func HomeBase() string {
	if HomeOverride != "" {
		return HomeOverride
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return os.Getenv("HOME")
}

func liveHomesAt(base string, uid int) map[string]bool {
	homes := map[string]bool{realpath(filepath.Join(base, "factory")): true}
	cfgdir := filepath.Join(base, ".config", "factory")
	path := filepath.Join(cfgdir, "live_home")
	st, err1 := os.Lstat(path)
	pst, err2 := os.Lstat(cfgdir)
	if err1 != nil || err2 != nil {
		return homes
	}
	if st.Mode().IsRegular() && pst.IsDir() && ownerOf(st) == uid && st.Mode().Perm()&0o022 == 0 {
		if data, err := os.ReadFile(path); err == nil {
			if live, ok := LiveHomeLine(data); ok {
				homes[realpath(live)] = true
			}
		}
	}
	return homes
}

// SplitLines splits like Python str.splitlines: on \n, \r, \r\n, \v, \f, \x1c-\x1e, \x85, \u2028 and \u2029,
// with no empty last element.
func SplitLines(s string) []string {
	var out []string
	rs := []rune(s)
	var cur []rune
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '\r':
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			out = append(out, string(cur))
			cur = cur[:0]
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(cur))
			cur = cur[:0]
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// LiveHomeLine returns the live home recorded in the content of a live_home file as the Python tools read it
// (text mode, so CRLF is normalised, then splitlines): the file must hold exactly one absolute line without NUL.
// Invalid UTF-8 is unreadable text and yields no home.
func LiveHomeLine(data []byte) (string, bool) {
	if !utf8.Valid(data) {
		return "", false
	}
	lines := SplitLines(string(data))
	if len(lines) == 1 && filepath.IsAbs(lines[0]) && !strings.Contains(lines[0], "\x00") {
		return lines[0], true
	}
	return "", false
}

// CheckOut resolves --out; it is refused inside the live home or when it exists and is not empty.
func CheckOut(out string) (string, error) {
	if strings.HasPrefix(out, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			out = filepath.Join(h, out[2:])
		}
	}
	target, rerr := realpathErr(out)
	if rerr != nil {
		return "", Refused{fmt.Sprintf("%s cannot be resolved (symlink loop or unreadable link). Choose a new directory without symlink loops, e.g. under /tmp.", out)}
	}
	for live := range LiveHomes() {
		if target == live || strings.HasPrefix(target, live+string(filepath.Separator)) {
			return "", Refused{fmt.Sprintf("%s is inside the live factory home %s. Choose a directory outside it, e.g. a new dir under /tmp.", target, live)}
		}
	}
	if st, err := os.Stat(target); err == nil {
		if !st.IsDir() {
			return "", Refused{fmt.Sprintf("%s exists and is not a directory. Choose a new path.", target)}
		}
		ents, err := os.ReadDir(target)
		if err != nil {
			return "", err
		}
		if len(ents) > 0 {
			return "", Refused{fmt.Sprintf("%s exists and is not empty. Choose a new or empty directory.", target)}
		}
	}
	return target, nil
}

// ParseAuthor splits "Name <email>".
func ParseAuthor(text string) (string, string, error) {
	m := authorRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", Refused{fmt.Sprintf("--author %q is not of the form \"Name <email>\".", text)}
	}
	return m[1], m[2], nil
}

// ParsePublicRepo splits and validates OWNER/NAME.
func ParsePublicRepo(text string) (string, string, error) {
	owner, name, ok := strings.Cut(text, "/")
	if !(ok && ownerRe.MatchString(owner) && nameRe.MatchString(name) && name != "." && name != "..") {
		return "", "", Refused{fmt.Sprintf("--public-repo %q is not OWNER/NAME (OWNER: letters, digits, '-', at most 39; NAME: letters, digits, "+
			"'.', '_', '-', at most 100, not '.' or '..'). Example: --public-repo example-org/factory.", text)}
	}
	return owner, name, nil
}

func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// extract runs `git archive ref` and unpacks it member by member into target; a member leaving target is refused.
func extract(repo, ref, target string) error {
	if _, err := gitOK(repo, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("git", "archive", "--format=tar", ref)
	cmd.Dir = repo
	cmd.Env = GitEnv() // user config (core.autocrlf, ...) must not change the archived bytes
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot run git: %v", err)
	}
	root := realpath(target)
	tr := tar.NewReader(pipe)
	var xerr error
	for xerr == nil {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			xerr = fmt.Errorf("reading the git archive of %s: %v", ref, err)
			break
		}
		xerr = extractMember(tr, h, root, ref)
	}
	io.Copy(io.Discard, pipe)
	if werr := cmd.Wait(); werr != nil && xerr == nil {
		xerr = fmt.Errorf("git archive %s failed in %s: %s. Check --repo and --ref", ref, repo, tail(strings.TrimSpace(stderr.String()), 500))
	}
	if xerr == nil {
		xerr = verifyLinks(root, ref)
	}
	return xerr
}

// verifyLinks re-resolves every extracted symlink once the tree is complete (a link created later in the archive
// can redirect an earlier one) and refuses any whose real target is outside root.
func verifyLinks(root, ref string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if r, err := realpathErr(p); err != nil || !within(root, r) {
			return fmt.Errorf("archive symlink member refused by the tar safety filter (link target, path or mode not allowed): "+
				"remove or fix that entry in %s and rerun. Find it with: git ls-tree -r %s", ref, ref)
		}
		return nil
	})
}

func extractMember(tr *tar.Reader, h *tar.Header, root, ref string) error {
	if h.Typeflag == tar.TypeXGlobalHeader {
		return nil
	}
	refuse := func(kind string) error {
		return fmt.Errorf("archive %s member refused by the tar safety filter (link target, path or mode not allowed): "+
			"remove or fix that entry in %s and rerun. Find it with: git ls-tree -r %s", kind, ref, ref)
	}
	name := strings.TrimSuffix(h.Name, "/")
	if name == "" || name == "." {
		return nil
	}
	if filepath.IsAbs(name) {
		return refuse("file")
	}
	dest := filepath.Join(root, name)
	if !within(root, dest) {
		return fmt.Errorf("archive member %q escapes %s", h.Name, root)
	}
	parent, perr := realpathErr(filepath.Dir(dest))
	if perr != nil || !within(root, parent) {
		return fmt.Errorf("archive member %q escapes %s", h.Name, root)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	switch h.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(dest, 0o755)
	case tar.TypeReg:
		mode := os.FileMode(0o644)
		if h.Mode&0o100 != 0 {
			mode = 0o755
		}
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.Chmod(dest, mode)
	case tar.TypeSymlink:
		target := h.Linkname
		// Follow the chain for real: the target is resolved from the (already resolved) parent through every
		// symlink extracted so far, so a hop that only looks inside the root lexically is still refused.
		if filepath.IsAbs(target) {
			return refuse("symlink")
		}
		if r, err := realpathErr(parent + string(filepath.Separator) + target); err != nil || !within(root, r) {
			return refuse("symlink")
		}
		return os.Symlink(target, dest)
	case tar.TypeLink:
		return refuse("hardlink")
	}
	return refuse("file")
}

func assertNoGeneratedState(target string) error {
	var bad []string
	ents, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if forbidTop[e.Name()] {
			bad = append(bad, e.Name())
		}
	}
	err = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "factory.db" {
			bad = append(bad, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("export contains generated state %v: remove it from the tracked tree before publishing.", bad)
	}
	return nil
}

// checkEntries refuses gitlinks and other modes, and paths with control characters or invalid UTF-8, in ref's
// tree. Names are not echoed: they may themselves be what the scan is looking for. Returns the blob count.
func checkEntries(repo, ref, what string) (int, error) {
	out, err := gitOK(repo, nil, "ls-tree", "-r", "-z", ref)
	if err != nil {
		return 0, err
	}
	badMode, badName, count := 0, 0, 0
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, path, _ := strings.Cut(rec, "\t")
		count++
		if f := strings.Fields(meta); len(f) == 0 || !modesOK[f[0]] {
			badMode++
		}
		if !utf8.ValidString(path) || strings.ContainsFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			badName++
		}
	}
	if badMode > 0 {
		return 0, fmt.Errorf("%s holds %d entries with a mode other than 100644, 100755 or 120000 (gitlink or "+
			"submodule): remove them from the source tree and rerun. Find them with: git ls-tree -r %s", what, badMode, ref)
	}
	if badName > 0 {
		return 0, fmt.Errorf("%s holds %d paths with control characters or invalid UTF-8: rename them in the source "+
			"tree and rerun. Find them with: git ls-tree -r -z %s | grep -zaP '[\\x00-\\x1f\\x7f\\x80-\\xff]'", what, badName, ref)
	}
	return count, nil
}

func assertStructure(target string) error {
	count, err := gitOK(target, nil, "rev-list", "--all", "--count")
	if err != nil {
		return err
	}
	if c := strings.TrimSpace(count); c != "1" {
		return fmt.Errorf("export has %s commits, expected 1: rerun into a new --out.", c)
	}
	refsOut, err := gitOK(target, nil, "for-each-ref", "--format=%(refname)")
	if err != nil {
		return err
	}
	if refs := strings.Fields(refsOut); len(refs) != 1 || refs[0] != "refs/heads/main" {
		return fmt.Errorf("export has refs %v, expected only refs/heads/main: rerun into a new --out.", refs)
	}
	rawOut, err := gitOK(target, nil, "cat-file", "commit", "HEAD")
	if err != nil {
		return err
	}
	raw, _, _ := strings.Cut(rawOut, "\n\n")
	var headers []string
	for _, ln := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(ln, " ") {
			h, _, _ := strings.Cut(ln, " ")
			headers = append(headers, h)
		}
	}
	if strings.Join(headers, ",") != "tree,author,committer" {
		return fmt.Errorf("commit headers are %v, expected tree, author, committer only: rerun into a new --out.", headers)
	}
	_, err = checkEntries(target, "HEAD", "the export commit")
	return err
}

// countBinaryBlobs counts the NUL-containing blobs of HEAD, for the summary.
func countBinaryBlobs(target string) (int, error) {
	out, err := gitOK(target, nil, "ls-tree", "-r", "-z", "HEAD")
	if err != nil {
		return 0, err
	}
	var ids []string
	for _, rec := range strings.Split(out, "\x00") {
		if f := strings.Fields(rec); len(f) >= 3 {
			ids = append(ids, f[2])
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir, cmd.Env = target, GitEnv()
	cmd.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("git cat-file failed in %s: %s", target, tail(strings.TrimSpace(e.String()), 500))
	}
	data, pos, binary := o.Bytes(), 0, 0
	for range ids {
		nl := bytes.IndexByte(data[pos:], '\n')
		if nl < 0 {
			return 0, errors.New("git cat-file --batch printed a short answer")
		}
		f := strings.Fields(string(data[pos : pos+nl]))
		size, err := strconv.Atoi(f[len(f)-1])
		if err != nil {
			return 0, fmt.Errorf("git cat-file --batch printed a bad header %q", data[pos:pos+nl])
		}
		start := pos + nl + 1
		if bytes.IndexByte(data[start:start+size], 0) >= 0 {
			binary++
		}
		pos = start + size + 1
	}
	return binary, nil
}

// privateSlug is OWNER/NAME of the source repo's GitHub origin, or "" (a note is printed) when there is none. An
// origin that is not a GitHub URL is refused. Only the derived OWNER/NAME leaves this function.
func privateSlug(repo string, stdout io.Writer) (string, error) {
	r, err := git(repo, nil, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	if r.rc != 0 {
		fmt.Fprintln(stdout, "fresh_export: note: the source repository has no GitHub origin; the private-slug comparison is skipped.")
		return "", nil
	}
	bad := Refused{"origin is not a GitHub URL the export can check; set a plain https://github.com/OWNER/NAME origin or remove it."}
	m := originRe.FindStringSubmatch(strings.TrimSpace(r.out))
	if m == nil {
		return "", bad
	}
	if !(ownerRe.MatchString(m[1]) && nameRe.MatchString(m[2]) && m[2] != "." && m[2] != "..") {
		return "", bad
	}
	return m[1] + "/" + m[2], nil
}

func pathLess(a, b string) bool {
	as, bs := strings.Split(a, string(filepath.Separator)), strings.Split(b, string(filepath.Separator))
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

// treeFiles are the regular (non-symlink) files of the extracted export, outside the top-level .git, sorted.
func treeFiles(target string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == filepath.Join(target, ".git") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return pathLess(files[i], files[j]) })
	return files, err
}

func rel(target, p string) string {
	r, err := filepath.Rel(target, p)
	if err != nil {
		return p
	}
	return r
}

func applyOverlay(target string) error {
	base := filepath.Join(target, filepath.FromSlash(overlay))
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		return nil
	}
	files, err := treeFiles(base)
	if err != nil {
		return err
	}
	for _, p := range files {
		dest := filepath.Join(target, rel(base, p))
		if _, err := os.Lstat(dest); err == nil {
			return Refused{fmt.Sprintf("overlay file %s would overwrite %s, which is already in the tree. Remove one of them in the source.", rel(target, p), rel(target, dest))}
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.Rename(p, dest); err != nil {
			return err
		}
	}
	return os.RemoveAll(base)
}

type subst struct {
	path string
	n    int
}

// substitute replaces the slug tokens in every UTF-8 text file; binary files are left alone.
func substitute(target, public string) ([]subst, error) {
	files, err := treeFiles(target)
	if err != nil {
		return nil, err
	}
	var out []subst
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			continue
		}
		text := string(data)
		n := strings.Count(text, TokenRepo) + strings.Count(text, TokenOwner)
		if n == 0 {
			continue
		}
		if public == "" {
			return nil, Refused{fmt.Sprintf("The tree names the public repository (%s); pass --public-repo OWNER/NAME.", TokenRepo)}
		}
		owner, _, _ := strings.Cut(public, "/")
		text = strings.ReplaceAll(strings.ReplaceAll(text, TokenRepo, public), TokenOwner, owner)
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(text), st.Mode().Perm()); err != nil {
			return nil, err
		}
		out = append(out, subst{rel(target, p), n})
	}
	return out, nil
}

// NamesPrivateSlug reports whether data names the private slug on a slug boundary, case-insensitively: not inside
// a longer name (acme/private-public). Any '.' after the slug is a boundary (fail-safe), so acme/private.git,
// .wiki, .zip and a sentence-ending dot are refused. The '/' may be percent-encoded (%2F, as in an API URL); a
// preceding %2F also counts as a boundary.
func NamesPrivateSlug(data []byte, private string) bool {
	parts := strings.Split(private, "/")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	re := regexp.MustCompile(`(?i)` + strings.Join(parts, `(?:/|%2F)`))
	isWord := func(c byte, extra string) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(extra, c) >= 0
	}
	for pos := 0; pos <= len(data); {
		loc := re.FindIndex(data[pos:])
		if loc == nil {
			return false
		}
		s, e := pos+loc[0], pos+loc[1]
		okStart := s == 0 || !isWord(data[s-1], "-") || (s >= 3 && strings.EqualFold(string(data[s-3:s]), "%2F"))
		okEnd := e == len(data) || !isWord(data[e], "_-")
		if okStart && okEnd {
			return true
		}
		pos = s + 1
	}
	return false
}

// checkSubstituted: no token anywhere, and no text file naming the private slug (paths and a count only).
func checkSubstituted(target, private string) error {
	files, err := treeFiles(target)
	if err != nil {
		return err
	}
	var left, named []string
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r := rel(target, p)
		if bytes.Contains(data, []byte(TokenRepo)) || bytes.Contains(data, []byte(TokenOwner)) {
			left = append(left, r)
		}
		if private != "" && bytes.IndexByte(data, 0) < 0 && NamesPrivateSlug(data, private) {
			named = append(named, r)
		}
	}
	if len(left) > 0 {
		return Refused{fmt.Sprintf("%d files still hold a public slug token after substitution: %s. Fix the token spelling.", len(left), strings.Join(left, ", "))}
	}
	if len(named) > 0 {
		return Refused{fmt.Sprintf("%d files name the private repository: %s. Replace the private slug with the %s token (or remove it) in the source and rerun.",
			len(named), strings.Join(named, ", "), TokenRepo)}
	}
	return nil
}

// ScanPython runs `python3 <ciDir>/scrub_scan.py args...` with the hardened git environment and returns its
// output and exit code. It exists until the Go scanner replaces it (G-048 P5b).
func ScanPython(ciDir string, dropTerms bool, args ...string) (stdout, stderr string, rc int, err error) {
	py, lerr := exec.LookPath("python3")
	if lerr != nil {
		return "", "", 0, errors.New("python3 not found: ci/scrub_scan.py needs it until the Go scanner lands (G-048 P5b). Install python3 or run on a box that has it")
	}
	cmd := exec.Command(py, append([]string{filepath.Join(ciDir, "scrub_scan.py")}, args...)...)
	cmd.Env = GitEnv()
	if dropTerms { // generic patterns only, like ci/changelog.py's Scanner([])
		var env []string
		for _, kv := range cmd.Env {
			if !strings.HasPrefix(kv, "SCRUB_"+"TERMS_FILE=") {
				env = append(env, kv)
			}
		}
		cmd.Env = env
	}
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	runErr := cmd.Run()
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return o.String(), e.String(), ee.ExitCode(), nil
	}
	if runErr != nil {
		return "", "", 0, fmt.Errorf("cannot run %s: %v", py, runErr)
	}
	return o.String(), e.String(), 0, nil
}

// ScanLines scans text lines with the scanner and returns the 1-based numbers of the lines that have a hit. Line
// separators other than "\n" inside a line are replaced by a space so the numbering holds.
func ScanLines(ciDir string, withTerms bool, lines []string) (map[int]bool, error) {
	clean := strings.NewReplacer("\r", " ", "\v", " ", "\f", " ", "\x1c", " ", "\x1d", " ", "\x1e", " ", "\u0085", " ", "\u2028", " ", "\u2029", " ")
	f, err := os.CreateTemp("", "scan-lines-*.txt")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	for _, l := range lines {
		fmt.Fprintln(f, clean.Replace(l))
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	out, stderr, rc, err := ScanPython(ciDir, !withTerms, "--path", f.Name())
	if err != nil {
		return nil, err
	}
	if rc != 0 && rc != 1 {
		return nil, fmt.Errorf("ci/scrub_scan.py failed (exit %d): %s. Fix the scanner error and rerun", rc, tail(strings.TrimSpace(stderr), 300))
	}
	hits := map[int]bool{}
	prefix := f.Name() + ":"
	for _, ln := range strings.Split(out, "\n") {
		fields := strings.Fields(ln)
		if len(fields) >= 2 && strings.HasPrefix(fields[1], prefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(fields[1], prefix)); err == nil {
				hits[n] = true
			}
		}
	}
	return hits, nil
}

func masked(ciDir, text string) (string, error) {
	hits, err := ScanLines(ciDir, true, []string{text})
	if err != nil {
		return "", err
	}
	if len(hits) > 0 {
		return "[masked: scanner hit]", nil
	}
	return text, nil
}

type options struct {
	out, author, ref, date, allowlist, publicRepo, repo, ciDir string
}

// Main runs the tool and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fresh_export.py", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.out, "out", "", "new or empty directory for the export (not inside the live home)")
	fs.StringVar(&o.author, "author", "", `author and committer of the single commit, "Name <email>"`)
	fs.StringVar(&o.ref, "ref", "origin/main", "ref to export")
	fs.StringVar(&o.date, "date", "", "author and committer date YYYY-MM-DDTHH:MM:SS+0000 (default: today, UTC, at 00:00:00+0000)")
	fs.StringVar(&o.allowlist, "history-allowlist", "", "reviewed fixture lines for the gate (default ci/scrub_history_allowlist.txt)")
	fs.StringVar(&o.publicRepo, "public-repo", "", "slug of the new public repository, substituted for the slug tokens of the tree")
	fs.StringVar(&o.repo, "repo", ".", "source repository")
	fs.StringVar(&o.ciDir, "ci-dir", "", "directory holding scrub_scan.py (default: found from the working directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if o.out == "" || o.author == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "fresh_export.py: error: --out and --author are required")
		return 2
	}
	if o.date != "" && !dateRe.MatchString(o.date) {
		fmt.Fprintf(stderr, "fresh_export.py: error: argument --date: %q is not YYYY-MM-DDTHH:MM:SS+0000 (the timezone is fixed to UTC on purpose)\n", o.date)
		return 2
	}
	rc, err := run(o, stdout)
	if err != nil {
		var r Refused
		if errors.As(err, &r) {
			fmt.Fprintf(stderr, "fresh_export: refused: %s\n", r.Msg)
		} else {
			fmt.Fprintf(stderr, "fresh_export: error: %v\n", err)
		}
		return 2
	}
	return rc
}

func run(o options, stdout io.Writer) (int, error) {
	name, email, err := ParseAuthor(o.author)
	if err != nil {
		return 0, err
	}
	if strings.HasPrefix(o.ref, "-") {
		return 0, Refused{fmt.Sprintf("--ref %q starts with '-' and would be read as a git option. Pass a branch, tag or commit.", o.ref)}
	}
	if o.publicRepo != "" {
		if _, _, err := ParsePublicRepo(o.publicRepo); err != nil {
			return 0, err
		}
	}
	target, err := CheckOut(o.out)
	if err != nil {
		return 0, err
	}
	repo, err := filepath.Abs(o.repo)
	if err != nil {
		return 0, err
	}
	rc, err := export(o, repo, target, name, email, stdout)
	if err != nil { // any failure leaves --out empty again
		if ents, rerr := os.ReadDir(target); rerr == nil {
			for _, e := range ents {
				os.RemoveAll(filepath.Join(target, e.Name()))
			}
		}
		return 0, err
	}
	return rc, nil
}

func export(o options, repo, target, name, email string, stdout io.Writer) (int, error) {
	ciDir := o.ciDir
	if ciDir == "" {
		d, err := FindCIDir(repo)
		if err != nil {
			return 0, err
		}
		ciDir = d
	}
	allowlist := o.allowlist
	if allowlist == "" {
		allowlist = filepath.Join(ciDir, "scrub_history_allowlist.txt")
	}
	date := o.date
	if date == "" {
		date = Now().UTC().Format("2006-01-02") + "T00:00:00+0000"
	}
	private, err := privateSlug(repo, stdout)
	if err != nil {
		return 0, err
	}
	if private != "" && o.publicRepo != "" && strings.EqualFold(o.publicRepo, private) {
		return 0, Refused{"--public-repo names the source repository; the public repository must be a new one."}
	}
	if _, err := checkEntries(repo, o.ref+"^{tree}", o.ref); err != nil {
		return 0, err
	}
	if err := extract(repo, o.ref, target); err != nil {
		return 0, err
	}
	if err := assertNoGeneratedState(target); err != nil {
		return 0, err
	}
	if err := applyOverlay(target); err != nil {
		return 0, err
	}
	substituted, err := substitute(target, o.publicRepo)
	if err != nil {
		return 0, err
	}
	if err := checkSubstituted(target, private); err != nil {
		return 0, err
	}
	env := []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	for _, step := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", message}} {
		if _, err := gitOK(target, env, step...); err != nil {
			return 0, err
		}
	}
	if err := assertStructure(target); err != nil {
		return 0, err
	}
	if o.publicRepo != "" {
		total := 0
		for _, s := range substituted {
			total += s.n
		}
		fmt.Fprintf(stdout, "fresh_export: public repo %s; substituted %d occurrences in %d files\n", o.publicRepo, total, len(substituted))
		for _, s := range substituted {
			fmt.Fprintf(stdout, "  %s: %d\n", s.path, s.n)
		}
	}
	lsOut, err := gitOK(target, nil, "ls-tree", "-r", "-z", "--name-only", "HEAD")
	if err != nil {
		return 0, err
	}
	files := len(strings.Split(lsOut, "\x00")) - 1
	fmt.Fprintf(stdout, "== gate: scrub_scan --history %s (blobs as committed, path names, author, committer, message, refs, tags)\n", target)
	gOut, gErr, grc, err := ScanPython(ciDir, false, "--history", target, "--history-allowlist", allowlist)
	if err != nil {
		return 0, err
	}
	io.WriteString(stdout, gOut)
	if gErr != "" {
		fmt.Fprint(os.Stderr, gErr)
	}
	if grc != 0 && grc != 1 {
		return 0, fmt.Errorf("history scan failed (exit %d); fix the scanner error above and rerun.", grc)
	}
	binary, err := countBinaryBlobs(target)
	if err != nil {
		return 0, err
	}
	who, err := masked(ciDir, fmt.Sprintf("%s <%s>", name, email))
	if err != nil {
		return 0, err
	}
	verdict := "clean"
	if grc != 0 {
		verdict = "HITS"
	}
	fmt.Fprintf(stdout, "fresh_export: ref=%s out=%s files=%d binary_blobs=%d commits=1 date=%s author=%s\n", o.ref, target, files, binary, date, who)
	fmt.Fprintf(stdout, "fresh_export: history gate %s. Publish only with `git push <new-remote> main` from the export directory; never tar or copy it.\n", verdict)
	if grc != 0 {
		fmt.Fprintln(stdout, "fresh_export: FAIL - do not publish this export. Fix the hits in the source, then rerun into a new --out.")
		return 1, nil
	}
	fmt.Fprintln(stdout, "fresh_export: OK - 0 hits. Nothing was pushed and no remote was added.")
	return 0, nil
}

// ReadText reads a file like Python's Path.read_text(encoding="utf-8"): invalid UTF-8 is an error and \r\n and
// a lone \r become \n.
func ReadText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not valid UTF-8: fix the file encoding", path)
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n"), nil
}
