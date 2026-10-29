"""T-0660: bin/factory is a stub for bin/factory_cli.py, so the guard must treat every way of running the module like `factory`."""
import os
import subprocess
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from harness import FACTORY_SRC  # noqa: E402
from test_worker_guard import run_hook  # noqa: E402


class GuardFactoryCli(unittest.TestCase):
    def blocked(self, cmd):
        p = run_hook(cmd)
        self.assertEqual(p.returncode, 2, f"should block: {cmd!r}\n{p.stderr}")

    def allowed(self, cmd):
        p = run_hook(cmd)
        self.assertEqual(p.returncode, 0, f"should allow: {cmd!r}\n{p.stderr}")

    def test_blocks_every_way_to_run_the_module(self):
        for cmd in ("python3 bin/factory res", "python3 bin/factory_cli.py res", "python3 ./bin/factory_cli.py res",
                    "python3 ~/factory/bin/factory_cli.py res", "python3 -X dev bin/factory_cli.py tick",
                    "python3 -m factory_cli res", "python3 -c 'import factory_cli; factory_cli.main()'",
                    "python3 -I -c \"import factory_cli\""):
            with self.subTest(cmd=cmd):
                self.blocked(cmd)

    def test_allows_the_harmless_neighbours(self):
        for cmd in ("python3 bin/factory whoami", "python3 bin/factory_cli.py whoami", "python3 -m unittest tests.test_cli_stub",
                    "python3 -c 'print(1)'", "grep factory_cli bin/factory"):
            with self.subTest(cmd=cmd):
                self.allowed(cmd)

    def test_module_is_not_runnable_directly(self):
        self.assertNotIn("__main__", FACTORY_SRC.read_text().split("\n\n\n")[-1])
        p = subprocess.run([sys.executable, str(FACTORY_SRC), "--help"], capture_output=True, text=True)
        self.assertEqual(p.stdout, "")


if __name__ == "__main__":
    unittest.main()
