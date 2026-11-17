package identity

import (
	"os/user"
	"strconv"
	"unicode/utf8"

	"factory/go/internal/sandbox"
)

// Realpath is Python's os.path.realpath (non-strict). It is sandbox.Realpath, so the two cannot drift: no lexical Clean
// (".." after a symlink is resolved physically), a dangling link's target is appended, a symlinked parent of a missing
// path is followed, and a symlink loop is left as is.
func Realpath(p string) string { return sandbox.Realpath(p) }

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
