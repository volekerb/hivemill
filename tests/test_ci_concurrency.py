"""tests.yml and install.yml concurrency (T-1592): all pushes to main share one group, so a newer merge supersedes an
older *pending* main run; a running main run is never cancelled; PR runs still cancel older ones of the same PR."""
import re
import unittest
from pathlib import Path

WORKFLOWS = Path(__file__).resolve().parent.parent / ".github" / "workflows"
WORKFLOW = WORKFLOWS / "tests.yml"


def _concurrency(path=WORKFLOW):
    m = re.search(r"^concurrency:\n((?:[ ]+.*\n|\n)+)", path.read_text(), re.M)
    assert m, f"{path.name} has no concurrency block"
    group = re.search(r'^\s+group:\s*"?(.*?)"?\s*$', m.group(1), re.M).group(1)
    cancel = re.search(r'^\s+cancel-in-progress:\s*"?(.*?)"?\s*$', m.group(1), re.M).group(1)
    return group, cancel


class ConcurrencyPolicy(unittest.TestCase):
    def test_push_group_is_per_sha(self):
        """Name kept from the per-sha days (tests are never renamed). Since T-1592 the push group is NOT per sha: every
        push to main evaluates to the same constant group, and nothing run-specific may enter the push branch."""
        for name, prefix in (("tests.yml", "tests"), ("install.yml", "install")):
            with self.subTest(workflow=name):
                group, _ = _concurrency(WORKFLOWS / name)
                self.assertEqual(group, prefix + "-${{ github.event_name == 'push' && 'main' || "
                                                 "github.event.pull_request.number || github.ref }}")
                self.assertNotIn("github.sha", group)
                self.assertNotIn("github.run_id", group)

    def test_pr_runs_share_group_by_number(self):
        group, _ = _concurrency()
        self.assertIn("github.event.pull_request.number", group)

    def test_push_runs_not_cancelled_and_pr_runs_are(self):
        for name in ("tests.yml", "install.yml"):
            with self.subTest(workflow=name):
                _, cancel = _concurrency(WORKFLOWS / name)
                self.assertEqual(cancel, "${{ github.event_name != 'push' }}")

    def test_only_main_pushes_can_enter_the_shared_push_group(self):
        """The constant push group is only right while `push` is limited to main: a push trigger on another branch would
        share (and be superseded in) the main group."""
        for name in ("tests.yml", "install.yml"):
            with self.subTest(workflow=name):
                text = (WORKFLOWS / name).read_text()
                self.assertRegex(text, r"(?m)^  push:\n    branches: \[main\]\n")


if __name__ == "__main__":
    unittest.main()
