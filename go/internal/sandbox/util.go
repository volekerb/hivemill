// Package sandbox is the Go port of scout/{nsbox,workerbox,gatebox,testbox,egress,sandbox,ghcred}.py: the
// namespace view, the worker/gate/test profiles, the egress allow-list proxy and the scoped gh credential.
//
// SECURITY CODE. Everything is stdlib only (no cgo, no unsafe, no shell): helpers are exec'd as argv, mounts go
// through syscall.Mount on /proc/self/fd/<fd> targets opened component by component without following symlinks.
package sandbox

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Realpath is Python's os.path.realpath (non-strict): every symlink on the path is followed, including a dangling
// one (its target is appended) and a symlinked parent of a missing path; a symlink loop is left as is.
func Realpath(p string) string {
	// No lexical Clean: ".." after a symlink must be resolved physically, as Python does.
	if !filepath.IsAbs(p) {
		if wd, err := os.Getwd(); err == nil {
			p = wd + "/" + p
		}
	}
	return realpathWalk("/", splitPath(p), map[string]*string{})
}

// realpathWalk is posixpath._joinrealpath. seen memoises every resolved link, as Python's does (nil while a link is
// being resolved, so meeting it again is a loop), which keeps a doubling chain (l1 -> l0/l0, l2 -> l1/l1, ...) linear.
func realpathWalk(path string, rest []string, seen map[string]*string) string {
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		switch name {
		case ".":
			continue
		case "..":
			path = filepath.Dir(path)
			continue
		}
		newPath := filepath.Join(path, name)
		target, err := os.Readlink(newPath)
		if err != nil {
			path = newPath
			continue
		}
		if r, ok := seen[newPath]; ok {
			if r != nil {
				path = *r
			} else { // loop: Python keeps the path unresolved
				path = newPath
			}
			continue
		}
		seen[newPath] = nil
		base := path
		if filepath.IsAbs(target) {
			base = "/"
		}
		path = realpathWalk(base, splitPath(target), seen)
		res := path
		seen[newPath] = &res
	}
	return path
}

// Abs is os.path.abspath.
func Abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return a
}

// under reports path == base or path inside base.
func under(path, base string) bool {
	return path == base || strings.HasPrefix(path, strings.TrimRight(base, "/")+"/")
}

// RealHome is the home from the passwd entry, never $HOME.
func RealHome() string {
	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		return os.Getenv("HOME")
	}
	return u.HomeDir
}

// symlinkParent is the first ancestor of an absolute path that is a symlink ("" when none; the leaf is not looked at).
func symlinkParent(path string) string {
	cur := "/"
	parts := splitPath(path)
	for i := 0; i+1 < len(parts); i++ {
		cur = filepath.Join(cur, parts[i])
		if isLink(cur) {
			return cur
		}
	}
	return ""
}

func splitPath(p string) []string {
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

func isLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
