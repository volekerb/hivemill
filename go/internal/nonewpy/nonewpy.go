// Package nonewpy implements the G-041 SC4 check: no new Python is added after 2026-11-11.
// It reads `git diff --name-status` between two commits and reports every path that introduces Python.
package nonewpy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
)

// allowList holds paths exempt from the check. It stays empty (G-041 D2: new Python test files count too);
// a non-empty list needs a director decision cited in the PR.
var allowList = map[string]bool{}

// Change is one parsed name-status entry. Old is the source path for R and C, empty otherwise.
type Change struct {
	Status string // single letter: A C D M R T ...
	Old    string
	Path   string
}

// Violation is one new Python file.
type Violation struct {
	Path   string
	Status string
}

var pyExts = map[string]bool{".py": true, ".pyi": true, ".pyw": true, ".pyx": true}

var pyInterp = regexp.MustCompile(`^pythonw?[0-9.]*$`)

// IsPythonPath reports whether the path has a Python extension (case-insensitive).
func IsPythonPath(p string) bool {
	return pyExts[strings.ToLower(path.Ext(p))]
}

// HasPythonShebang reports whether the first line of content is a shebang naming a python interpreter,
// directly or through env (including `env -S python3 -u`).
func HasPythonShebang(content []byte) bool {
	line := content
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	s := strings.TrimSpace(string(line))
	if !strings.HasPrefix(s, "#!") {
		return false
	}
	for _, tok := range strings.Fields(s[2:]) {
		if pyInterp.MatchString(path.Base(tok)) {
			return true
		}
	}
	return false
}

// ParseNameStatus parses `git diff --name-status -z` output.
func ParseNameStatus(out []byte) ([]Change, error) {
	parts := strings.Split(string(out), "\x00")
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	var changes []Change
	for i := 0; i < len(parts); {
		st := parts[i]
		if st == "" {
			return nil, fmt.Errorf("empty status field in git diff output")
		}
		need := 2
		if st[0] == 'R' || st[0] == 'C' {
			need = 3
		}
		if i+need > len(parts) {
			return nil, fmt.Errorf("truncated git diff output at status %q", st)
		}
		c := Change{Status: st[:1]}
		if need == 3 {
			c.Old, c.Path = parts[i+1], parts[i+2]
		} else {
			c.Path = parts[i+1]
		}
		changes = append(changes, c)
		i += need
	}
	return changes, nil
}

// Reader returns the content of a path at the base or head commit; ok is false when it does not exist there.
type Reader func(atHead bool, p string) (content []byte, ok bool, err error)

// Evaluate applies the status rules to the changes.
func Evaluate(changes []Change, read Reader) ([]Violation, error) {
	var bad []Violation
	for _, c := range changes {
		if c.Status == "D" || allowList[c.Path] {
			continue
		}
		fail, err := isNewPython(c, read)
		if err != nil {
			return nil, err
		}
		if fail {
			bad = append(bad, Violation{Path: c.Path, Status: c.Status})
		}
	}
	return bad, nil
}

func isNewPython(c Change, read Reader) (bool, error) {
	if IsPythonPath(c.Path) {
		switch c.Status {
		case "M":
			return false, nil
		case "R":
			return !IsPythonPath(c.Old), nil
		default: // A, C, T and anything unknown fail closed
			return true, nil
		}
	}
	head, ok, err := read(true, c.Path)
	if err != nil {
		return false, err
	}
	if !ok || !HasPythonShebang(head) {
		return false, nil
	}
	// Head has a python shebang: new unless the source already had one.
	src := c.Path
	if c.Status == "R" {
		src = c.Old
	}
	if c.Status == "A" || c.Status == "C" { // a copy of a python script is new Python too
		return true, nil
	}
	old, ok, err := read(false, src)
	if err != nil {
		return false, err
	}
	return !(ok && HasPythonShebang(old)), nil
}

// Run diffs base...head in repo and returns the violations and the number of changed files.
func Run(repo, base, head string) ([]Violation, int, error) {
	for _, rev := range []string{base, head} {
		if strings.HasPrefix(rev, "-") {
			return nil, 0, fmt.Errorf("revision %q starts with '-': pass a commit sha", rev)
		}
	}
	out, err := git(repo, "diff", "--name-status", "-z", "-M50%", "-C50%", "--find-copies-harder", base+"..."+head, "--")
	if err != nil {
		return nil, 0, err
	}
	changes, err := ParseNameStatus(out)
	if err != nil {
		return nil, 0, err
	}
	mb, err := git(repo, "merge-base", base, head)
	if err != nil {
		return nil, 0, err
	}
	mergeBase := strings.TrimSpace(string(mb))
	read := func(atHead bool, p string) ([]byte, bool, error) {
		rev := mergeBase
		if atHead {
			rev = head
		}
		return readFirstBytes(repo, rev, p, atHead)
	}
	bad, err := Evaluate(changes, read)
	return bad, len(changes), err
}

// gitTimeout bounds every git call so a hung process (lazy fetch, lock wait) fails the check instead of blocking it.
const gitTimeout = 2 * time.Minute

// maxHead is how many bytes of a blob are read: only the first line is needed for the shebang test.
const maxHead = 256

// readFirstBytes returns the first bytes of repo's rev:p blob. ok is false for an entry that is not a blob
// (a submodule) or a path missing at the merge base. Any other git failure is an error, never "absent".
func readFirstBytes(repo, rev, p string, headRead bool) ([]byte, bool, error) {
	out, err := git(repo, "ls-tree", "-z", rev, "--", p)
	if err != nil {
		return nil, false, err
	}
	var typ string
	for _, ent := range strings.Split(string(out), "\x00") {
		meta, name, found := strings.Cut(ent, "\t")
		if !found || name != p {
			continue
		}
		if f := strings.Fields(meta); len(f) == 3 {
			typ = f[1]
		}
	}
	if typ == "" {
		if headRead {
			return nil, false, fmt.Errorf("%s missing at head %s although git diff lists it: rerun with a full clone", p, rev)
		}
		return nil, false, nil // absent at the merge base: a new file
	}
	if typ != "blob" {
		return nil, false, nil
	}
	return gitHead(repo, maxHead, "cat-file", "blob", rev+":"+p)
}

// gitHead runs git and returns at most limit bytes of stdout.
func gitHead(repo string, limit int64, args ...string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = cleanEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	buf, rerr := io.ReadAll(io.LimitReader(pipe, limit))
	_, _ = io.Copy(io.Discard, pipe) // drain so git exits normally
	if werr := cmd.Wait(); werr != nil || rerr != nil {
		if werr == nil {
			werr = rerr
		}
		return nil, false, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), werr, strings.TrimSpace(stderr.String()))
	}
	return buf, true, nil
}

// git runs git with argv (never a shell) and returns stdout; errors carry stderr.
func git(repo string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = cleanEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// cleanEnv drops GIT_* variables so an inherited GIT_DIR cannot redirect the diff (L-050).
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}
