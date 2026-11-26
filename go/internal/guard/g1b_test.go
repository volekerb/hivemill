// Ported from go/internal/factorytest/guard1: gh_new_alias_test.go, gh_repo_groups_test.go, live_remote_test.go,
// nopush_archive_git_test.go and nopush_archive_main_test.go (T-1917). The same recorded ids, replayed in-process
// (replay_test.go). Case map: testdata/portmap/guard1b.txt.

package guard

import (
	"reflect"
	"testing"
)

func TestG1GhNewAliasCreateAndReadsKeepOriginExitCodes(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_create_and_reads_keep_origin_exit_codes", false)
}

func TestG1GhNewAliasFrozenRepoTreatsNewLikeCreate(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_frozen_repo_treats_new_like_create", false)
}

func TestG1GhNewAliasIssueNewRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_issue_new_refused_for_no_push_roles", false)
}

func TestG1GhNewAliasOtherRolesKeepOriginExitCodes(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_other_roles_keep_origin_exit_codes", false)
}

func TestG1GhNewAliasPrCommentStaysAllowedForReviewerAndQa(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_pr_comment_stays_allowed_for_reviewer_and_qa", false)
}

func TestG1GhNewAliasPrNewRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_pr_new_refused_for_no_push_roles", false)
}

func TestG1GhNewAliasPublicTextTreatsNewLikeCreate(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_public_text_treats_new_like_create", false)
}

func TestG1GhNewAliasReleaseNewRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_new_alias", "test_worker_guard_gh_new_alias.GhNewAliasTests.test_release_new_refused_for_no_push_roles", false)
}

func TestG1GhRepoGroupsFrozenRepoRFirstEqualsTrailing(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_frozen_repo_r_first_equals_trailing", false)
}

func TestG1GhRepoGroupsFrozenRepoRFirstReadKeepsOriginCode(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_frozen_repo_r_first_read_keeps_origin_code", false)
}

func TestG1GhRepoGroupsInlineRepoVerbsStayRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_inline_repo_verbs_stay_refused_for_no_push_roles", false)
}

func TestG1GhRepoGroupsLabelCreateRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_label_create_refused_for_no_push_roles", false)
}

func TestG1GhRepoGroupsOtherRolesKeepOriginExitCodes(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_other_roles_keep_origin_exit_codes", false)
}

func TestG1GhRepoGroupsPublicTextRFirstEqualsTrailing(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_public_text_r_first_equals_trailing", false)
}

func TestG1GhRepoGroupsReadsKeepOriginExitCodes(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_reads_keep_origin_exit_codes", false)
}

func TestG1GhRepoGroupsRepoWritesRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_repo_writes_refused_for_no_push_roles", false)
}

func TestG1GhRepoGroupsResearcherPythonFormsOfLabelSecretVariable(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_researcher_python_forms_of_label_secret_variable", false)
}

func TestG1GhRepoGroupsSecretSetRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_secret_set_refused_for_no_push_roles", false)
}

func TestG1GhRepoGroupsVariableSetRefusedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_gh_repo_groups", "test_worker_guard_gh_repo_groups.GhRepoGroupsTests.test_variable_set_refused_for_no_push_roles", false)
}

func TestG1LiveRemoteAliasReentryKeepsCGitDirWorktree(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_alias_reentry_keeps_c_git_dir_worktree", false)
}

func TestG1LiveRemoteAliasRemoteBodyTrailingSetUrlAddBlocked(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_alias_remote_body_trailing_set_url_add_blocked", false)
}

func TestG1LiveRemoteAliasRemoteWriteBlockedLiveCommon(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_alias_remote_write_blocked_live_common", false)
}

func TestG1LiveRemoteCommonDirConfigFileBlocked(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_common_dir_config_file_blocked", false)
}

func TestG1LiveRemoteConfigCommentWorktreeValueNotWorktreeScope(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_config_comment_worktree_value_not_worktree_scope", false)
}

func TestG1LiveRemoteConfigTrailingWorktreeIsValueNotScope(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_config_trailing_worktree_is_value_not_scope", false)
}

