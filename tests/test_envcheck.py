import tempfile
import unittest
from pathlib import Path

import envcheck


class HasGit(unittest.TestCase):
    def test_both_outcomes(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertFalse(envcheck.has_git(d))
            (Path(d) / ".git").mkdir()
            self.assertTrue(envcheck.has_git(d))

    def test_git_file_of_a_linked_worktree_counts(self):
        with tempfile.TemporaryDirectory() as d:
            (Path(d) / ".git").write_text("gitdir: /elsewhere\n")
            self.assertTrue(envcheck.has_git(d))

    def test_skip_decorator_skips_only_without_git(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(self.outcome(envcheck.skip_without_git(d)), "skipped")
            (Path(d) / ".git").mkdir()
            self.assertEqual(self.outcome(envcheck.skip_without_git(d)), "ran")

    @staticmethod
    def outcome(deco):
        @deco
        def probe(self):
            pass
        try:
            probe(None)
        except unittest.SkipTest as e:
            assert e.args[0] in (envcheck.NO_GIT_REASON, envcheck.RAM_REASON), e
            return "skipped"
        return "ran"


class OnRam(unittest.TestCase):
    def test_both_outcomes_with_injected_ram_dirs(self):
        with tempfile.TemporaryDirectory() as ram, tempfile.TemporaryDirectory() as disk:
            ram_dirs = (Path(ram),)
            self.assertTrue(envcheck.on_ram(Path(ram) / "a" / "b", ram_dirs))
            self.assertTrue(envcheck.on_ram(ram, ram_dirs))
            self.assertFalse(envcheck.on_ram(Path(disk) / "a", ram_dirs))

    def test_symlink_into_ram_is_resolved(self):
        with tempfile.TemporaryDirectory() as ram, tempfile.TemporaryDirectory() as disk:
            link = Path(disk) / "link"
            link.symlink_to(ram)
            self.assertTrue(envcheck.on_ram(link / "x", (Path(ram),)))

    def test_skip_decorator_follows_the_condition(self):
        with tempfile.TemporaryDirectory() as ram, tempfile.TemporaryDirectory() as disk:
            self.assertEqual(HasGit.outcome(envcheck.skip_on_ram(Path(ram) / "w", (Path(ram),))), "skipped")
            self.assertEqual(HasGit.outcome(envcheck.skip_on_ram(Path(disk) / "w", (Path(ram),))), "ran")


if __name__ == "__main__":
    unittest.main()
