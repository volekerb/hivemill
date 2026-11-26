package spend

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// subagentOf reads a transcript's path under the root (split at "/"): a subagent transcript is
// <project>/<session>/subagents/*.jsonl, or <project>/<session>/subagents/workflows/<run>/*.jsonl for a Workflow agent.
func subagentOf(parts []string) (session string, workflow, ok bool) {
	if len(parts) < 4 || parts[2] != "subagents" {
		return "", false, false
	}
	workflow = len(parts) == 6 && parts[3] == "workflows"
	if len(parts) != 4 && !workflow {
		return "", false, false
	}
	return parts[1], workflow, true
}

// SessionCosts is the cost at API prices of each given session id, its subagent and Workflow agent transcripts
// (see subagentOf) included. It reads only those files under root (~/.claude/projects).
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
		session, workflow, sub := subagentOf(parts)
		if sub {
			owner = session
		}
		if !want[owner] {
			return nil
		}
		b := out[owner]
		b.Key = owner
		if workflow && strings.HasPrefix(parts[len(parts)-1], "agent-") {
			b.WorkflowAgents++
		}
		_ = readFile(path, func(string) bool { return false }, func(r Request) { b.add(r) }, nil, seen)
		out[owner] = b
		return nil
	})
	return out
}
