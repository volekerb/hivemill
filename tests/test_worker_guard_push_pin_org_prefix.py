"""M16430: the coder push pin accepts the org prefix (claude-|cursor-) on the coder name and nothing wider.
Runs through guard_impl.hook_run, so both the Python hook and the Go guard are exercised."""
import os
import unittest

import test_worker_guard_push_pin as base

TASK = base.TASK
OWN = base.OWN
P = base.PushPinTests


class OrgPrefixPushPinTests(unittest.TestCase):
    setUp = P.setUp
    write_db = P.write_db
    run_hook = P.run_hook
    pinned = P.pinned

    def test_org_prefixed_and_legacy_coders_may_push_own_branch(self):
        for agent in (f"claude-coder-{TASK}", f"cursor-coder-{TASK}", f"coder-{TASK}"):
            with self.subTest(agent=agent):
                p = self.run_hook(f"git push origin {OWN}", agent=agent)
                self.assertEqual(p.returncode, 0, p.stderr)

    def test_other_names_refused(self):
        for agent in (f"evil-coder-{TASK}", f"claude-cursor-coder-{TASK}", f"cursor-claude-coder-{TASK}",
                      f"Claude-coder-{TASK}", f"claude-coder-{TASK}x", f"claude-claude-coder-{TASK}", f"claude-{TASK}"):
            with self.subTest(agent=agent):
                p = self.run_hook(f"git push origin {OWN}", agent=agent)
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertTrue(self.pinned(f"git push origin {OWN}", agent=agent))

    def test_org_prefixed_coder_pinned_to_own_task(self):
        for agent in (f"claude-coder-{TASK}", f"cursor-coder-{TASK}"):
            with self.subTest(agent=agent):
                p = self.run_hook("git push origin HEAD:factory/t-0001", agent=agent)
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("coders push exactly", p.stderr)


del P  # not a module-level TestCase: the loader would re-run the base tests


if __name__ == "__main__":
    unittest.main()
