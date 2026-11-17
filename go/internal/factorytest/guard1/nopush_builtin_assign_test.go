// Code generated from the Python recording of tests/test_worker_guard_nopush_builtin_assign.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushBuiltinAssignAllowedNamesOutsideProtectedSet(t *testing.T) {
	replay(t, "test_worker_guard_nopush_builtin_assign", "test_worker_guard_nopush_builtin_assign.BuiltinAssignTests.test_allowed_names_outside_protected_set", false)
}

func TestNopushBuiltinAssignDeniedForEveryNoPushRole(t *testing.T) {
	replay(t, "test_worker_guard_nopush_builtin_assign", "test_worker_guard_nopush_builtin_assign.BuiltinAssignTests.test_denied_for_every_no_push_role", false)
}

func TestNopushBuiltinAssignOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_builtin_assign", "test_worker_guard_nopush_builtin_assign.BuiltinAssignTests.test_other_roles_unchanged", false)
}
