package spend

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// SessionCosts is the cost at API prices of each given session id, its subagent transcripts
// (<project>/<session>/subagents/*.jsonl) included. It reads only those files under root (~/.claude/projects).
func SessionCosts(root string, sessions []string) map[string]Bucket {
	want := map[string]bool{}
	for _, s := range sessions {
		want[s] = true
	}
	out := map[string]Bucket{}
	seen := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		owner := strings.TrimSuffix(parts[len(parts)-1], ".jsonl")
		if len(parts) == 4 && parts[2] == "subagents" {
			owner = parts[1]
		}
		if !want[owner] {
			return nil
		}
		b := out[owner]
		b.Key = owner
		_ = readFile(path, func(string) bool { return false }, func(r Request) { b.add(r) }, nil, seen)
		out[owner] = b
		return nil
	})
	return out
}
