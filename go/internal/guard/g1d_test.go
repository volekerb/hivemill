// Ported from go/internal/factorytest/guard1: nopush_gitfile_test.go, nopush_interp_inline_test.go,
// nopush_interp_stdin_test.go, nopush_zsh_path_test.go and unicode_symlink_parity_test.go (T-1919). The same recorded
// ids, replayed in-process (replay_test.go). Case map: testdata/portmap/guard1d.txt.

package guard

import "testing"

// g1dNotPorted skips a Python unit case that guard1 never replayed, with the reason guard1 gave.
func g1dNotPorted(t *testing.T, pyCase, why string) {
	t.Helper()
	t.Skipf("not ported: %s - %s", pyCase, why)
}

func TestG1NopushGitfileExtractionIntoGitDirRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_extraction_into_git_dir_refused", false)
}

func TestG1NopushGitfileGitDirFormsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_git_dir_forms_refused", false)
}

func TestG1NopushGitfileGitfileEditToolsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_gitfile_edit_tools_refused", false)
}

func TestG1NopushGitfileGitfileReadAndOtherWritesAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_gitfile_read_and_other_writes_allowed", false)
}

func TestG1NopushGitfileGitfileWritesRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_gitfile_writes_refused", false)
}

func TestG1NopushGitfileHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_hq_unchanged", false)
}

func TestG1NopushGitfileListingAndPlainExtractionAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_listing_and_plain_extraction_allowed", true)
}

func TestG1NopushGitfileNameRewritingAndOtherExtractorsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_name_rewriting_and_other_extractors_refused", false)
}

func TestG1NopushGitfileNestedAndOutsideScratchpadTargetsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_nested_and_outside_scratchpad_targets_refused", false)
}

func TestG1NopushGitfileOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_other_roles_unchanged", false)
}

func TestG1NopushGitfilePythonAndArExtractorsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_python_and_ar_extractors_refused", false)
}

func TestG1NopushGitfilePythonFlagClustersAndArOptionsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_python_flag_clusters_and_ar_options_refused", false)
}

func TestG1NopushGitfileSymlinkWalkErrorAndEarlierMutationRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_symlink_walk_error_and_earlier_mutation_refused", true)
}

func TestG1NopushGitfileUnboundedExtractionRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_unbounded_extraction_refused", false)
}

func TestG1NopushGitfileWalkBoundAndGitfileBelowRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_walk_bound_and_gitfile_below_refused", true)
}

func TestG1NopushGitfileWalkBoundUnitNotPorted(t *testing.T) {
	g1dNotPorted(t, "test_walk_bound_unit", "unit test of the Python walk-bound constant; the bound is covered by test_walk_bound_and_gitfile_below_refused")
}

func TestG1NopushGitfileSymlinkWalkErrorUnitNotPorted(t *testing.T) {
	g1dNotPorted(t, "test_symlink_walk_error_unit", "unit test of a Python-internal walk error type; symlink walks are covered by the replayed cases")
}

func TestG1NopushInterpInlineAllowedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_allowed_for_no_push_roles", false)
}

func TestG1NopushInterpInlineDeniedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_denied_for_every_no_push_role", false)
}

func TestG1NopushInterpInlineDeniedReasonNamesTheRule(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_denied_reason_names_the_rule", false)
}

func TestG1NopushInterpInlineHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_hq_unchanged", false)
}

func TestG1NopushInterpInlineOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_other_roles_unchanged", false)
}

func TestG1NopushInterpStdinAllowedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_allowed_for_no_push_roles", false)
}

func TestG1NopushInterpStdinDashWordIsNotAStripHeredoc(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_dash_word_is_not_a_strip_heredoc", false)
}

func TestG1NopushInterpStdinDeniedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_denied_for_every_no_push_role", false)
}

func TestG1NopushInterpStdinDeniedReasonNamesTheRule(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_denied_reason_names_the_rule", false)
}

func TestG1NopushInterpStdinHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_hq_unchanged", false)
}

func TestG1NopushInterpStdinOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_other_roles_unchanged", false)
}

func TestG1NopushZshPathAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_zsh_path", "test_worker_guard_nopush_zsh_path.ZshPathTests.test_allowed", false)
}

func TestG1NopushZshPathDeniedPlainChainedAndInShell(t *testing.T) {
	replay(t, "test_worker_guard_nopush_zsh_path", "test_worker_guard_nopush_zsh_path.ZshPathTests.test_denied_plain_chained_and_in_shell", false)
}

func TestG1NopushZshPathHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_zsh_path", "test_worker_guard_nopush_zsh_path.ZshPathTests.test_hq_unchanged", false)
}

func TestG1NopushZshPathOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_zsh_path", "test_worker_guard_nopush_zsh_path.ZshPathTests.test_other_roles_unchanged", false)
}

func TestG1UnicodeSymlinkParitySymlinkWalks(t *testing.T) {
	replay(t, "test_worker_guard_unicode_symlink_parity", "test_worker_guard_unicode_symlink_parity.SymlinkTests.test_symlink_walks", true)
}

func TestG1UnicodeSymlinkParityUnicodeWhitespaceAndDigits(t *testing.T) {
	replay(t, "test_worker_guard_unicode_symlink_parity", "test_worker_guard_unicode_symlink_parity.UnicodeTests.test_unicode_whitespace_and_digits", false)
}
