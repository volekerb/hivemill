"""T-0642: the role files and skills say local = fast, CI = full, and none tells an agent to run the full suite."""
import glob
import os
import re
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FILES = ([os.path.join(ROOT, "roles", r + ".md") for r in ("coder", "reviewer", "qa")]
         + glob.glob(os.path.join(ROOT, "roles", "plugins", "coder", "**", "*.md"), recursive=True)
         + glob.glob(os.path.join(ROOT, "roles", "plugins", "reviewer", "**", "*.md"), recursive=True))
# a discover with no single-module -p pattern, or a bare `-m unittest`
FULL_RUN = re.compile(r"-m unittest(?: -v)?(?:`|\s*$)|-m unittest discover(?!.*-p test_[\w<>]+\.py)[^`\n]*`")


def read(p):
    with open(p, encoding="utf-8") as f:
        return f.read()


class LocalFastPolicy(unittest.TestCase):
    def test_no_role_file_or_skill_runs_the_full_suite(self):
        for p in FILES:
            with self.subTest(file=os.path.relpath(p, ROOT)):
                self.assertIsNone(FULL_RUN.search(read(p)), "tells the agent to run the full suite locally")
                self.assertNotIn("judge's local gate", read(p))

    def test_each_role_has_the_rule(self):
        for role in ("coder", "reviewer", "qa"):
            text = read(os.path.join(ROOT, "roles", role + ".md"))
            with self.subTest(role=role):
                self.assertIn("ci/affected.py --base origin/main --quick --run", text)
                self.assertIn("GIT_", text)
                self.assertIn("gh pr checks", text if role != "coder" else read(
                    os.path.join(ROOT, "roles/plugins/coder/coder-skills/skills/ci-wait/SKILL.md")))
                self.assertIn("CI", text)

    def test_ci_wait_skill_reads_failures_with_log_failed(self):
        text = read(os.path.join(ROOT, "roles/plugins/coder/coder-skills/skills/ci-wait/SKILL.md"))
        self.assertIn("gh run view <run-id> --log-failed", text)
        self.assertIn("GIT_*", text)

    def test_testing_doc_exists_and_names_the_guard_and_gate_key(self):
        text = read(os.path.join(ROOT, "docs", "testing.md"))
        for needle in ("--quick", "gate_fast_projects", "worker-guard", "L-050"):
            self.assertIn(needle, text)


if __name__ == "__main__":
    unittest.main()
