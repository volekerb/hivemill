// Code generated from the Python recording of tests/test_worker_guard_nopush_env_prefix_zsh_modifiers.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushEnvPrefixZshModifiersAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_allowed", false)
}

func TestNopushEnvPrefixZshModifiersDenied(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_denied", false)
}

func TestNopushEnvPrefixZshModifiersRegressionFormsRefusedAsOnMain(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_regression_forms_refused_as_on_main", false)
}

func TestNopushEnvPrefixZshModifiersUnsetOfOtherVarThenCommandAllowedForCoder(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_unset_of_other_var_then_command_allowed_for_coder", false)
}

func TestNopushEnvPrefixZshModifiersValueOptionThenCommandAllowedForCoder(t *testing.T) {
	replay(t, "test_worker_guard_nopush_env_prefix_zsh_modifiers", "test_worker_guard_nopush_env_prefix_zsh_modifiers.EnvPrefixZshModifierTests.test_value_option_then_command_allowed_for_coder", false)
}
