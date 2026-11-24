// Code generated from the Python recording of tests/test_worker_guard_nopush_env_unset.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushEnvUnsetAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_unset", "test_worker_guard_nopush_env_unset.EnvUnsetTests.test_allowed", false)
}

func TestNopushEnvUnsetDeniedPlainChainedAndInShell(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_unset", "test_worker_guard_nopush_env_unset.EnvUnsetTests.test_denied_plain_chained_and_in_shell", false)
}

func TestNopushEnvUnsetHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_unset", "test_worker_guard_nopush_env_unset.EnvUnsetTests.test_hq_unchanged", false)
}

func TestNopushEnvUnsetOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_unset", "test_worker_guard_nopush_env_unset.EnvUnsetTests.test_other_roles_unchanged", false)
}