func TestG1LiveRemotePushAliasHidingPushRefused(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_alias_hiding_push_refused", false)
}

func TestG1LiveRemotePushAliasLastCWins(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_alias_last_c_wins", false)
}

func TestG1LiveRemotePushCIncludeifPathRefused(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_c_includeif_path_refused", false)
}

func TestG1LiveRemotePushCPushinsteadofIncludePathRefused(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_c_pushinsteadof_include_path_refused", false)
}

func TestG1LiveRemotePushCRemoteUrlRefusedForWorkers(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_c_remote_url_refused_for_workers", false)
}

func TestG1LiveRemotePushCredentialedUrlRefusedTokenNotEchoed(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_credentialed_url_refused_token_not_echoed", false)
}

func TestG1LiveRemotePushExplicitFactoryUrlAllowedForCoder(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_explicit_factory_url_allowed_for_coder", false)
}

func TestG1LiveRemotePushExplicitUrlFailClosedWithoutValidFactoryRemote(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_explicit_url_fail_closed_without_valid_factory_remote", false)
}

func TestG1LiveRemotePushWrongExplicitUrlRefused(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_push_wrong_explicit_url_refused", false)
}

func TestG1LiveRemoteReadsAllowed(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_reads_allowed", false)
}

func TestG1LiveRemoteRemoteVerboseFlagsBeforeWriteBlocked(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_remote_verbose_flags_before_write_blocked", false)
}

func TestG1LiveRemoteRemoteWritesBlockedEveryRole(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_remote_writes_blocked_every_role", false)
}

func TestG1LiveRemoteRepoConfigWriteBlockedWorktreeScopeAllowed(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_repo_config_write_blocked_worktree_scope_allowed", false)
}

func TestG1LiveRemoteScratchpadCloneStillAllowsRepoConfig(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_scratchpad_clone_still_allows_repo_config", false)
}

func TestG1LiveRemoteSendPackBlockedJudgeCleaner(t *testing.T) {
	replay(t, "test_worker_guard_live_remote", "test_worker_guard_live_remote.LiveRemoteGuardTests.test_send_pack_blocked_judge_cleaner", false)
}

func TestG1NopushArchiveGitAfterCdAndInShellWrapperRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_after_cd_and_in_shell_wrapper_refused", true)
}

func TestG1NopushArchiveGitBadArchivesRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_bad_archives_refused", true)
}

func TestG1NopushArchiveGitCleanArchivesListingAndWorktreeGitAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_clean_archives_listing_and_worktree_git_allowed", true)
}

func TestG1NopushArchiveGitCoderCleanerJudgeUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_coder_cleaner_judge_unchanged", false)
}

func TestG1NopushArchiveGitConcatenatedTarIgnoreZerosRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_concatenated_tar_ignore_zeros_refused", false)
}

func TestG1NopushArchiveGitEnvInHookProcessRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_env_in_hook_process_refused", true)
}

func TestG1NopushArchiveGitEnvRuleNotForCoderAndHq(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_env_rule_not_for_coder_and_hq", false)
}

func TestG1NopushArchiveGitFrozenProbeTable(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_frozen_probe_table", true)
}

func TestG1NopushArchiveGitHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_hq_unchanged", true)
}

func TestG1NopushArchiveGitLinkIntoDotgitRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_link_into_dotgit_refused", true)
}

func TestG1NopushArchiveGitMissingArchiveAloneIsHarmless(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_missing_archive_alone_is_harmless", true)
}

func TestG1NopushArchiveGitNameRewriteOptionsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_name_rewrite_options_refused", true)
}

func TestG1NopushArchiveGitUnlistableArchiveFailsClosed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_unlistable_archive_fails_closed", true)
}

func TestG1NopushArchiveGitUnzipZipSuffixCandidatesListed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_unzip_zip_suffix_candidates_listed", true)
}

func TestG1NopushArchiveMainBenignFormsAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_main", "test_worker_guard_nopush_archive_main.ArchiveMainTests.test_benign_forms_allowed", false)
}

