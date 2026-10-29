"""Tests for ci/risk_tier.py: the pure tier(diff, paths, lines, rules) classifier (G-022 #1)."""
import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
_spec = importlib.util.spec_from_file_location("risk_tier_under_test", ROOT / "ci" / "risk_tier.py")
rt = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(rt)

BASE = 'SCHEMA = "a"\n\ndef cmd_inbox(a):\n    return 1\n\ndef cmd_judge(a):\n    return 1\n\ndef judge_gate(a):\n    return 1\n'


def edit(old, new):
    return BASE.replace(old, new)


class TierRuleTests(unittest.TestCase):
    def setUp(self):
        self.rules = rt.load_rules()

    def tier(self, paths, lines=10, diff=None):
        return rt.tier(diff or {}, paths, lines, self.rules)

    def test_docs_only_is_t0(self):
        self.assertEqual(self.tier(["docs/a.md", "README.md", "docs/img/x.png"], lines=900 - 400), "T0")

    def test_markdown_in_sensitive_or_excluded_dirs_is_not_docs(self):
        self.assertEqual(self.tier(["roles/coder.md"], 10), "T1")      # excluded from T0, small -> T1
        self.assertEqual(self.tier(["roles/coder.md"], 300), "T2")
        self.assertEqual(self.tier([".github/pull_request_template.md"]), "T3")

    def test_tests_only_is_t1_even_when_large(self):
        self.assertEqual(self.tier(["tests/test_a.py", "tests/fixtures/x.json"], 400), "T1")

    def test_test_infrastructure_is_not_tests_only(self):
        self.assertEqual(self.tier(["tests/harness.py"], 400), "T2")

    def test_small_diff_is_t1_and_boundary(self):
        self.assertEqual(self.tier(["web/app.py"], 150), "T1")
        self.assertEqual(self.tier(["web/app.py"], 151), "T2")

    def test_t2_between_small_and_large(self):
        self.assertEqual(self.tier(["web/app.py"], 600), "T2")

    def test_over_600_lines_is_t3(self):
        self.assertEqual(self.tier(["web/app.py"], 601), "T3")
        self.assertEqual(self.tier(["docs/a.md"], 601), "T3")

    def test_sensitive_paths_are_t3(self):
        for p in (".github/workflows/tests.yml", "ci/public_text_guard.py", "ci/scrub_scan.py", "ci/guard_diff.py",
                  "ci/test_count_gate.py", "roles/settings/coder.json.in", "roles/hooks/worker-guard.py",
                  "scout/nsbox.py", "ci/tier_rules.json"):
            self.assertEqual(self.tier([p, "docs/a.md"], 5), "T3", p)

    def test_non_sensitive_ci_file_is_not_t3(self):
        self.assertEqual(self.tier(["ci/changelog.py"], 5), "T1")

    def test_sensitive_def_in_bin_factory_is_t3(self):
        for old, new in (("return 1\n\ndef judge_gate", "return 2\n\ndef judge_gate"),
                         ("def judge_gate(a):\n    return 1", "def judge_gate(a):\n    return 2"),
                         ('SCHEMA = "a"', 'SCHEMA = "b"')):
            diff = {"bin/factory": (BASE, edit(old, new))}
            self.assertEqual(self.tier(["bin/factory"], 3, diff), "T3", old)

    def test_sensitive_def_added_is_t3(self):
        diff = {"bin/factory": (BASE, BASE + "\ndef judge_new(a):\n    return 0\n")}
        self.assertEqual(self.tier(["bin/factory"], 3, diff), "T3")

    def test_non_sensitive_def_in_bin_factory_is_not_t3(self):
        diff = {"bin/factory": (BASE, edit("def cmd_inbox(a):\n    return 1", "def cmd_inbox(a):\n    return 2"))}
        self.assertEqual(self.tier(["bin/factory"], 3, diff), "T1")
        self.assertEqual(rt.sensitive_defs(diff, self.rules), [])

    def test_unparsable_bin_factory_fails_closed(self):
        diff = {"bin/factory": (BASE, "def broken(:\n")}
        self.assertEqual(self.tier(["bin/factory"], 3, diff), "T3")

    def test_max_tier_only_goes_up(self):
        self.assertEqual(rt.max_tier("T1", "T3"), "T3")
        self.assertEqual(rt.max_tier("T3", "T0"), "T3")
        self.assertEqual(rt.max_tier(None, "T2"), "T2")
        self.assertIsNone(rt.max_tier(None, None))

    def test_broken_rules_file_is_an_error_not_a_default(self):
        with self.assertRaises(rt.TierError):
            rt.load_rules("/nonexistent/tier_rules.json")


if __name__ == "__main__":
    unittest.main()
