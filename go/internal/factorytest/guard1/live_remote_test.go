// Code generated from the Python recording of tests/test_worker_guard_live_remote.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestLiveRemoteAliasReentryKeepsCGitDirWorktree(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_alias_reentry_keeps_c_git_dir_worktree", false)
}

func TestLiveRemoteAliasRemoteBodyTrailingSetUrlAddBlocked(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_alias_remote_body_trailing_set_url_add_blocked", false)
}

func TestLiveRemoteAliasRemoteWriteBlockedLiveCommon(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_alias_remote_write_blocked_live_common", false)
}

func TestLiveRemoteCommonDirConfigFileBlocked(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_common_dir_config_file_blocked", false)
}

func TestLiveRemoteConfigCommentWorktreeValueNotWorktreeScope(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_config_comment_worktree_value_not_worktree_scope", false)
}

func TestLiveRemoteConfigTrailingWorktreeIsValueNotScope(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_config_trailing_worktree_is_value_not_scope", false)
}

func TestLiveRemotePushAliasHidingPushRefused(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_alias_hiding_push_refused", false)
}

func TestLiveRemotePushAliasLastCWins(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_alias_last_c_wins", false)
}

func TestLiveRemotePushCIncludeifPathRefused(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_c_includeif_path_refused", false)
}

func TestLiveRemotePushCPushinsteadofIncludePathRefused(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_c_pushinsteadof_include_path_refused", false)
}

func TestLiveRemotePushCRemoteUrlRefusedForWorkers(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_c_remote_url_refused_for_workers", false)
}

func TestLiveRemotePushCredentialedUrlRefusedTokenNotEchoed(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_credentialed_url_refused_token_not_echoed", false)
}

func TestLiveRemotePushExplicitFactoryUrlAllowedForCoder(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_explicit_factory_url_allowed_for_coder", false)
}

func TestLiveRemotePushExplicitUrlFailClosedWithoutValidFactoryRemote(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_explicit_url_fail_closed_without_valid_factory_remote", false)
}

func TestLiveRemotePushWrongExplicitUrlRefused(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_wrong_explicit_url_refused", false)
}

func TestLiveRemoteReadsAllowed(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_reads_allowed", false)
}

func TestLiveRemoteRemoteVerboseFlagsBeforeWriteBlocked(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_remote_verbose_flags_before_write_blocked", false)
}

func TestLiveRemoteRemoteWritesBlockedEveryRole(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_remote_writes_blocked_every_role", false)
}

func TestLiveRemoteRepoConfigWriteBlockedWorktreeScopeAllowed(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_repo_config_write_blocked_worktree_scope_allowed", false)
}

func TestLiveRemoteScratchpadCloneStillAllowsRepoConfig(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_scratchpad_clone_still_allows_repo_config", false)
}

func TestLiveRemoteSendPackBlockedJudgeCleaner(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_send_pack_blocked_judge_cleaner", false)
}
