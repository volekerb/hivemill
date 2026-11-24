// Code generated from the Python recording of tests/test_worker_guard_nopush_gh_writes.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushGhWritesIssueWorkflowPrWritesRefusedInEveryWrapper(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_issue_workflow_pr_writes_refused_in_every_wrapper", false)
}

func TestNopushGhWritesOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_other_roles_unchanged", false)
}

func TestNopushGhWritesPrCommentAllowedForReviewerAndQa(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_pr_comment_allowed_for_reviewer_and_qa", false)
}

func TestNopushGhWritesPrCommentNotInInlineScan(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_pr_comment_not_in_inline_scan", false)
}

func TestNopushGhWritesPrCommentRefusedForOtherNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_pr_comment_refused_for_other_no_push_roles", false)
}

func TestNopushGhWritesReadsAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_reads_allowed", false)
}

func TestNopushGhWritesXargsFormsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_xargs_forms_refused", false)
}
