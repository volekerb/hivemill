// Extra parity probes written for T-1689 (not a port of a committed Python module): symlink walks and Unicode whitespace/digits, recorded from the Python guard in testdata/test_worker_guard_unicode_symlink_parity.json.

package guard1

import "testing"

func TestUnicodeSymlinkParitySymlinkWalks(t *testing.T) {
	replay(t, "test_worker_guard_unicode_symlink_parity", "test_worker_guard_unicode_symlink_parity.SymlinkTests.test_symlink_walks", true)
}

func TestUnicodeSymlinkParityUnicodeWhitespaceAndDigits(t *testing.T) {
	replay(t, "test_worker_guard_unicode_symlink_parity", "test_worker_guard_unicode_symlink_parity.UnicodeTests.test_unicode_whitespace_and_digits", false)
}
