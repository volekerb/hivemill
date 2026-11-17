package checklist

import (
	"strconv"
	"strings"
)

// ParseDiff reads a git unified diff (a/ b/ prefixes) into per-path changes.
// Added, modified, renamed-to and deleted paths are all reported; deleted files
// carry no added lines.
func ParseDiff(diff string) []Change {
	var out, sources []Change
	cur := -1
	inHunk := false
	setPath := func(p string) {
		if cur >= 0 && p != "" {
			out[cur].Path = p
		}
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
			out = append(out, Change{})
			cur = len(out) - 1
			setPath(headerPath(strings.TrimPrefix(line, "diff --git ")))
		case inHunk && strings.HasPrefix(line, "+"):
			out[cur].Added = append(out[cur].Added, line[1:])
		case strings.HasPrefix(line, "@@"):
			inHunk = cur >= 0
		case inHunk:
			// context or removed line, or "\ No newline at end of file"
		case strings.HasPrefix(line, "rename from "):
			// The source path of a rename leaves its directory: it counts as a changed path too.
			sources = append(sources, Change{Path: unquote(strings.TrimPrefix(line, "rename from "))})
		case strings.HasPrefix(line, "rename to "):
			setPath(unquote(strings.TrimPrefix(line, "rename to ")))
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			setPath(markerPath(line[4:]))
		}
	}
	// Drop entries whose path could not be determined (nothing to match on).
	kept := out[:0]
	for _, c := range out {
		if c.Path != "" {
			kept = append(kept, c)
		}
	}
	return append(kept, sources...)
}

// markerPath reads the path of a `--- ` or `+++ ` line body; "" for /dev/null.
func markerPath(p string) string {
	p = strings.TrimSuffix(p, "\t")
	if p == "/dev/null" {
		return ""
	}
	p = unquote(p)
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

// headerPath extracts the path from `a/X b/X` (non-rename headers); "" when ambiguous.
func headerPath(rest string) string {
	if strings.HasPrefix(rest, `"`) {
		// Quoted header: take the first quoted token, the a/ side.
		for i := 1; i < len(rest); i++ {
			if rest[i] == '\\' {
				i++
			} else if rest[i] == '"' {
				return strings.TrimPrefix(unquote(rest[:i+1]), "a/")
			}
		}
		return ""
	}
	rest = strings.TrimPrefix(rest, "a/")
	if n := (len(rest) - 3) / 2; n > 0 && len(rest) == 2*n+3 && rest[n:n+3] == " b/" && rest[:n] == rest[n+3:] {
		return rest[:n]
	}
	return ""
}

func unquote(p string) string {
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}
