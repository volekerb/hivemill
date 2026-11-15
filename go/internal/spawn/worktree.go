package spawn

import (
	"fmt"
	"os"
	"strings"

	"factory/go/internal/gitx"
)

// WorktreeSpec is what prepare_worktree reads of the task row and its project.
type WorktreeSpec struct {
	Task, Goal, Role string
	Repo, Base       string // projects.repo_path, projects.base_branch ("" = main)
	Worktree, Branch string // tasks.worktree, tasks.branch
	TaskClones       bool   // config task_clones == "1": the clone path is not ported
	WorktreesDir     string // the WORKTREES root
}

// WorktreeDeps carries the guards Python runs around git: they are injected, never skipped (nil = ErrNotPorted).
type WorktreeDeps struct {
	Git gitx.Options
	// ConfigGuard is git_config_guard: the offending keys, nil = clean. Required before any checkout.
	ConfigGuard func(repo string) []string
	// PrePushHook is install_pre_push_hook; its error text is the refusal.
	PrePushHook func(repo string) error
	// Record is `UPDATE tasks SET worktree=?, branch=? WHERE id=?`.
	Record func(worktree, branch string) error
}

// WorktreeRefusal is a die(): Code is the exit code.
type WorktreeRefusal struct {
	Msg  string
	Code int
}

func (r *WorktreeRefusal) Error() string { return fmt.Sprintf("factory: %s (exit %d)", r.Msg, r.Code) }

// PrepareWorktree is prepare_worktree(): a coder gets a worktree on factory/<task> from origin/<base>; qa and reviewer
// reuse the coder's. "" = no worktree (no repo, or a verifier with none). The branch is never main or the base.
func PrepareWorktree(s WorktreeSpec, d WorktreeDeps) (string, error) {
	if s.Task == "" || s.Repo == "" {
		return "", nil
	}
	if d.ConfigGuard == nil || d.PrePushHook == nil || d.Record == nil {
		return "", ErrNotPorted
	}
	base := s.Base
	if base == "" {
		base = "main"
	}
	branch := s.Branch
	if branch == "" {
		branch = "factory/" + pyLower(s.Task)
	}
	if s.Worktree != "" {
		if _, err := os.Stat(s.Worktree); err == nil {
			if s.Role == "coder" && s.Branch != "" {
				if keys := d.ConfigGuard(s.Worktree); len(keys) > 0 {
					return "", &WorktreeRefusal{fmt.Sprintf("spawn refused: git config in %s would run code: %s", s.Worktree, strings.Join(keys, ", ")), 4}
				}
				if _, err := gitx.Run(s.Worktree, d.Git, "switch", "-q", s.Branch); err != nil {
					return "", fmt.Errorf("git switch in %s: %w", s.Worktree, err)
				}
			}
			if err := d.PrePushHook(s.Worktree); err != nil {
				return "", &WorktreeRefusal{err.Error(), 4}
			}
			return s.Worktree, nil
		}
	}
	if s.Role != "coder" {
		return "", nil // verifiers never create branches
	}
	if branch == base || branch == "main" || branch == "master" || branch == "origin/"+base {
		return "", &WorktreeRefusal{fmt.Sprintf("spawn refused: the task branch %s is the base branch; a coder never works on it", branch), 4}
	}
	if s.TaskClones {
		return "", ErrNotPorted
	}
	if keys := d.ConfigGuard(s.Repo); len(keys) > 0 {
		return "", &WorktreeRefusal{fmt.Sprintf("spawn refused: git config in %s would run code: %s", s.Repo, strings.Join(keys, ", ")), 4}
	}
	wt := s.WorktreesDir + "/" + s.Goal + "-" + s.Task
	if _, err := gitx.Run(s.Repo, d.Git, "fetch", "-q", "origin"); err != nil {
		return "", fmt.Errorf("git fetch in %s: %w", s.Repo, err)
	}
	rc, _, _ := gitx.RC(s.Repo, d.Git, "rev-parse", "--verify", "-q", "origin/"+base)
	start := base
	if rc == 0 {
		start = "origin/" + base
	}
	brc, _, _ := gitx.RC(s.Repo, d.Git, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	branchExisted := brc == 0
	p, err := gitx.Run(s.Repo, d.Git, "worktree", "add", "--no-checkout", "-q", "-B", branch, wt, start)
	if err != nil {
		return "", fmt.Errorf("git worktree add: %w", err)
	}
	if p.ExitCode != 0 {
		return "", &WorktreeRefusal{"worktree add failed: " + strings.TrimSpace(p.Stderr), 1}
	}
	drop := func() { // only a worktree created in this call, never a pre-existing one
		_, _ = gitx.Run(s.Repo, d.Git, "worktree", "remove", "--force", wt) // best-effort cleanup; the refusal below is the result
		_, _ = gitx.Run(s.Repo, d.Git, "worktree", "prune")                 // same
		if !branchExisted {
			_, _ = gitx.Run(s.Repo, d.Git, "branch", "-D", branch) // same
		}
	}
	if keys := d.ConfigGuard(wt); len(keys) > 0 { // an includeIf scoped to worktrees/** only applies once it exists
		drop()
		return "", &WorktreeRefusal{fmt.Sprintf("spawn refused: git config in %s would run code: %s", wt, strings.Join(keys, ", ")), 4}
	}
	if p, err = gitx.Run(wt, d.Git, "checkout", "-q", "-f"); err != nil || p.ExitCode != 0 {
		drop()
		if err != nil {
			return "", fmt.Errorf("git checkout in %s: %w", wt, err)
		}
		return "", &WorktreeRefusal{"worktree checkout failed: " + strings.TrimSpace(p.Stderr), 1}
	}
	if err := d.PrePushHook(wt); err != nil {
		return "", &WorktreeRefusal{err.Error(), 4}
	}
	if err := d.Record(wt, branch); err != nil {
		return "", fmt.Errorf("record worktree of %s: %w", s.Task, err)
	}
	return wt, nil
}
