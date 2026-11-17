// Code generated from the Python recording of tests/test_worker_guard_unicode_symlink_parity.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestUnicodeSymlinkParitySymlinkWalks(t *testing.T) {
	replay(t, "test_worker_guard_unicode_symlink_parity", "test_worker_guard_unicode_symlink_parity.SymlinkTests.test_symlink_walks", true)
}

func TestUnicodeSymlinkParityUnicodeWhitespaceAndDigits(t *testing.T) {
	replay(t, "test_worker_guard_unicode_symlink_parity", "test_worker_guard_unicode_symlink_parity.UnicodeTests.test_unicode_whitespace_and_digits", false)
}
