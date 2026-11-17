package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrBadRoot is wrapped by every OpenRoot refusal.
var ErrBadRoot = errors.New("fsx: refusing root")

// ErrEscape is returned for a rel path that is absolute or contains "..".
var ErrEscape = errors.New("fsx: path escapes root")

// Root is a directory all later operations are confined to.
type Root struct {
	path string
	r    *os.Root
	dev  uint64
}

// OpenRoot opens path as a deletion root. It refuses "/", the user's home
// directory itself, a relative path, and a path that is itself a symlink.
func OpenRoot(path string) (*Root, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: %q is not absolute", ErrBadRoot, path)
	}
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: filesystem root", ErrBadRoot)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if sameDir(clean, home) {
			return nil, fmt.Errorf("%w: home directory %q itself", ErrBadRoot, clean)
		}
	}
	fi, err := os.Lstat(clean)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRoot, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %q is a symlink", ErrBadRoot, clean)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%w: %q is not a directory", ErrBadRoot, clean)
	}
	r, err := os.OpenRoot(clean)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRoot, err)
	}
	dev, _ := devOf(fi)
	return &Root{path: clean, r: r, dev: dev}, nil
}

// Path is the absolute root path.
func (r *Root) Path() string { return r.path }

// Close releases the root handle.
func (r *Root) Close() error { return r.r.Close() }

// sameDir reports whether a and b are the same directory, by name or by the
// resolved path (home may itself be reached through a symlink).
func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// cleanRel validates a root-relative path. "" and "." mean the root itself.
func cleanRel(rel string) (string, error) {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: absolute path %q", ErrEscape, rel)
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: %q contains ..", ErrEscape, rel)
		}
	}
	c := filepath.Clean(rel)
	if c == "" {
		c = "."
	}
	return c, nil
}
