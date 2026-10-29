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
TOOL = os.path.join(ROOT, "ci", "shlex_golden.py")
GOLDEN = os.path.join(ROOT, "go", "internal", "shlex", "testdata", "golden.json")

_spec = importlib.util.spec_from_file_location("shlex_golden", TOOL)
shlex_golden = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(shlex_golden)

ON_REFERENCE = "%d.%d" % sys.version_info[:2] == shlex_golden.PYTHON


class LexTests(unittest.TestCase):
    def test_punctuation_run_is_one_token(self):
        self.assertEqual(shlex_golden.lex("script", "x>&2"), {"tokens": ["x", ">&", "2"]})

    def test_unterminated_quote_is_recorded_as_error(self):
        self.assertEqual(shlex_golden.lex("split", "a 'b"), {"error": "No closing quotation"})

    def test_trailing_backslash_is_recorded_as_error(self):
        self.assertEqual(shlex_golden.lex("script", "a \\"), {"error": "No escaped character"})

    def test_script_mode_keeps_hash_default_drops_comment(self):
        self.assertEqual(shlex_golden.lex("script", "a #b"), {"tokens": ["a", "#b"]})
        self.assertEqual(shlex_golden.lex("default", "a #b"), {"tokens": ["a"]})


class GoldenFileTests(unittest.TestCase):
    def load(self):
        with open(GOLDEN, encoding="utf-8") as f:
            return json.load(f)

    def test_covers_corpus_and_required_edges(self):
        g = self.load()
        inputs = {c["input"] for c in g["cases"]}
        self.assertEqual(g["corpus_commands"], len(shlex_golden.corpus_commands()))
        for cmd in shlex_golden.corpus_commands():
            self.assertIn(cmd, inputs)
        for must in ("café naïve", "中文 词", "a 'b", "a \\", "x>&2", "a|&b", ""):
            self.assertIn(must, inputs)

    def test_reference_python_matches_ci(self):
        with open(os.path.join(ROOT, ".github", "workflows", "tests.yml"), encoding="utf-8") as f:
            versions = set(re.findall(r'python-version: "([^"]+)"', f.read()))
        self.assertEqual(versions, {shlex_golden.PYTHON})
        self.assertEqual(self.load()["python"], shlex_golden.PYTHON)

    @unittest.skipUnless(ON_REFERENCE, "golden is built by CPython " + shlex_golden.PYTHON)
    def test_check_mode_passes_on_committed_golden(self):
        r = subprocess.run([sys.executable, TOOL, "--check"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)

    def run_main(self, *argv):
        err = io.StringIO()
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
            return shlex_golden.main(list(argv)), err.getvalue()

    @unittest.skipUnless(ON_REFERENCE, "golden is built by CPython " + shlex_golden.PYTHON)
    def test_check_mode_rejects_stale_golden(self):
        with tempfile.TemporaryDirectory() as d:
            stale = os.path.join(d, "golden.json")
            with open(stale, "w", encoding="utf-8") as f:
                f.write(shlex_golden.render(shlex_golden.build()).replace('"python": "3.13"', '"python": "3.12"'))
            with mock.patch.object(shlex_golden, "GOLDEN", stale):
                rc, err = self.run_main("--check")
        self.assertEqual(rc, 1)
        self.assertIn("stale", err)

    def test_other_python_is_refused(self):
        with mock.patch.object(shlex_golden, "PYTHON", "2.7"):
            rc, err = self.run_main("--check")
        self.assertEqual(rc, 2)
        self.assertIn("needs CPython 2.7", err)


if __name__ == "__main__":
    unittest.main()
