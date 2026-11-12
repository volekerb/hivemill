package identity

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Realpath is Python's os.path.realpath (non-strict): symlinks are resolved as far as the path exists and the
// remaining components are kept lexically.
func Realpath(p string) string {
	if !filepath.IsAbs(p) {
		if wd, err := os.Getwd(); err == nil {
			p = filepath.Join(wd, p)
		}
	}
	parts := strings.Split(p, "/")
	resolved := "/"
	links := 0
	for len(parts) > 0 {
		c := parts[0]
		parts = parts[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, c)
		fi, err := os.Lstat(next)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		links++
		if links > 40 {
			return filepath.Join(append([]string{next}, parts...)...)
		}
		target, err := os.Readlink(next)
		if err != nil {
			resolved = next
			continue
		}
		if filepath.IsAbs(target) {
			resolved = "/"
		}
		parts = append(strings.Split(target, "/"), parts...)
	}
	return resolved
}

// pwDir is the passwd home directory of uid.
func pwDir(uid int) (string, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

// splitLines is Python's str.splitlines (no keepends) for the separators it knows.
func splitLines(s string) []string {
	var out []string
	start := 0
	i := 0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, s[start:i])
			start = i + w
		case '\r':
			out = append(out, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
		i += w
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
