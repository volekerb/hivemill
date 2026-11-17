// Code generated from the Python recording of tests/test_worker_guard_nopush_archive_main.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushArchiveMainBenignFormsAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_main", "test_worker_guard_nopush_archive_main.ArchiveMainTests.test_benign_forms_allowed", false)
}

func TestNopushArchiveMainTarfileMainExtractIntoGitRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_main", "test_worker_guard_nopush_archive_main.ArchiveMainTests.test_tarfile_main_extract_into_git_refused", false)
}

func TestNopushArchiveMainZipfileMainExtractIntoGitRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_archive_main", "test_worker_guard_nopush_archive_main.ArchiveMainTests.test_zipfile_main_extract_into_git_refused", false)
}
