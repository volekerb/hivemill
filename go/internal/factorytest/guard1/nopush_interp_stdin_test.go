// Code generated from the Python recording of tests/test_worker_guard_nopush_interp_stdin.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushInterpStdinAllowedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_allowed_for_no_push_roles", false)
}

func TestNopushInterpStdinDashWordIsNotAStripHeredoc(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_dash_word_is_not_a_strip_heredoc", false)
}

func TestNopushInterpStdinDeniedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_denied_for_every_no_push_role", false)
}

func TestNopushInterpStdinDeniedReasonNamesTheRule(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_denied_reason_names_the_rule", false)
}

func TestNopushInterpStdinHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_hq_unchanged", false)
}

func TestNopushInterpStdinOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_stdin", "test_worker_guard_nopush_interp_stdin.InterpStdinTests.test_other_roles_unchanged", false)
}
