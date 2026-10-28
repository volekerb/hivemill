import subprocess
import unittest
from unittest import mock

from harness import FactoryTestCase

FLAGS = ["-c", "core.commitGraph=false", "-c", "core.attributesFile=/dev/null"]
MAIN, HEAD = "a" * 40, "b" * 40


class HeadHasMainFlagsTests(FactoryTestCase):
    """head_has_main and verdict_carries run every git call with the same hardened -c flags."""

    def run_check(self, call):
        seen = []

        def fake_git(args, **kw):
            seen.append(list(args))
            if "rev-parse" in args:
                return 0, (HEAD if "pr-" in args[-1] else MAIN), ""
            return 0, "", ""

        gh = subprocess.CompletedProcess([], 0, stdout=MAIN + "\n", stderr="")
        with self.env.load_factory() as mod, mock.patch.object(mod, "_git_rc", fake_git), \
                mock.patch.object(mod.subprocess, "run", return_value=gh):
            result = call(mod)
        return result, seen

    def test_head_has_main_every_git_call_has_flags(self):
        result, seen = self.run_check(lambda m: m.head_has_main("/repo", HEAD, 7, "https://x/y.git", "o/r"))
        self.assertEqual(result, (True, "", MAIN))
        self.assertEqual(len(seen), 5)  # 2 fetches, 2 rev-parse, 1 merge-base
        for argv in seen:
            self.assertEqual(argv[:4], FLAGS, argv)
        self.assertEqual(seen[-1], FLAGS + ["merge-base", "--is-ancestor", MAIN, HEAD])
        self.assertEqual(seen[0], FLAGS + ["-c", "remote.origin.fetch=", "fetch", "-q", "--no-write-fetch-head",
                                           "https://x/y.git", "+refs/heads/main:refs/factory-judge/main"])

    def test_verdict_carries_every_git_call_has_flags(self):
        result, seen = self.run_check(lambda m: m.verdict_carries("/repo", HEAD, HEAD, 7, "https://x/y.git", "o/r"))
        self.assertTrue(result[0])
        self.assertTrue(seen)
        for argv in seen:
            self.assertEqual(argv[:4], FLAGS, argv)

    def test_verified_main_every_git_call_has_flags(self):
        def call(m):
            git, main_sha, why = m.verified_main("/repo", 7, "https://x/y.git", "o/r", head=HEAD)
            git("merge-base", "--is-ancestor", MAIN, HEAD)
            return main_sha, why

        result, seen = self.run_check(call)
        self.assertEqual(result, (MAIN, ""))
        self.assertEqual(len(seen), 5)  # 2 fetches, 2 rev-parse, 1 call through the returned git
        for argv in seen:
            self.assertEqual(argv[:4], FLAGS, argv)
        self.assertEqual(seen[0], FLAGS + ["-c", "remote.origin.fetch=", "fetch", "-q", "--no-write-fetch-head",
                                           "https://x/y.git", "+refs/heads/main:refs/factory-judge/main"])
        self.assertEqual(seen[-1], FLAGS + ["merge-base", "--is-ancestor", MAIN, HEAD])


if __name__ == "__main__":
    unittest.main()
