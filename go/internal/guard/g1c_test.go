// Ported from go/internal/factorytest/guard1: nopush_builtin_assign_test.go, nopush_env_prefix_zsh_modifiers_test.go,
// nopush_env_unset_test.go, nopush_extract_flags_test.go, nopush_gh_api_release_test.go, nopush_gh_writes_test.go and
// nopush_gitcfg_test.go (T-1918). The same recorded ids, replayed in-process (replay_test.go).
// Case map: testdata/portmap/guard1c.txt.

package guard

import "testing"

func TestG1NopushBuiltinAssignAllowedNamesOutsideProtectedSet(t *testing.T) {
	replay(t, "test_worker_guard_nopush_builtin_assign", "test_worker_guard_nopush_builtin_assign.BuiltinAssignTests.test_allowed_names_outside_protected_set", false)
}

func TestG1NopushBuiltinAssignDeniedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_builtin_assign", "test_worker_guard_nopush_builtin_assign.BuiltinAssignTests.test_denied_for_every_no_push_role", false)
}

func TestG1NopushBuiltinAssignOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_builtin_assign", "test_worker_guard_nopush_builtin_assign.BuiltinAssignTests.test_other_roles_unchanged", false)
}

func TestG1NopushEnvPrefixZshModifiersAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_allowed", false)
}

func TestG1NopushEnvPrefixZshModifiersDenied(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_denied", false)
}

func TestG1NopushEnvPrefixZshModifiersRegressionFormsRefusedAsOnMain(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_regression_forms_refused_as_on_main", false)
}

func TestG1NopushEnvPrefixZshModifiersUnsetOfOtherVarThenCommandAllowedForCoder(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_unset_of_other_var_then_command_allowed_for_coder", false)
}

func TestG1NopushEnvPrefixZshModifiersValueOptionThenCommandAllowedForCoder(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_value_option_then_command_allowed_for_coder", false)
}

func TestG1NopushEnvUnsetAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_unset", "test_worker_guard_nopush_env_unset.EnvUnsetTests.test_allowed", false)
}

func TestG1NopushEnvUnsetDeniedPlainChainedAndInShell(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_unset", "test_worker_guard_nopush_env_unset.EnvUnsetTests.test_denied_plain_chained_and_in_shell", false)
}

func TestG1NopushEnvUnsetHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_unset", "test_worker_guard_nopush_env_unset.EnvUnsetTests.test_hq_unchanged", false)
}

func TestG1NopushEnvUnsetOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_unset", "test_worker_guard_nopush_env_unset.EnvUnsetTests.test_other_roles_unchanged", false)
}

func TestG1NopushExtractFlagsB1EmptyLongOptionValue(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_b1_empty_long_option_value", true)
}

func TestG1NopushExtractFlagsB1ExplicitStdinProgramNonPython(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_b1_explicit_stdin_program_non_python", false)
}

func TestG1NopushExtractFlagsB2UnicodeWhitespaceBeforeParen(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_b2_unicode_whitespace_before_paren", false)
}

func TestG1NopushExtractFlagsB2VersionAndHelpRunNoProgram(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_b2_version_and_help_run_no_program", true)
}

func TestG1NopushExtractFlagsF1F2BenignNeighbours(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f1_f2_benign_neighbours", false)
}

func TestG1NopushExtractFlagsF1ShortOptionValuesBeforeM(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f1_short_option_values_before_m", false)
}

func TestG1NopushExtractFlagsF2LongOptionsBeforeM(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f2_long_options_before_m", false)
}

func TestG1NopushExtractFlagsF3ArResponseFile(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f3_ar_response_file", false)
}

func TestG1NopushExtractFlagsF4PythonCExtraction(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f4_python_c_extraction", false)
}

func TestG1NopushExtractFlagsF5BenignOptionValuesAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f5_benign_option_values_allowed", true)
}

func TestG1NopushExtractFlagsF5InlineProgramWithoutOption(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f5_inline_program_without_option", false)
}

func TestG1NopushExtractFlagsF5InlineWriterBeforeExtraction(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f5_inline_writer_before_extraction", true)
}

func TestG1NopushExtractFlagsNonPythonOptionFormsNotTightened(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_non_python_option_forms_not_tightened", true)
}

func TestG1NopushGhApiReleaseCoderCleanerJudgeUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_api_release", "test_worker_guard_nopush_gh_api_release.NoPushGhApiReleaseTests.test_coder_cleaner_judge_unchanged", false)
}

func TestG1NopushGhApiReleaseDeniedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_api_release", "test_worker_guard_nopush_gh_api_release.NoPushGhApiReleaseTests.test_denied_for_every_no_push_role", false)
}

func TestG1NopushGhApiReleaseHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_api_release", "test_worker_guard_nopush_gh_api_release.NoPushGhApiReleaseTests.test_hq_unchanged", false)
}

func TestG1NopushGhApiReleaseReadsStayAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_api_release", "test_worker_guard_nopush_gh_api_release.NoPushGhApiReleaseTests.test_reads_stay_allowed", false)
}

func TestG1NopushGhApiReleaseReasonNamesTheRule(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_api_release", "test_worker_guard_nopush_gh_api_release.NoPushGhApiReleaseTests.test_reason_names_the_rule", false)
}

func TestG1NopushGhWritesIssueWorkflowPrWritesRefusedInEveryWrapper(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_issue_workflow_pr_writes_refused_in_every_wrapper", false)
}

func TestG1NopushGhWritesOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_other_roles_unchanged", false)
}

func TestG1NopushGhWritesPrCommentAllowedForReviewerAndQa(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_pr_comment_allowed_for_reviewer_and_qa", false)
}

func TestG1NopushGhWritesPrCommentNotInInlineScan(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_pr_comment_not_in_inline_scan", false)
}

func TestG1NopushGhWritesPrCommentRefusedForOtherNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_pr_comment_refused_for_other_no_push_roles", false)
}

func TestG1NopushGhWritesReadsAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_reads_allowed", false)
}

func TestG1NopushGhWritesXargsFormsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gh_writes", "test_worker_guard_nopush_gh_writes.NoPushGhWriteTests.test_xargs_forms_refused", false)
}

func TestG1NopushGitcfgEditToolsPlainScratchWriteAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_edit_tools_plain_scratch_write_allowed", false)
}

func TestG1NopushGitcfgEditToolsRefusedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_edit_tools_refused_for_every_no_push_role", false)
}

func TestG1NopushGitcfgGitInitWithCOrTemplateRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_git_init_with_c_or_template_refused", false)
}

func TestG1NopushGitcfgHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_hq_unchanged", false)
}

func TestG1NopushGitcfgOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_other_roles_unchanged", false)
}

func TestG1NopushGitcfgReadsAndPlainScratchWritesAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_reads_and_plain_scratch_writes_allowed", false)
}

func TestG1NopushGitcfgWritesRefusedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_writes_refused_for_every_no_push_role", false)
}
