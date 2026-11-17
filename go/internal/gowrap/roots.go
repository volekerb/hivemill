package gowrap

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// roots are the places go may write: the task worktree, /tmp, and the worker cache dirs.
type roots struct {
	Cwd      string // realpath(cwd)
	Worktree string // "" when cwd is not in a linked worktree under an allowed parent
	Tmp      string
	Cache    string
	TmpDir   string
}

func inside(p, root string) bool {
	if root == "" {
		return false
	}
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// cwdOK: a path that cwd or -C may point at (worktree or /tmp).
func (r *roots) cwdOK(p string) bool { return inside(p, r.Worktree) || inside(p, r.Tmp) }

// writeOK: a path an output flag may point at (any writable root).
func (r *roots) writeOK(p string) bool {
	return r.cwdOK(p) || inside(p, r.Cache) || inside(p, r.TmpDir)
}

func (r *roots) writable() []string {
	var out []string
	for _, p := range []string{r.Worktree, r.Tmp, r.Cache, r.TmpDir} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func computeRoots(home, cwd, gocache, gotmp string) (*roots, error) {
	rcwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve cwd %s: %w", cwd, err)
	}
	r := &roots{Cwd: rcwd}
	if r.Tmp, err = filepath.EvalSymlinks("/tmp"); err != nil {
		return nil, fmt.Errorf("cannot resolve /tmp: %w", err)
	}
	if r.Cache, err = filepath.EvalSymlinks(gocache); err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", gocache, err)
	}
	if r.TmpDir, err = filepath.EvalSymlinks(gotmp); err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", gotmp, err)
	}
	allowed := []string{r.Tmp}
	for _, sub := range []string{"factory/worktrees", "factory/run"} {
		if p, err := filepath.EvalSymlinks(filepath.Join(home, sub)); err == nil {
			allowed = append(allowed, p)
		}
	}
	r.Worktree = findWorktree(rcwd, allowed)
	return r, nil
}

// findWorktree walks up from dir to the first directory with a .git FILE (a linked worktree). A .git directory
// (a main checkout) stops the walk with no root. The worktree must sit under one of the allowed parents.
func findWorktree(dir string, allowed []string) string {
	for {
		st, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			if !st.Mode().IsRegular() {
				return ""
			}
			for _, a := range allowed {
				if inside(dir, a) {
					return dir
				}
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// realPath resolves p (relative to cwd) through symlinks. A final component that does not exist yet is allowed
// when its parent resolves (an output file); the path is never cleaned lexically before symlinks are resolved.
func realPath(cwd, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = cwd + "/" + p
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	trimmed := strings.TrimRight(p, "/")
	i := strings.LastIndex(trimmed, "/")
	if i < 0 {
		return "", fmt.Errorf("cannot resolve %q", p)
	}
	base := trimmed[i+1:]
	if base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("cannot resolve %q", p)
	}
	parent := trimmed[:i]
	if parent == "" {
		parent = "/"
	}
	rp, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(rp, base), nil
}
