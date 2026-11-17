// Code generated from the Python recording of tests/test_worker_guard_flags_read.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestFlagsReadFlagsWordElsewhereAllowed(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_flags_word_elsewhere_allowed", false)
}

func TestFlagsReadHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_hq_unchanged", false)
}

func TestFlagsReadStaffEngineerUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_staff_engineer_unchanged", false)
}

func TestFlagsReadTempHomeAllowed(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_temp_home_allowed", false)
}

func TestFlagsReadWorkersBlocked(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_workers_blocked", false)
}
