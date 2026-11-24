package spend

import (
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
)

// SessionCosts is the cost at API prices of each given session id, its subagent transcripts
// (<project>/<session>/subagents/*.jsonl) included. It reads only those files under root (~/.claude/projects).
func SessionCosts(root string, sessions []string) map[string]Bucket {
	return SessionCostsN(root, sessions, 1)
}

// SessionCostsN is SessionCosts reading at most workers sessions at a time. The tree is walked once; a request
// is counted once per session (its own transcripts and its subagents'), so the result does not depend on workers.
func SessionCostsN(root string, sessions []string, workers int) map[string]Bucket {
	want := map[string]bool{}
	for _, s := range sessions {
		want[s] = true
	}
	files := map[string][]string{}
	var owners []string
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
		if files[owner] == nil {
			owners = append(owners, owner)
		}
		files[owner] = append(files[owner], path)
		return nil
	})
	if workers < 1 {
		workers = 1
	}
	out := map[string]Bucket{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for _, owner := range owners {
		wg.Add(1)
		sem <- struct{}{}
		go func(owner string) {
			defer func() { <-sem; wg.Done() }()
			b := Bucket{Key: owner}
			seen := map[string]bool{}
			for _, p := range files[owner] {
				_ = readFile(p, func(string) bool { return false }, func(r Request) { b.add(r) }, nil, seen)
			}
			mu.Lock()
			out[owner] = b
			mu.Unlock()
		}(owner)
	}
	wg.Wait()
	return out
}
