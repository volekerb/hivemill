"""Eval task suite under skilleval/tasks: template fails, template + solution passes."""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TASKS = ROOT / "skilleval" / "tasks"
EXPECTED = ["add-feature", "fix-bug", "review-diff"]
FORBIDDEN_WORDS = ("skill", "claude", "eval")
NETWORK_MODULES = ("urllib", "socket", "http", "requests", "ftplib", "smtplib", "telnetlib", "ssl")


def _env():
    return {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": "/nonexistent",
            "PYTHONDONTWRITEBYTECODE": "1", "LANG": "C.UTF-8"}


def _overlay(src, dst):
    if src.is_dir():
        shutil.copytree(src, dst, dirs_exist_ok=True)


def _run_check(task, work):
    start = time.monotonic()
    proc = subprocess.run([sys.executable, str(TASKS / task / "check.py")], cwd=work, env=_env(),
                          capture_output=True, text=True, timeout=60)
    return proc, time.monotonic() - start


def _trial(task, with_solution, extra=None):
    """Build a work dir (repo [+ solution] + hidden [+ extra files]) and run check.py."""
    with tempfile.TemporaryDirectory() as tmp:
        work = Path(tmp) / "work"
        work.mkdir()
        _overlay(TASKS / task / "repo", work)
        if with_solution:
            _overlay(TASKS / task / "solution", work)
        for name, text in (extra or {}).items():
            (work / name).write_text(text)
        _overlay(TASKS / task / "hidden", work)
        return _run_check(task, work)


class TaskSuite(unittest.TestCase):
    def test_exact_task_set_and_layout(self):
        self.assertEqual(sorted(p.name for p in TASKS.iterdir()), EXPECTED)
        for task in EXPECTED:
            with self.subTest(task=task):
                d = TASKS / task
                for name in ("task.md", "check.py", "meta.json"):
                    self.assertTrue((d / name).is_file(), name)
                for name in ("repo", "hidden", "solution"):
                    self.assertTrue(any((d / name).iterdir()), name)

    def test_meta_json(self):
        for task in EXPECTED:
            with self.subTest(task=task):
                meta = json.loads((TASKS / task / "meta.json").read_text())
                self.assertEqual(set(meta), {"id", "role", "title", "timeout_s"})
                self.assertEqual(meta["id"], task)
                self.assertIn(meta["role"], ("coder", "reviewer"))
                self.assertTrue(meta["title"])
                self.assertIsInstance(meta["timeout_s"], int)
                self.assertGreater(meta["timeout_s"], 0)

    def test_task_md_short_and_neutral(self):
        for task in EXPECTED:
            with self.subTest(task=task):
                text = (TASKS / task / "task.md").read_text()
                self.assertLessEqual(len(text.splitlines()), 60)
                for word in FORBIDDEN_WORDS:
                    self.assertNotIn(word, text.lower())

    def test_check_py_stdlib_and_offline(self):
        for task in EXPECTED:
            with self.subTest(task=task):
                src = (TASKS / task / "check.py").read_text()
                mods = re.findall(r"^\s*(?:import|from)\s+([\w.]+)", src, re.MULTILINE)
                stdlib = sys.stdlib_module_names
                for mod in mods:
                    top = mod.split(".")[0]
                    self.assertIn(top, stdlib, mod)
                    self.assertNotIn(top, NETWORK_MODULES, mod)
                self.assertNotRegex(src, r"urllib|socket|http|requests")

    def test_template_fails_and_solution_passes(self):
        for task in EXPECTED:
            with self.subTest(task=task):
                proc, secs = _trial(task, with_solution=False)
                self.assertNotEqual(proc.returncode, 0, proc.stdout)
                self.assertLessEqual(secs, 60)
                proc, secs = _trial(task, with_solution=True)
                self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
                self.assertLessEqual(secs, 60)

    def test_hidden_not_visible_in_repo(self):
        for task in EXPECTED:
            with self.subTest(task=task):
                repo = {p.name for p in (TASKS / task / "repo").rglob("*") if p.is_file()}
                hidden = {p.name for p in (TASKS / task / "hidden").rglob("*") if p.is_file()}
                self.assertFalse(repo & hidden)

    def test_not_collected_by_tests_discovery(self):
        def ids(suite):
            for t in suite:
                if isinstance(t, unittest.TestSuite):
                    yield from ids(t)
                else:
                    yield t.id()
        found = list(ids(unittest.TestLoader().discover(str(ROOT / "tests"))))
        self.assertTrue(found)
        for tid in found:
            mod = sys.modules[tid.split(".")[0]]
            self.assertNotIn("skilleval", Path(mod.__file__).parts)
            self.assertFalse(tid.startswith(("test_taskdeps", "test_afterlist")), tid)


REVIEW_GOOD = "# Review\n\nVerdict: request changes\n\n1. `app/store.py:39`: SQL built with an f-string.\n"


