// Code generated from the Python recording of tests/test_worker_guard_gh_new_alias.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestGhNewAliasCreateAndReadsKeepOriginExitCodes(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_create_and_reads_keep_origin_exit_codes", false)
}

func TestGhNewAliasFrozenRepoTreatsNewLikeCreate(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_frozen_repo_treats_new_like_create", false)
}

func TestGhNewAliasIssueNewRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_issue_new_refused_for_no_push_roles", false)
}

func TestGhNewAliasOtherRolesKeepOriginExitCodes(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_other_roles_keep_origin_exit_codes", false)
}

func TestGhNewAliasPrCommentStaysAllowedForReviewerAndQa(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_pr_comment_stays_allowed_for_reviewer_and_qa", false)
}

func TestGhNewAliasPrNewRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_pr_new_refused_for_no_push_roles", false)
}

func TestGhNewAliasPublicTextTreatsNewLikeCreate(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_public_text_treats_new_like_create", false)
}

func TestGhNewAliasReleaseNewRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_release_new_refused_for_no_push_roles", false)
}
