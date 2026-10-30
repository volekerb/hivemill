import contextlib
import importlib.util
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOL = os.path.join(ROOT, "ci", "guard_golden.py")
GOLDEN = os.path.join(ROOT, "go", "internal", "guard", "testdata", "frontend_golden.json")

_spec = importlib.util.spec_from_file_location("guard_golden", TOOL)
guard_golden = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(guard_golden)

ON_REFERENCE = "%d.%d" % sys.version_info[:2] == guard_golden.PYTHON


class CaseTests(unittest.TestCase):
    def setUp(self):
        self.hook = guard_golden.load_hook()

    def test_heredoc_body_is_dropped_and_recorded(self):
        c = guard_golden.case(self.hook, "cat <<EOF\nx\nEOF\nls")
        self.assertEqual(c["preprocess"]["text"], "cat <<EOF ; ls")
        self.assertEqual(c["preprocess"]["bodies"], [["cat <<EOF", "x"]])

    def test_shell_heredoc_becomes_an_extra(self):
        c = guard_golden.case(self.hook, "bash <<EOF\nsudo ls\nEOF")
        self.assertEqual(c["preprocess"]["extras"], [["sh", "sudo ls"]])

    def test_unbalanced_quote_is_recorded_as_error(self):
        c = guard_golden.case(self.hook, "echo 'a")
        self.assertEqual(c["preprocess"], {"error": "unbalanced quote"})
        self.assertNotIn("segments_pre", c)

    def test_unbalanced_substitution_is_recorded_as_error(self):
        c = guard_golden.case(self.hook, 'echo "$(a"')
        self.assertEqual(c["extract_subs_raw"], {"error": "unbalanced $( )"})

    def test_segment_offsets_count_characters(self):
        c = guard_golden.case(self.hook, "é; ls")
        self.assertEqual(c["segments_raw"], [[0, 1, ["é"]], [2, 5, ["ls"]]])


class GoldenFileTests(unittest.TestCase):
    def load(self):
        with open(GOLDEN, encoding="utf-8") as f:
            return json.load(f)

    def test_covers_corpus_and_required_edges(self):
        g = self.load()
        inputs = {c["input"] for c in g["cases"]}
        self.assertEqual(g["corpus_commands"], len(guard_golden.corpus_commands()))
        for cmd in guard_golden.corpus_commands():
            self.assertIn(cmd, inputs)
        for must in ("", "echo 'a", "cat <<EOF\nhello\nEOF", "python3 <<EOF\nsudoé\nEOF", "echo 日本語herdr"):
            self.assertIn(must, inputs)

    def test_reference_python_matches_ci(self):
        with open(os.path.join(ROOT, ".github", "workflows", "tests.yml"), encoding="utf-8") as f:
            versions = set(re.findall(r'python-version: "([^"]+)"', f.read()))
        self.assertEqual(versions, {guard_golden.PYTHON})
        self.assertEqual(self.load()["python"], guard_golden.PYTHON)

    @unittest.skipUnless(ON_REFERENCE, "golden is built by CPython " + guard_golden.PYTHON)
    def test_check_mode_passes_on_committed_golden(self):
        r = subprocess.run([sys.executable, TOOL, "--check"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)

    def run_main(self, *argv):
        err = io.StringIO()
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
            return guard_golden.main(list(argv)), err.getvalue()

    @unittest.skipUnless(ON_REFERENCE, "golden is built by CPython " + guard_golden.PYTHON)
    def test_check_mode_rejects_stale_golden(self):
        with tempfile.TemporaryDirectory() as d:
            stale = os.path.join(d, "frontend_golden.json")
            with open(stale, "w", encoding="utf-8") as f:
                f.write(guard_golden.render(guard_golden.build()).replace('"python": "3.13"', '"python": "3.12"'))
            with mock.patch.object(guard_golden, "GOLDEN", stale):
                rc, err = self.run_main("--check")
        self.assertEqual(rc, 1)
        self.assertIn("stale", err)

    @unittest.skipUnless(ON_REFERENCE, "golden is built by CPython " + guard_golden.PYTHON)
    def test_check_mode_rejects_missing_golden(self):
        with tempfile.TemporaryDirectory() as d:
            with mock.patch.object(guard_golden, "GOLDEN", os.path.join(d, "none.json")):
                rc, err = self.run_main("--check")
        self.assertEqual(rc, 1)
        self.assertIn("stale", err)

    def test_other_python_is_refused(self):
        with mock.patch.object(guard_golden, "PYTHON", "2.7"):
            rc, err = self.run_main("--check")
        self.assertEqual(rc, 2)
        self.assertIn("needs CPython 2.7", err)


if __name__ == "__main__":
    unittest.main()
