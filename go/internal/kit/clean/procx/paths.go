package procx

import (
	"path/filepath"
	"strings"
)

// candidates returns the spellings of p to match against: the cleaned absolute
// path and, when it resolves, its symlink-free form (macOS /var vs /private/var).
func candidates(p string) []string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	out := []string{abs}
	if r, err := filepath.EvalSymlinks(abs); err == nil && r != abs {
		out = append(out, r)
	}
	return out
}

// under reports whether target is at or below any of roots.
func under(target string, roots []string) bool {
	target = strings.TrimSuffix(target, " (deleted)")
	if !filepath.IsAbs(target) {
		return false
	}
	target = filepath.Clean(target)
	for _, r := range roots {
		if target == r || strings.HasPrefix(target, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}
