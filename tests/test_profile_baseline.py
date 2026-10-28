"""ci/profile_baseline.py: runs as a subprocess with a temp FACTORY_HOME and --runs 1; read-only."""
import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "ci", "profile_baseline.py")


def load():
    spec = importlib.util.spec_from_file_location("profile_baseline", SCRIPT)
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


class ProfileBaselineRun(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.home = os.path.join(cls.tmp.name, "home")
        os.mkdir(cls.home)
        cls.jpath = os.path.join(cls.tmp.name, "pb.json")
        env = dict(os.environ, FACTORY_HOME=cls.home)
        cls.p = subprocess.run([sys.executable, SCRIPT, "--runs", "1", "--json", cls.jpath],
                               capture_output=True, text=True, env=env, cwd=ROOT)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_exit_and_sections(self):
        self.assertEqual(self.p.returncode, 0, self.p.stderr)
        for sec in ("python_start", "guard", "cli", "tick"):
            self.assertRegex(self.p.stdout, r"(?m)^" + sec)

    def test_json_keys(self):
        d = json.load(open(self.jpath))
        self.assertEqual(sorted(d), ["cli", "guard", "meta", "python_start", "tick"])
        self.assertTrue({"git_sha", "python", "nproc", "loadavg", "date"} <= set(d["meta"]))
        self.assertEqual(sorted(d["guard"]), sorted(["coder", "qa", "reviewer", "judge", "cleaner", "evaluator", "hq"]))
        for k in ("min", "median", "p95"):
            self.assertIn(k, d["guard"]["coder"])

    def test_guard_exit_codes(self):
        g = json.load(open(self.jpath))["guard"]
        self.assertEqual(g["coder"]["exit_codes"], {"allowed": 0, "blocked": 2, "heredoc": 0})
        self.assertEqual(g["hq"]["exit_codes"]["allowed"], 0)

    def test_cli_names_scout(self):
        self.assertRegex(self.p.stdout, r"(?m)^cli .*scout")

    def test_temp_home_untouched(self):
        self.assertEqual(os.listdir(self.home), [])


class ProfileBaselineUnits(unittest.TestCase):
    def test_hook_cmd_is_used(self):
        with tempfile.TemporaryDirectory() as t:
            shim = os.path.join(t, "shim.py")
            with open(shim, "w") as f:
                f.write("import sys; sys.stdin.read(); sys.exit(7)\n")
            out = os.path.join(t, "o.json")
            p = subprocess.run([sys.executable, SCRIPT, "--runs", "1", "--json", out, "--hook-cmd",
                                f"{sys.executable} {shim}"], capture_output=True, text=True, cwd=ROOT)
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertEqual(json.load(open(out))["guard"]["qa"]["exit_codes"],
                             {"allowed": 7, "blocked": 7, "heredoc": 7})

    def test_parse_tick_journal(self):
        m = load()
        text = ("100.000 h systemd[1]: Starting factory-tick.service - x...\n"
                "104.000 h systemd[1]: Finished factory-tick.service - x.\n"
                "160.000 h systemd[1]: Starting factory-tick.service - x...\n"
                "163.000 h factory[9]: SPAWNED a\n"
                "169.000 h systemd[1]: Finished factory-tick.service - x.\n")
        self.assertEqual(m.parse_tick_journal(text, 10), [4.0, 9.0])
        self.assertEqual(m.parse_tick_journal(text, 1), [9.0])

    def test_tick_no_data(self):
        m = load()
        r = m.bench_tick(5, journal_cmd=f"{sys.executable} -c pass")
        self.assertEqual(r["status"], "no data")
        self.assertIn("no Starting/Finished", r["reason"])
        r = m.bench_tick(5, journal_cmd="/nonexistent/journalctl")
        self.assertEqual(r["status"], "no data")

    def test_parse_importtime(self):
        m = load()
        rows = m.parse_importtime("import time:       100 |       300 |   scout.x\nnoise\n")
        self.assertEqual(rows, [(300, "scout.x", 100)])

    def test_script_never_runs_mutating_factory_commands(self):
        src = open(SCRIPT).read()
        self.assertIsNone(re.search(r"factory (tick|spawn|up|done|msg|task update)", src.replace("factory-tick", "")))


if __name__ == "__main__":
    unittest.main()
