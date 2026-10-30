"""VERSION file, `factory --version` and `ci/changelog.py --check-version` (T-0792)."""
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CHANGELOG = ROOT / "ci" / "changelog.py"


def run(cmd, env=None, cwd=None):
    return subprocess.run(cmd, capture_output=True, text=True, env=env, cwd=cwd, timeout=60)


def clean_env(**extra):
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_") and k != "FACTORY_ROLE"}
    env.update(extra)
    return env


def copy_bin(dest):
    """A copy of bin/ plus web/ (factory_cli imports web.dbpath) without any .git."""
    for sub in ("bin", "web"):
        shutil.copytree(ROOT / sub, dest / sub, ignore=shutil.ignore_patterns("__pycache__"))


class TestVersionFile(unittest.TestCase):
    def test_content(self):
        self.assertEqual((ROOT / "VERSION").read_text(), "0.2.0\n")


class TestFactoryVersion(unittest.TestCase):
    def test_with_git_and_no_db(self):
        with tempfile.TemporaryDirectory() as home:
            p = run([sys.executable, str(ROOT / "bin" / "factory"), "--version"], env=clean_env(FACTORY_HOME=home))
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertRegex(p.stdout, r"^hivemill 0\.2\.0 \(git [0-9a-f]{12}\)\n$")
            self.assertEqual(os.listdir(home), [])

    def test_without_git_binary(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            copy_bin(tmp)
            shutil.copy(ROOT / "VERSION", tmp / "VERSION")
            empty_path = tmp / "nopath"
            empty_path.mkdir()
            p = run([sys.executable, str(tmp / "bin" / "factory"), "--version"],
                    env=clean_env(PATH=str(empty_path), FACTORY_HOME=str(tmp / "home")))
            self.assertEqual((p.returncode, p.stdout), (0, "hivemill 0.2.0\n"), p.stderr)

    def test_non_git_copy(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            copy_bin(tmp)
            shutil.copy(ROOT / "VERSION", tmp / "VERSION")
            p = run([sys.executable, str(tmp / "bin" / "factory"), "--version"], env=clean_env(GIT_CEILING_DIRECTORIES=str(tmp.parent)))
            self.assertEqual((p.returncode, p.stdout), (0, "hivemill 0.2.0\n"), p.stderr)

    def test_missing_and_empty_version_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            copy_bin(tmp)
            p = run([sys.executable, str(tmp / "bin" / "factory"), "--version"], env=clean_env())
            self.assertEqual((p.returncode, p.stdout), (0, "hivemill unknown\n"), p.stderr)
            (tmp / "VERSION").write_text("\n")
            p = run([sys.executable, str(tmp / "bin" / "factory"), "--version"], env=clean_env())
            self.assertEqual((p.returncode, p.stdout), (0, "hivemill unknown\n"), p.stderr)

    def test_help_still_works(self):
        p = run([sys.executable, str(ROOT / "bin" / "factory"), "--help"], env=clean_env())
        self.assertEqual(p.returncode, 0, p.stderr)


class TestCheckVersion(unittest.TestCase):
    def fixture(self, tmp, version="1.2.3", file_version=None, notes=True, upgrade=True, heading=True):
        """A repo-like tree with ci/changelog.py (+ scrub_scan.py it imports); returns its changelog.py path."""
        tmp = Path(tmp)
        (tmp / "ci" / "release-notes").mkdir(parents=True)
        for name in ("changelog.py", "scrub_scan.py"):
            shutil.copy(ROOT / "ci" / name, tmp / "ci" / name)
        (tmp / "VERSION").write_text((file_version or version) + "\n")
        if notes:
            body = "### Highlights\n\nx\n" + ("\n### Upgrade\n\ny\n" if upgrade else "")
            (tmp / "ci" / "release-notes" / f"{version}.md").write_text(body)
        (tmp / "CHANGELOG.md").write_text(f"# Changelog\n\n## [{version}] - 2026-01-01\n" if heading else "# Changelog\n")
        return tmp / "ci" / "changelog.py"

    def check(self, script, version="1.2.3"):
        return run([sys.executable, str(script), "--check-version", version], env=clean_env())

    def test_pass(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self.check(self.fixture(tmp))
            self.assertEqual((p.returncode, p.stderr), (0, ""))

    def test_real_repo_passes_for_current_version(self):
        p = run([sys.executable, str(CHANGELOG), "--check-version", "0.2.0"], env=clean_env())
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_real_repo_blocks_other_version(self):
        p = run([sys.executable, str(CHANGELOG), "--check-version", "9.9.9"], env=clean_env())
        self.assertEqual(p.returncode, 1)
        self.assertIn("VERSION file", p.stderr)
        self.assertIn("ci/release-notes/9.9.9.md", p.stderr)

    def test_fail_version_mismatch(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self.check(self.fixture(tmp, file_version="1.2.2"))
            self.assertEqual(p.returncode, 1)
            self.assertEqual(len(p.stderr.strip().splitlines()), 1)
            self.assertIn("VERSION file", p.stderr)

    def test_fail_notes_missing(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self.check(self.fixture(tmp, notes=False))
            self.assertEqual(p.returncode, 1)
            self.assertEqual(len(p.stderr.strip().splitlines()), 1)
            self.assertIn("release-notes/1.2.3.md is missing", p.stderr)

    def test_fail_no_upgrade_heading(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self.check(self.fixture(tmp, upgrade=False))
            self.assertEqual(p.returncode, 1)
            self.assertEqual(len(p.stderr.strip().splitlines()), 1)
            self.assertIn("### Upgrade", p.stderr)

    def test_fail_no_changelog_heading(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self.check(self.fixture(tmp, heading=False))
            self.assertEqual(p.returncode, 1)
            self.assertEqual(len(p.stderr.strip().splitlines()), 1)
            self.assertIn("## [1.2.3]", p.stderr)

    def test_usage_error_exits_2(self):
        p = run([sys.executable, str(CHANGELOG), "--check-version"], env=clean_env())
        self.assertEqual(p.returncode, 2)


class TestReleaseWorkflow(unittest.TestCase):
    def test_check_step_precedes_release_creation(self):
        lines = (ROOT / ".github" / "workflows" / "release.yml").read_text().splitlines()
        check = [i for i, l in enumerate(lines) if 'changelog.py --check-version "${GITHUB_REF_NAME#v}"' in l]
        create = [i for i, l in enumerate(lines) if "gh release create" in l]
        self.assertEqual(len(check), 1)
        self.assertEqual(len(create), 1)
        self.assertLess(check[0], create[0])


if __name__ == "__main__":
    unittest.main()
