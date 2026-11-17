// Code generated from the Python recording of tests/test_worker_guard_nopush_gitfile.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushGitfileExtractionIntoGitDirRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_extraction_into_git_dir_refused", false)
}

func TestNopushGitfileGitDirFormsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_git_dir_forms_refused", false)
}

func TestNopushGitfileGitfileEditToolsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_gitfile_edit_tools_refused", false)
}

func TestNopushGitfileGitfileReadAndOtherWritesAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_gitfile_read_and_other_writes_allowed", false)
}

func TestNopushGitfileGitfileWritesRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_gitfile_writes_refused", false)
}

func TestNopushGitfileHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_hq_unchanged", false)
}

func TestNopushGitfileListingAndPlainExtractionAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_listing_and_plain_extraction_allowed", true)
}

func TestNopushGitfileNameRewritingAndOtherExtractorsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_name_rewriting_and_other_extractors_refused", false)
}

func TestNopushGitfileNestedAndOutsideScratchpadTargetsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_nested_and_outside_scratchpad_targets_refused", false)
}

func TestNopushGitfileOtherRolesUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_other_roles_unchanged", false)
}

func TestNopushGitfilePythonAndArExtractorsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_python_and_ar_extractors_refused", false)
}

func TestNopushGitfilePythonFlagClustersAndArOptionsRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_python_flag_clusters_and_ar_options_refused", false)
}

func TestNopushGitfileSymlinkWalkErrorAndEarlierMutationRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_symlink_walk_error_and_earlier_mutation_refused", true)
}

func TestNopushGitfileUnboundedExtractionRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_unbounded_extraction_refused", false)
}

func TestNopushGitfileWalkBoundAndGitfileBelowRefused(t *testing.T) {
	replay(t, "test_worker_guard_nopush_gitfile", "test_worker_guard_nopush_gitfile.NoPushGitFileTests.test_walk_bound_and_gitfile_below_refused", true)
}
