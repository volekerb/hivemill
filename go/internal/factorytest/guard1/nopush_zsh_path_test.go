// Code generated from the Python recording of tests/test_worker_guard_nopush_zsh_path.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushZshPathAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_zsh_path", "test_worker_guard_nopush_zsh_path.ZshPathTests.test_allowed", false)
}

func TestNopushZshPathDeniedPlainChainedAndInShell(t *testing.T) {
	replay(t, "test_worker_guard_nopush_zsh_path", "test_worker_guard_nopush_zsh_path.ZshPathTests.test_denied_plain_chained_and_in_shell", false)
}

func TestNopushZshPathHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_zsh_path", "test_worker_guard_nopush_zsh_path.ZshPathTests.test_hq_unchanged", false)
}

func TestNopushZshPathOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_zsh_path", "test_worker_guard_nopush_zsh_path.ZshPathTests.test_other_roles_unchanged", false)
}