class ReviewDiffCheck(unittest.TestCase):
    def run_review(self, text):
        proc, _ = _trial("review-diff", with_solution=False, extra={"REVIEW.md": text})
        return proc

    def test_right_location_passes(self):
        self.assertEqual(self.run_review(REVIEW_GOOD).returncode, 0)

    def test_within_two_lines_passes(self):
        self.assertEqual(self.run_review(REVIEW_GOOD.replace(":39", ":41")).returncode, 0)

    def test_wrong_line_fails(self):
        self.assertNotEqual(self.run_review(REVIEW_GOOD.replace(":39", ":42")).returncode, 0)

    def test_wrong_file_fails(self):
        self.assertNotEqual(self.run_review(REVIEW_GOOD.replace("store.py", "handlers.py")).returncode, 0)

    def test_missing_verdict_fails(self):
        self.assertNotEqual(self.run_review(REVIEW_GOOD.replace("request changes", "approve")).returncode, 0)

    def test_six_findings_fails(self):
        extra = "".join("%d. `app/util.py:%d`: nit.\n" % (n, n) for n in range(2, 7))
        proc = self.run_review(REVIEW_GOOD + extra)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("6 findings", proc.stdout)

    def test_five_findings_pass(self):
        extra = "".join("%d. `app/util.py:%d`: nit.\n" % (n, n) for n in range(2, 6))
        self.assertEqual(self.run_review(REVIEW_GOOD + extra).returncode, 0)

    def test_approve_verdict_fails_despite_prose(self):
        text = REVIEW_GOOD.replace("request changes", "approve") + "\nI would otherwise request changes.\n"
        proc = self.run_review(text)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("verdict", proc.stdout)

    def test_six_findings_in_any_format_fail(self):
        extra = "### app/util.py:1\napp/util.py:2 nit\n- app/util.py:3 nit\n* app/util.py:4\n2) app/util.py:5\n"
        proc = self.run_review(REVIEW_GOOD + extra)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("6 findings", proc.stdout)

    def test_six_refs_on_two_lines_fail(self):
        extra = "app/util.py:1 app/util.py:2 app/util.py:3\napp/util.py:4 app/util.py:5\n"
        proc = self.run_review(REVIEW_GOOD + extra)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("6 findings", proc.stdout)

    def test_five_refs_on_two_lines_pass(self):
        extra = "app/util.py:1 app/util.py:2 app/util.py:3\napp/util.py:4\n"
        self.assertEqual(self.run_review(REVIEW_GOOD + extra).returncode, 0)

    def test_repeated_ref_counts_once(self):
        extra = "app/util.py:1 again app/util.py:1\n" * 3
        self.assertEqual(self.run_review(REVIEW_GOOD + extra).returncode, 0)

    def test_narrow_range_passes(self):
        self.assertEqual(self.run_review(REVIEW_GOOD.replace(":39", ":37-40")).returncode, 0)

    def test_wide_range_fails(self):
        proc = self.run_review(REVIEW_GOOD.replace(":39", ":1-100"))
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("too wide", proc.stdout)

    def test_six_spaced_findings_fail(self):
        extra = "".join("%d. `app/util.py: %d`: nit.\n" % (n, n) for n in range(2, 7))
        proc = self.run_review(REVIEW_GOOD + extra)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("6 findings", proc.stdout)

    def test_five_mixed_form_findings_pass(self):
        extra = "app/util.py: 2, app/util.py:3, app/util.py: 4-5, app/util.py:6\n"
        self.assertEqual(self.run_review(REVIEW_GOOD + extra).returncode, 0)

    def test_equivalent_ref_forms_count_once(self):
        extra = "app/util.py:1 app/util.py: 1 app/util.py:1-1\n" + \
            "".join("app/util.py:%d\n" % n for n in range(2, 5))
        self.assertEqual(self.run_review(REVIEW_GOOD + extra).returncode, 0)

    def test_version_prose_not_a_ref(self):
        extra = "".join("app/util.py:%d\n" % n for n in range(2, 6)) + \
            "Tested on Python 3.11: 2 runs, v1.2: 5 times, 3.11:2.\n"
        self.assertEqual(self.run_review(REVIEW_GOOD + extra).returncode, 0)

    def test_two_space_ref_not_a_ref(self):
        extra = "".join("app/util.py:%d\n" % n for n in range(2, 6)) + "app/util.py:  9\n"
        self.assertEqual(self.run_review(REVIEW_GOOD + extra).returncode, 0)

    def test_tab_ok_newline_not_a_ref(self):
        base = "".join("app/util.py:%d\n" % n for n in range(2, 5))
        self.assertEqual(self.run_review(REVIEW_GOOD + base + "app/util.py:\t5\n").returncode, 0)
        proc = self.run_review(REVIEW_GOOD + base + "app/util.py:5\napp/util.py:\n6\napp/util.py:\n7\n")
        self.assertEqual(proc.returncode, 0)

    def test_spaced_defect_ref_credited(self):
        proc = self.run_review(REVIEW_GOOD.replace(":39", ": 39"))
        self.assertEqual(proc.returncode, 0)
        self.assertIn("PASS: defect at app/store.py:39 found", proc.stdout)

    def test_spaced_wide_range_fails(self):
        proc = self.run_review(REVIEW_GOOD.replace(":39", ": 1-100"))
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("too wide", proc.stdout)

    def test_no_review_fails(self):
        proc, _ = _trial("review-diff", with_solution=False)
        self.assertNotEqual(proc.returncode, 0)


if __name__ == "__main__":
    unittest.main()
