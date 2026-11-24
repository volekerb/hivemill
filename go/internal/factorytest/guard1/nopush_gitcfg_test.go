// Code generated from the Python recording of tests/test_worker_guard_nopush_gitcfg.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushGitcfgEditToolsPlainScratchWriteAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_edit_tools_plain_scratch_write_allowed", false)
}

func TestNopushGitcfgEditToolsRefusedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_edit_tools_refused_for_every_no_push_role", false)
}

func TestNopushGitcfgGitInitWithCOrTemplateRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_git_init_with_c_or_template_refused", false)
}

func TestNopushGitcfgHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_hq_unchanged", false)
}

func TestNopushGitcfgOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_other_roles_unchanged", false)
}

func TestNopushGitcfgReadsAndPlainScratchWritesAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_reads_and_plain_scratch_writes_allowed", false)
}

func TestNopushGitcfgWritesRefusedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitcfg", "test_worker_guard_nopush_gitcfg.NoPushGitCfgTests.test_writes_refused_for_every_no_push_role", false)
}