func TestG1NopushArchiveMainTarfileMainExtractIntoGitRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_main", "test_worker_guard_nopush_archive_main.ArchiveMainTests.test_tarfile_main_extract_into_git_refused", false)
}

func TestG1NopushArchiveMainZipfileMainExtractIntoGitRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_main", "test_worker_guard_nopush_archive_main.ArchiveMainTests.test_zipfile_main_extract_into_git_refused", false)
}

// TestG1NopushArchiveGitParseTarGrammarNotPorted keeps the guard1 name of the case that only skipped there
// (test_parse_tar_grammar unit-tests the Python parse_tar helper, which has no recording). Here it runs the rows of
// that Python test against the Go mirror, parseTar; an empty Python list is a nil slice in Go.
func TestG1NopushArchiveGitParseTarGrammarNotPorted(t *testing.T) {
	rows := []struct {
		args           []string
		extract        bool
		archives, dirs []string
		problem        bool
	}{
		{[]string{"-xf", "a.tar", "-C", "o"}, true, []string{"a.tar"}, []string{"o"}, false},
		{[]string{"xzf", "a.tar", "-C", "o"}, true, []string{"a.tar"}, []string{"o"}, false},
		{[]string{"-xzf", "a.tar", "-Co"}, true, []string{"a.tar"}, []string{"o"}, false},
		{[]string{"--extract", "--file=a.tar", "--directory", "o"}, true, []string{"a.tar"}, []string{"o"}, false},
		{[]string{"-tf", "/x/a.tar"}, false, []string{"/x/a.tar"}, nil, true},
		{[]string{"-cf", "a.tar", "x"}, true, nil, nil, true}, // c not allowlisted: unclassifiable = extract
		{[]string{"-xf/x/a.tar"}, true, nil, nil, true},       // glued value: unclassifiable = extract
		{[]string{"-xvf/x/a.tar", "-C", "o"}, true, nil, []string{"o"}, true},
		{[]string{"-xfC", "a.tar", "out"}, true, nil, nil, true}, // value letter not last
		{[]string{"xfC", "a.tar", "out"}, true, nil, nil, true},
		{[]string{"-gf", "-x", "-f", "-", "-C", "o"}, true, []string{"-"}, []string{"o"}, true}, // g is not allowlisted
		{[]string{"-Kf", "-x", "-f", "-", "-C", "o"}, true, []string{"-"}, []string{"o"}, true},
		{[]string{"-gf", "a.tar"}, true, nil, nil, true},
		{[]string{"-x2f", "a.tar"}, true, nil, nil, true}, // digit
		{[]string{"-xf", "a", "-f", "b"}, true, []string{"a", "b"}, nil, true},
		{[]string{"-xf", "a", "--", "x"}, true, []string{"a"}, nil, true},
		{[]string{"-xif", "a"}, true, nil, nil, true}, // i not allowlisted: unclassifiable
		{[]string{"--ignore-zeros", "-xf", "a"}, true, []string{"a"}, nil, true},
		{[]string{"-xf"}, true, nil, nil, true},
		{[]string{"-xf", "a", "foo", "-C", "o"}, true, []string{"a"}, []string{"o"}, true},
		{[]string{"-xf", "a", "-C", "o", "foo"}, true, []string{"a"}, []string{"o"}, false},
		{[]string{"-xf", "a", "-C", "o", "-C", ".."}, true, []string{"a"}, []string{"o", ".."}, true},
	}
	norm := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return s
	}
	for _, r := range rows {
		ex, ar, di, pr := parseTar(r.args)
		if ex != r.extract || !reflect.DeepEqual(norm(ar), r.archives) || !reflect.DeepEqual(norm(di), r.dirs) || (len(pr) > 0) != r.problem {
			t.Errorf("parseTar(%q) = %v %v %v %v; want %v %v %v problem=%v", r.args, ex, ar, di, pr, r.extract, r.archives, r.dirs, r.problem)
		}
	}
}
