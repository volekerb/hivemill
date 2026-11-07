package guard

import (
	"strings"
	"testing"
)

// T-0995 (tests/test_worker_guard_nopush_gh_writes.py): no-push roles must not run gh issue/workflow/pr writes;
// reviewer and qa keep gh pr comment.

var (
	noPushGhWriteRoles = []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"}
	ghCommentRoles     = []string{"qa", "reviewer"}
	ghNonCommentRoles  = []string{"pm", "staff-engineer", "scout", "evaluator"}

	ghWriteDenied = []string{
		"gh issue create",
		"gh issue edit 1",
		"gh workflow run x",
		"gh pr create",
		"gh pr edit 1",
		"gh pr merge 1",
		"gh repo create",
		"gh label create x",
		"gh pr create --title t --body 'herdr is stubbed in tests'",
		"gh pr create --fill",
		"gh pr create --body 'use `docker ps` and $(su -)'",
	}
	ghCommentAllowed = []string{
		"gh pr comment 1 --body x",
		"gh pr comment 11 --body 'herdr server stop is blocked'",
	}
	ghCommentDenied = []string{
		"gh pr comment 1 --body x",
		"gh pr comment 11 --body 'herdr server stop is blocked'",
	}
	ghPrReads = []string{
		"gh pr list",
		"gh pr view 1",
		"gh pr checks 1",
	}
)

func TestNoPushGhWriteVerbsDenied(t *testing.T) {
	clearTermsEnv(t)
	dir := t.TempDir()
	for _, role := range noPushGhWriteRoles {
		for _, cmd := range ghWriteDenied {
			if err := Check(cmd, dir, role, false); exitOf(err) != 2 {
				t.Errorf("%s: %q not refused", role, cmd)
			}
		}
	}
}

func TestNoPushGhCommentAllowedForReviewerQA(t *testing.T) {
	clearTermsEnv(t)
	dir := t.TempDir()
	for _, role := range ghCommentRoles {
		for _, cmd := range ghCommentAllowed {
			if err := Check(cmd, dir, role, false); err != nil {
				t.Errorf("%s: %q refused: %v", role, cmd, err)
			}
		}
	}
}

func TestNoPushGhCommentDeniedForOthers(t *testing.T) {
	clearTermsEnv(t)
	dir := t.TempDir()
	for _, role := range ghNonCommentRoles {
		for _, cmd := range ghCommentDenied {
			if err := Check(cmd, dir, role, false); exitOf(err) != 2 {
				t.Errorf("%s: %q not refused", role, cmd)
			}
		}
	}
}

func TestNoPushGhReadsStillAllowed(t *testing.T) {
	clearTermsEnv(t)
	dir := t.TempDir()
	for _, role := range noPushGhWriteRoles {
		for _, cmd := range ghPrReads {
			if err := Check(cmd, dir, role, false); err != nil {
				t.Errorf("%s: %q refused: %v", role, cmd, err)
			}
		}
	}
}

func TestNoPushGhWriteMessage(t *testing.T) {
	clearTermsEnv(t)
	dir := t.TempDir()
	err := Check("gh pr create --fill", dir, "evaluator", false)
	if err == nil || !strings.Contains(err.Error(), "gh pr create") {
		t.Fatalf("message: %v", err)
	}
	err = Check("gh pr comment 1 --body x", dir, "pm", false)
	if err == nil || !strings.Contains(err.Error(), "only reviewer and qa") {
		t.Fatalf("comment message: %v", err)
	}
}

func TestNoPushGhWriteCoderUnchanged(t *testing.T) {
	clearTermsEnv(t)
	dir := t.TempDir()
	for _, cmd := range ghWriteDenied {
		if err := Check(cmd, dir, "coder", false); err != nil {
			t.Errorf("coder: %q refused: %v", cmd, err)
		}
	}
}
