// Code generated from the Python recording of tests/test_worker_guard_nopush_interp_inline.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushInterpInlineAllowedForNoPushRoles(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_allowed_for_no_push_roles", false)
}

func TestNopushInterpInlineDeniedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_denied_for_every_no_push_role", false)
}

func TestNopushInterpInlineDeniedReasonNamesTheRule(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_denied_reason_names_the_rule", false)
}

func TestNopushInterpInlineHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_hq_unchanged", false)
}

func TestNopushInterpInlineOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_interp_inline", "test_worker_guard_nopush_interp_inline.InterpInlinePushTests.test_other_roles_unchanged", false)
}
