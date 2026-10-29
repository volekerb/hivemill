"""Tests for ci/ci_tier.py: PR tier classification, affected mode plan, docs check, and the tests.yml wiring."""
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "ci"))

import ci_tier  # noqa: E402

TIER = str(ROOT / "ci" / "ci_tier.py")
BIN_SRC = "def cmd_other():\n    return 1\n\n\ndef cmd_judge():\n    return 1\n\n\ndef judge_pick():\n    return 1\n\n\ndef gate_x():\n    return 1\n"


def git(cwd, *args):
    subprocess.run(["git", "-C", str(cwd), "-c", "user.email=t@example.invalid", "-c", "user.name=t", *args],
                   check=True, capture_output=True, text=True)


class Repo:
    """A temp git repo with a base commit; `change` commits a head on top and returns (base, head)."""

    def __init__(self, tmp):
        self.root = Path(tmp)
        git(self.root, "init", "-q", "-b", "main")
        self.write("bin/factory", BIN_SRC)
        self.write("tests/test_alpha.py", "import unittest\n\nclass A(unittest.TestCase):\n    def test_a(self):\n        pass\n")
        self.write("tests/test_db_layout.py", "")
        self.write("tests/test_inbox.py", "")
        self.write("tests/test_judge.py", "")
        for i in range(10):
            self.write("tests/test_pad%d.py" % i, "")
        self.write("web/app.py", "x = 1\n")
        self.write("docs/a.md", "# a\n")
        self.write("README.md", "hi\n")
        git(self.root, "add", "-A")
        git(self.root, "commit", "-q", "-m", "base")
        self.base = self.rev()

    def rev(self):
        return subprocess.run(["git", "-C", str(self.root), "rev-parse", "HEAD"], capture_output=True, text=True,
                              check=True).stdout.strip()

    def write(self, rel, text):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def change(self, files):
        for rel, text in files.items():
            self.write(rel, text)
        git(self.root, "add", "-A")
        git(self.root, "commit", "-q", "-m", "head")
        return self.base, self.rev()

    def classify(self, files):
        base, head = self.change(files)
        return ci_tier.classify(str(self.root), base, head)


class ClassifyTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.repo = Repo(self._tmp.name)

    def tier(self, files):
        return self.repo.classify(files)[0]

    def test_docs_only(self):
        self.assertEqual(self.tier({"README.md": "changed\n", "docs/a.md": "# b\n", "docs/img/x.png": "bin"}), "docs")

    def test_roles_markdown_is_not_docs(self):
        self.assertEqual(self.tier({"roles/coder.md": "x\n"}), "normal")

    def test_normal(self):
        self.assertEqual(self.tier({"web/app.py": "x = 2\n"}), "normal")

    def test_docs_plus_code_is_normal(self):
        self.assertEqual(self.tier({"README.md": "c\n", "web/app.py": "x = 3\n"}), "normal")

    def test_every_risky_path(self):
        paths = ["ci/public_text_guard.py", "ci/publish_repo.py", "ci/scrub_scan.py", "ci/guard_diff.py",
                 "ci/ci_tier.py", "ci/affected.py", "ci/shard.py", "roles/hooks/guard.sh",
                 "roles/settings/coder.json.in", "scout/nsbox.py", "scout/gatebox.py", "scout/ghcred.py",
                 ".github/workflows/tests.yml", ".github/CODEOWNERS"]
        for p in paths:
            with self.subTest(path=p):
                repo = Repo(tempfile.mkdtemp(dir=self._tmp.name))
                self.assertEqual(repo.classify({p: "x\n"})[0], "risky")

    def test_every_risky_def_in_bin_factory(self):
        defs = ["cmd_judge", "judge_pick", "gate_x", "verdict_carries", "head_has_main", "refresh_branch",
                "git_config_guard", "noreply_email", "_push_policy", "OWNER_CAP_KEYS", "DEFAULT_CONFIG", "SCHEMA"]
        for name in defs:
            with self.subTest(name=name):
                repo = Repo(tempfile.mkdtemp(dir=self._tmp.name))
                src = BIN_SRC.replace("def cmd_other", "def x_other")
                if name in BIN_SRC:  # an existing def: edit its body
                    src = BIN_SRC.replace("def %s():\n    return 1" % name, "def %s():\n    return 2" % name)
                else:  # a new unit
                    src = BIN_SRC + "\n\ndef %s():\n    return 3\n" % name if name[0].islower() or name[0] == "_" \
                        else BIN_SRC + "\n\n%s = 3\n" % name
                tier, why = repo.classify({"bin/factory": src})
                self.assertEqual(tier, "risky", why)
                self.assertIn(name, why)

    def test_other_bin_factory_def_is_normal(self):
        self.assertEqual(self.tier({"bin/factory": BIN_SRC.replace("def cmd_other():\n    return 1", "def cmd_other():\n    return 2")}),
                         "normal")

    def test_over_600_lines_is_risky_and_600_is_not(self):
        big = "".join("v%d = %d\n" % (i, i) for i in range(601))
        tier, why = self.repo.classify({"web/big.py": big})
        self.assertEqual((tier, "601 changed lines" in why), ("risky", True))
        repo = Repo(tempfile.mkdtemp(dir=self._tmp.name))
        self.assertEqual(repo.classify({"web/big.py": big[: big.rindex("v600")]})[0], "normal")

    def test_rules_come_from_the_script_that_runs(self):
        # A PR that edits the rules is itself risky, and CI runs the BASE copy of the script (see the workflow test).
        self.assertEqual(self.tier({"ci/ci_tier.py": "MAX_LINES = 10**9\n"}), "risky")


class PlanTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.repo = Repo(self._tmp.name)

    def plan(self, files):
        base, head = self.repo.change(files)
        return ci_tier.plan(str(self.repo.root), base, head)

    def test_docs_mode_skips_suite_and_go(self):
        p = self.plan({"README.md": "c\n"})
        self.assertEqual((p["tier"], p["mode"], p["go"], p["modules"]), ("docs", "docs", "false", ""))

    def test_normal_mode_lists_affected_modules(self):
        p = self.plan({"tests/test_alpha.py": "import unittest\n"})
        self.assertEqual((p["tier"], p["mode"], p["go"]), ("normal", "affected", "false"))
        self.assertEqual(p["modules"].split(), ["test_alpha.py", "test_db_layout.py", "test_inbox.py", "test_judge.py"])

    def test_go_change_runs_go_job(self):
        self.assertEqual(self.plan({"go/main.go": "package main\n"})["go"], "true")

    def test_risky_is_full_with_go(self):
        p = self.plan({".github/CODEOWNERS": "x\n"})
        self.assertEqual((p["mode"], p["go"]), ("full", "true"))

    def test_affected_full_selection_is_full_mode(self):
        p = self.plan({"tests/harness.py": ""})
        self.assertEqual((p["tier"], p["mode"]), ("normal", "full"))


class CliTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.repo = Repo(self._tmp.name)

    def run_cli(self, *args, env=None):
        return subprocess.run([sys.executable, TIER, *args], capture_output=True, text=True, env=env)

    def test_classify_writes_output_and_only_file(self):
        base, head = self.repo.change({"tests/test_alpha.py": "import unittest\n"})
        out, only = Path(self._tmp.name) / "gh_out", Path(self._tmp.name) / "only.txt"
        env = dict(os.environ, GITHUB_OUTPUT=str(out))
        r = self.run_cli("classify", "--root", str(self.repo.root), "--base", base, "--head", head,
                         "--only-file", str(only), "--github-output", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("mode=affected", out.read_text())
        self.assertIn("test_alpha.py", only.read_text().split())

    def test_unknown_base_falls_back_to_full(self):
        r = self.run_cli("classify", "--root", str(self.repo.root), "--base", "deadbeef" * 5)
        self.assertEqual(r.returncode, 0)
        self.assertIn("mode=full", r.stdout)
        self.assertIn("::warning::", r.stderr)

    def test_docs_check_accepts_good_and_rejects_broken_link_and_fence(self):
        base, head = self.repo.change({"docs/a.md": "# a\n[ok](../README.md) [web](https://x.example.test/y) [h](#top)\n"})
        r = self.run_cli("docs-check", "--root", str(self.repo.root), "--base", base, "--head", head)
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "docs-check: OK"))
        base2 = head
        self.repo.base = base2
        _, head2 = self.repo.change({"docs/a.md": "# a\n[bad](missing.md)\n```\nopen fence\n"})
        r = self.run_cli("docs-check", "--root", str(self.repo.root), "--base", base2, "--head", head2)
        self.assertEqual(r.returncode, 1)
        self.assertIn("missing.md does not exist", r.stderr)
        self.assertIn("unbalanced code fence", r.stderr)


class WorkflowTest(unittest.TestCase):
    wf = (ROOT / ".github" / "workflows" / "tests.yml").read_text()

    def test_job_names_unchanged(self):
        self.assertEqual(re.findall(r"(?m)^    name: (.+)$", self.wf),
                         ["unittest-shard (${{ matrix.shard }})", "go", "unittest"])

    def test_classifier_runs_from_the_base_commit(self):
        self.assertEqual(self.wf.count('git archive "$BASE_SHA" ci/ci_tier.py ci/affected.py'), 3)
        self.assertNotIn("python ci/ci_tier.py", self.wf)
        self.assertEqual(self.wf.count('python "$RUNNER_TEMP/base/ci/ci_tier.py" classify'), 3)

    def test_push_and_dispatch_run_full(self):
        self.assertIn('if [ "$EVENT" != pull_request ]; then full "$EVENT"; exit 0; fi', self.wf)

    def test_heavy_steps_are_gated_on_tier(self):
        self.assertIn("steps.tier.outputs.go == 'true'", self.wf)
        self.assertEqual(self.wf.count("steps.tier.outputs.mode != 'docs'"), 6)
        self.assertIn("--only", self.wf)

    def test_runners_are_free(self):
        self.assertEqual(set(re.findall(r"runs-on: (\S+)", self.wf)), {"ubuntu-latest"})


if __name__ == "__main__":
    unittest.main()
