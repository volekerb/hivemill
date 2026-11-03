"""CI wiring of ci/guard_diff.py (T-0928): the real-corpus step of the guard-diff job in tests.yml, the skill text, and a loosened run."""
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import _gitenv  # noqa: F401  (git-spawning tests use the hermetic env helper)

ROOT = Path(__file__).resolve().parent.parent
WF = (ROOT / ".github" / "workflows" / "tests.yml").read_text()
SKILL = (ROOT / "roles/plugins/coder/coder-skills/skills/guard-change/SKILL.md").read_text()
JOB = "guard-diff"
STEP = "guard_diff.py base vs PR (real corpus)"


def job_block(text, name):
    m = re.search(r"(?ms)^  %s:\n(.*?)(?=^  [a-z][a-z-]*:\n|\Z)" % re.escape(name), text)
    if not m:
        raise AssertionError("tests.yml has no %s job" % name)
    return m.group(1)


class WorkflowWiring(unittest.TestCase):
    def test_job_runs_on_each_guard_path(self):
        block = job_block(WF, JOB)
        pattern = re.search(r"grep -E -q '([^']+)'", block).group(1)
        for path in ("go/cmd/worker" + "-guard/main.go", "go/internal/shlex/shlex.go",
                     "roles/hooks/some-hook.py", "roles/settings/coder.json.in", "ci/guard_corpus.json",
                     "ci/guard_diff.py", "ci/public_text_guard.py", "ci/scrub_scan.py", ".github/workflows/tests.yml", "go/internal/guard/guard.go"):
            self.assertRegex(path, pattern, path)
        for path in ("docs/guard-diff.md", "ci/shard.py", "roles/prompts/coder.md", "go/internal/other/x.go"):
            self.assertNotRegex(path, pattern, path)

    def test_job_runs_guard_diff_from_base_with_pr_corpus(self):
        block = job_block(WF, JOB)
        self.assertIn('git archive "$BASE_SHA" ci/guard_diff.py tests/_slot.py', block)
        self.assertIn('--base "$BASE_SHA"', block)
        self.assertIn("git diff --name-only --no-renames", block)
        self.assertIn("--mode both", block)
        self.assertIn("--mode worker --role evaluator", block)
        # base corpus AND PR corpus, on both runs (a PR cannot delete must_block entries)
        self.assertIn('git archive "$BASE_SHA" ci/guard_corpus.json', block)
        self.assertIn('--corpus "$tool/ci/guard_corpus.json" --corpus ci/guard_corpus.json', block)
        self.assertEqual(block.count('"${corpus[@]}"'), 2)
        self.assertRegex(block, r'--mode both\b')
        self.assertNotRegex(block, r"--repo \. --corpus ci/guard_corpus\.json")
        self.assertIn("--allow-loosen", block)
        self.assertIn("ci/allow_loosen_*.json", block)
        self.assertIn("set -o pipefail", block)
        self.assertRegex(block, r'-le 100 \]')

    def test_job_feeds_required_unittest_aggregate(self):
        agg = job_block(WF, "unittest")
        self.assertRegex(agg, r"needs:\s*\[[^\]]*\bguard-diff\b[^\]]*\]")
        self.assertIn("needs.guard-diff.result", agg)
        self.assertRegex(agg, r'"\$GUARD_DIFF_RESULT" != "success"')
        block = job_block(WF, JOB)
        self.assertIn("- name: " + STEP, block)
        self.assertNotRegex(block, r"continue-on-error:\s*true")
        self.assertIn("fetch-depth: 0", block)

    def test_skill_no_longer_tells_coders_to_run_guard_diff(self):
        for line in SKILL.splitlines():
            if "guard_diff.py" in line:
                self.assertRegex(line, r"[Dd]o not run", line)
        self.assertNotIn("python3 ci/guard_diff.py", SKILL)
        self.assertIn("real-corpus step", SKILL)


class LoosenedRun(unittest.TestCase):
    """The tool CI runs, as a subprocess: a hook that stops blocking a corpus command fails with a LOOSENED line."""

    def run_tool(self, pr_hook_src, allow=None, pr_corpus=None, base_corpus=False):
        with tempfile.TemporaryDirectory() as td:
            repo = os.path.join(td, "repo")
            os.makedirs(os.path.join(repo, "roles", "hooks"))
            hook = os.path.join(repo, "roles/hooks/stub-hook.py")
            base_src = "import json, sys\nc = json.load(sys.stdin)['tool_input']['command']\nsys.exit(2 if c == 'danger-cmd' else 0)\n"
            Path(hook).write_text(base_src)
            env = dict(os.environ, CI="true")
            for args in (["init", "-q"], ["add", "-A"], ["-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "b"]):
                subprocess.run(["git", "-C", repo, *args], check=True, env=env)
            Path(hook).write_text(pr_hook_src)
            corpus = os.path.join(td, "corpus.json")
            Path(corpus).write_text(json.dumps(pr_corpus or {"must_block": ["danger-cmd"], "must_allow": ["ls"]}))
            cmd = [sys.executable, str(ROOT / "ci/guard_diff.py"), "--base", "HEAD", "--repo", repo, "--hook", hook]
            if base_corpus:
                bc = os.path.join(td, "base-corpus.json")
                Path(bc).write_text(json.dumps({"must_block": ["danger-cmd"], "must_allow": ["ls"]}))
                cmd += ["--corpus", bc]
            cmd += ["--corpus", corpus, "--mode", "worker"]
            if allow:
                af = os.path.join(td, "allow.json")
                Path(af).write_text(json.dumps(allow))
                cmd += ["--allow-loosen", af]
            return subprocess.run(cmd, capture_output=True, text=True, env=env)

    def test_loosened_hook_fails_with_loosened_line(self):
        p = self.run_tool("import sys\nsys.exit(0)\n")
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^LOOSENED .*danger-cmd")

    def test_entry_deleted_from_pr_corpus_still_loosened(self):
        pr_corpus = {"must_block": [], "must_allow": ["ls"]}
        p = self.run_tool("import sys\nsys.exit(0)\n", pr_corpus=pr_corpus, base_corpus=True)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertRegex(p.stdout, r"(?m)^LOOSENED .*danger-cmd")

    def test_pr_corpus_alone_misses_deleted_entry(self):
        """Why the step passes the base corpus: the PR copy alone lets the deletion through."""
        pr_corpus = {"must_block": [], "must_allow": ["ls"]}
        p = self.run_tool("import sys\nsys.exit(0)\n", pr_corpus=pr_corpus, base_corpus=False)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_unchanged_hook_passes(self):
        same = "import json, sys\nc = json.load(sys.stdin)['tool_input']['command']\nsys.exit(2 if c == 'danger-cmd' else 0)\n"
        p = self.run_tool(same)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("0 loosened", p.stdout)

    def test_listed_loosening_passes(self):
        p = self.run_tool("import sys\nsys.exit(0)\n", allow=["danger-cmd"])
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)


if __name__ == "__main__":
    unittest.main()
