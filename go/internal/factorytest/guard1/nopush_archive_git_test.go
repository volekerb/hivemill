// Code generated from the Python recording of tests/test_worker_guard_nopush_archive_git.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushArchiveGitAfterCdAndInShellWrapperRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_after_cd_and_in_shell_wrapper_refused", true)
}

func TestNopushArchiveGitBadArchivesRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_bad_archives_refused", true)
}

func TestNopushArchiveGitCleanArchivesListingAndWorktreeGitAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_clean_archives_listing_and_worktree_git_allowed", true)
}

func TestNopushArchiveGitCoderCleanerJudgeUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_coder_cleaner_judge_unchanged", false)
}

func TestNopushArchiveGitConcatenatedTarIgnoreZerosRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_concatenated_tar_ignore_zeros_refused", false)
}

func TestNopushArchiveGitEnvInHookProcessRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_env_in_hook_process_refused", true)
}

func TestNopushArchiveGitEnvRuleNotForCoderAndHq(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_env_rule_not_for_coder_and_hq", false)
}

func TestNopushArchiveGitFrozenProbeTable(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_frozen_probe_table", true)
}

func TestNopushArchiveGitHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_hq_unchanged", true)
}

func TestNopushArchiveGitLinkIntoDotgitRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_link_into_dotgit_refused", true)
}

func TestNopushArchiveGitMissingArchiveAloneIsHarmless(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_missing_archive_alone_is_harmless", true)
}

func TestNopushArchiveGitNameRewriteOptionsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_name_rewrite_options_refused", true)
}

func TestNopushArchiveGitUnlistableArchiveFailsClosed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_unlistable_archive_fails_closed", true)
}

func TestNopushArchiveGitUnzipZipSuffixCandidatesListed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_git", "test_worker_guard_nopush_archive_git.ArchiveGitTests.test_unzip_zip_suffix_candidates_listed", true)
}

// notPorted keeps the Python case count visible: the case tests a Python-internal API with no Go counterpart.
func notPorted(t *testing.T, pyCase, why string) {
	t.Helper()
	t.Skipf("not ported: %s - %s", pyCase, why)
}

func TestNopushArchiveGitParseTarGrammarNotPorted(t *testing.T) {
	notPorted(t, "test_parse_tar_grammar", "unit test of the Python _parse_tar helper; the tar grammar is covered end to end by the replayed cases")
}
