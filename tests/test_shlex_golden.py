import importlib.util
import json
import os
import re
import subprocess
import sys
import unittest

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

    @unittest.skipUnless(ON_REFERENCE, "golden is built by CPython " + shlex_golden.PYTHON)
    def test_check_mode_rejects_stale_golden(self):
        doc = shlex_golden.build()
        doc["cases"][0]["input"] += "x"
        self.assertNotEqual(shlex_golden.render(doc), open(GOLDEN, encoding="utf-8").read())

    @unittest.skipIf(ON_REFERENCE, "only the wrong-version refusal is tested here")
    def test_other_python_is_refused(self):
        r = subprocess.run([sys.executable, TOOL, "--check"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)


if __name__ == "__main__":
    unittest.main()
