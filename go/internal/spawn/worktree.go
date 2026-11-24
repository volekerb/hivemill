package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

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
	// HooksGuard is git_hooks_guard (T-1544): it records the refusal and returns GitHooksRefusal's names, nil = clean.
	HooksGuard func(clone string) []string
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
	if d.ConfigGuard == nil || d.HooksGuard == nil || d.PrePushHook == nil || d.Record == nil {
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
			coderGit := s.Worktree + "/.git"
			fi, lerr := os.Lstat(coderGit)
			isLink := lerr == nil && fi.Mode()&os.ModeSymlink != 0
			isDir := lerr == nil && fi.IsDir()
			if isLink && (s.Role == "coder" || s.Role == "qa") { // git would read config and hooks from wherever it points
				return "", &WorktreeRefusal{fmt.Sprintf("spawn refused: %s is a symlink; inspect it, remove it, then spawn again", coderGit), 4}
			}
			if s.Role == "qa" && isDir {
				return "", ErrNotPorted // prepare_qa_clone (T-1544): qa gets its own clone, never the coder's; Python makes it
			}
			if s.Role == "coder" && isDir {
				if names := d.HooksGuard(s.Worktree); len(names) > 0 {
					return "", &WorktreeRefusal{fmt.Sprintf("spawn refused: %s/.git holds %s (only *.sample and the factory pre-push "+
						"are allowed in hooks/). Inspect and remove them, then spawn again.", s.Worktree, strings.Join(names, ", ")), 4}
				}
			}
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

// GitHooksRefusal is git_hooks_refusal (T-1544): the names in <clone>/.git/hooks a coder spawn must not run under,
// anything but *.sample files and a regular pre-push whose text is prePush (the body install_pre_push_hook writes).
// A symlink or a directory counts; a commondir file or a symlinked hooks/ refuses outright. nil = clean. An unreadable
// hooks/ or a non-UTF-8 hook refuses with "hooks unreadable: ..." as Python's OSError/UnicodeDecodeError branch does.
func GitHooksRefusal(clone, prePush string) []string {
	gitDir := filepath.Join(clone, ".git")
	if _, err := os.Lstat(filepath.Join(gitDir, "commondir")); err == nil { // git would take hooks/ from another directory
		return []string{"commondir"}
	}
	hooks := filepath.Join(gitDir, "hooks")
	if fi, err := os.Lstat(hooks); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return []string{"hooks (symlink)"}
	}
	if _, err := os.Stat(hooks); err != nil {
		return nil // Path.exists(): a missing hooks/ holds nothing
	}
	entries, err := os.ReadDir(hooks) // sorted by name, as sorted(iterdir()) is for one directory
	if err != nil {
		return []string{fmt.Sprintf("hooks unreadable: %v", err)}
	}
	var bad []string
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(hooks, name)
		fi, err := os.Lstat(p)
		if err != nil {
			return []string{fmt.Sprintf("hooks unreadable: %v", err)}
		}
		switch {
		case !fi.Mode().IsRegular(): // a symlink, a directory, a fifo
			bad = append(bad, name)
		case strings.HasSuffix(name, ".sample"):
		case name == "pre-push":
			b, err := os.ReadFile(p)
			if err != nil {
				return []string{fmt.Sprintf("hooks unreadable: %v", err)}
			}
			if !utf8.Valid(b) {
				return []string{"hooks unreadable: " + name + " is not UTF-8"}
			}
			// read_text() reads in universal-newline mode: \r\n and \r compare as \n
			text := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n")
			if text != prePush {
				bad = append(bad, name)
			}
		default:
			bad = append(bad, name)
		}
	}
	return bad
}
