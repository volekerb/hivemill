"""tests.yml concurrency: every push to main keeps its own completed run; PR runs still cancel older ones."""
import re
import unittest
from pathlib import Path

WORKFLOW = Path(__file__).resolve().parent.parent / ".github" / "workflows" / "tests.yml"


def _concurrency():
    m = re.search(r"^concurrency:\n((?:[ ]+.*\n|\n)+)", WORKFLOW.read_text(), re.M)
    assert m, "tests.yml has no concurrency block"
    group = re.search(r'^\s+group:\s*"?(.*?)"?\s*$', m.group(1), re.M).group(1)
    cancel = re.search(r'^\s+cancel-in-progress:\s*"?(.*?)"?\s*$', m.group(1), re.M).group(1)
    return group, cancel


class ConcurrencyPolicy(unittest.TestCase):
    def test_push_group_is_per_sha(self):
        group, _ = _concurrency()
        self.assertIn("github.event_name == 'push' && github.sha", group)

    def test_pr_runs_share_group_by_number(self):
        group, _ = _concurrency()
        self.assertIn("github.event.pull_request.number", group)

    def test_push_runs_not_cancelled_and_pr_runs_are(self):
        _, cancel = _concurrency()
        self.assertEqual(cancel, "${{ github.event_name != 'push' }}")


if __name__ == "__main__":
    unittest.main()
