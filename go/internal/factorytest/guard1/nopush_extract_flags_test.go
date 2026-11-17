// Code generated from the Python recording of tests/test_worker_guard_nopush_extract_flags.py (T-1689); DO NOT EDIT by hand.

package guard1

import "testing"

func TestNopushExtractFlagsB1EmptyLongOptionValue(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_b1_empty_long_option_value", true)
}

func TestNopushExtractFlagsB1ExplicitStdinProgramNonPython(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_b1_explicit_stdin_program_non_python", false)
}

func TestNopushExtractFlagsB2UnicodeWhitespaceBeforeParen(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_b2_unicode_whitespace_before_paren", false)
}

func TestNopushExtractFlagsB2VersionAndHelpRunNoProgram(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_b2_version_and_help_run_no_program", true)
}

func TestNopushExtractFlagsF1F2BenignNeighbours(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f1_f2_benign_neighbours", false)
}

func TestNopushExtractFlagsF1ShortOptionValuesBeforeM(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f1_short_option_values_before_m", false)
}

func TestNopushExtractFlagsF2LongOptionsBeforeM(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f2_long_options_before_m", false)
}

func TestNopushExtractFlagsF3ArResponseFile(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f3_ar_response_file", false)
}

func TestNopushExtractFlagsF4PythonCExtraction(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f4_python_c_extraction", false)
}

func TestNopushExtractFlagsF5BenignOptionValuesAllowed(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f5_benign_option_values_allowed", true)
}

func TestNopushExtractFlagsF5InlineProgramWithoutOption(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f5_inline_program_without_option", false)
}

func TestNopushExtractFlagsF5InlineWriterBeforeExtraction(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_f5_inline_writer_before_extraction", true)
}

func TestNopushExtractFlagsNonPythonOptionFormsNotTightened(t *testing.T) {
	replay(t, "test_worker_guard_nopush_extract_flags", "test_worker_guard_nopush_extract_flags.ExtractFlagTests.test_non_python_option_forms_not_tightened", true)
}
