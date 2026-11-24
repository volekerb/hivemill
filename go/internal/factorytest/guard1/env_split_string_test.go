// Code generated from the Python recording of tests/test_worker_guard_env_split_string.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestEnvSplitStringAllowForms(t *testing.T) {
	replay(t, "test_worker_guard_env_split_string", "test_worker_guard_env_split_string.EnvSplitStringTests.test_allow_forms", false)
}

func TestEnvSplitStringDenyAllRolesAndPrefixes(t *testing.T) {
	replay(t, "test_worker_guard_env_split_string", "test_worker_guard_env_split_string.EnvSplitStringTests.test_deny_all_roles_and_prefixes", false)
}

func TestEnvSplitStringDenyForms(t *testing.T) {
	replay(t, "test_worker_guard_env_split_string", "test_worker_guard_env_split_string.EnvSplitStringTests.test_deny_forms", false)
}

func TestEnvSplitStringMovedOutOfCorpus(t *testing.T) {
	replay(t, "test_worker_guard_env_split_string", "test_worker_guard_env_split_string.EnvSplitStringTests.test_moved_out_of_corpus", false)
}
