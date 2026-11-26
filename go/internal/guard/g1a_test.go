// Ported from go/internal/factorytest/guard1: env_split_string_test.go, env_value_options_test.go and
// flags_read_test.go (T-1916). The same recorded ids, replayed in-process (replay_test.go).
// Case map: testdata/portmap/guard1a.txt.

package guard

import "testing"

func TestG1EnvSplitStringAllowForms(t *testing.T) {
	replay(t, "test_worker_guard_env_split_string", "test_worker_guard_env_split_string.EnvSplitStringTests.test_allow_forms", false)
}

func TestG1EnvSplitStringDenyAllRolesAndPrefixes(t *testing.T) {
	replay(t, "test_worker_guard_env_split_string", "test_worker_guard_env_split_string.EnvSplitStringTests.test_deny_all_roles_and_prefixes", false)
}

func TestG1EnvSplitStringDenyForms(t *testing.T) {
	replay(t, "test_worker_guard_env_split_string", "test_worker_guard_env_split_string.EnvSplitStringTests.test_deny_forms", false)
}

func TestG1EnvSplitStringMovedOutOfCorpus(t *testing.T) {
	replay(t, "test_worker_guard_env_split_string", "test_worker_guard_env_split_string.EnvSplitStringTests.test_moved_out_of_corpus", false)
}

func TestG1EnvValueOptionsAllowForms(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.EnvValueOptionTests.test_allow_forms", false)
}

func TestG1EnvValueOptionsDenyForms(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.EnvValueOptionTests.test_deny_forms", false)
}

func TestG1EnvValueOptionsRegression00Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_00_coder", false)
}

func TestG1EnvValueOptionsRegression01Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_01_coder", false)
}

func TestG1EnvValueOptionsRegression02Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_02_coder", false)
}

func TestG1EnvValueOptionsRegression03Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_03_coder", false)
}

func TestG1EnvValueOptionsRegression04Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_04_coder", false)
}

func TestG1EnvValueOptionsRegression05Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_05_coder", false)
}

func TestG1EnvValueOptionsRegression06Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_06_coder", false)
}

func TestG1EnvValueOptionsRegression07Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_07_coder", false)
}

func TestG1EnvValueOptionsRegression08Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_08_coder", false)
}

func TestG1EnvValueOptionsRegression09Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_09_coder", false)
}

func TestG1EnvValueOptionsRegression10Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_10_coder", false)
}

func TestG1EnvValueOptionsRegression11Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_11_coder", false)
}

func TestG1EnvValueOptionsRegression12Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_12_coder", false)
}

func TestG1EnvValueOptionsRegression13Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_13_coder", false)
}

func TestG1EnvValueOptionsRegression14Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_14_coder", false)
}

func TestG1EnvValueOptionsRegression15Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_15_coder", false)
}

func TestG1EnvValueOptionsRegression16Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_16_coder", false)
}

func TestG1EnvValueOptionsRegression17Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_17_coder", false)
}

func TestG1EnvValueOptionsRegression18Coder(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_18_coder", false)
}

func TestG1EnvValueOptionsRegression19Qa(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_19_qa", false)
}

func TestG1EnvValueOptionsRegression20Qa(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_20_qa", false)
}

func TestG1EnvValueOptionsRegression21Qa(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_21_qa", false)
}

func TestG1EnvValueOptionsRegression22Qa(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_22_qa", false)
}

func TestG1EnvValueOptionsRegression23Qa(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_23_qa", false)
}

func TestG1EnvValueOptionsRegression24Qa(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_24_qa", false)
}

func TestG1EnvValueOptionsRegression25Qa(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_25_qa", false)
}

func TestG1EnvValueOptionsRegression26Qa(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_26_qa", false)
}

func TestG1EnvValueOptionsRegression27Reviewer(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_27_reviewer", false)
}

func TestG1EnvValueOptionsRegression28Reviewer(t *testing.T) {
	replay(t, "test_worker_guard_env_value_options", "test_worker_guard_env_value_options.T0826RegressionTests.test_regression_28_reviewer", false)
}

func TestG1FlagsReadFlagsWordElsewhereAllowed(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_flags_word_elsewhere_allowed", false)
}

func TestG1FlagsReadHqUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_hq_unchanged", false)
}

func TestG1FlagsReadStaffEngineerUnchanged(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_staff_engineer_unchanged", false)
}

func TestG1FlagsReadTempHomeAllowed(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_temp_home_allowed", false)
}

func TestG1FlagsReadWorkersBlocked(t *testing.T) {
	replay(t, "test_worker_guard_flags_read", "test_worker_guard_flags_read.FlagsReadTests.test_workers_blocked", false)
}
