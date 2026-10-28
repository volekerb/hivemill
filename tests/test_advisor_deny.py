import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from harness import REPO, ROLES, rendered_settings_dir

SETTINGS = sorted(rendered_settings_dir().glob("*.json"))
SECRET = "SECRET-CONTENT-ADVISOR-123"


def problems(d):
    out = []
    if "Advisor" not in d["permissions"]["deny"]:
        out.append("no advisor deny")
    if d["env"].get("CLAUDE_CODE_DISABLE_ADVISOR_TOOL") != "1":
        out.append("advisor tool not disabled via env")
    return out


class AdvisorDenyTests(unittest.TestCase):
    """Every role's settings file turns the advisor tool off (cost: it forwards the whole transcript per call)."""

    def test_every_role_has_a_settings_file(self):
        names = {p.stem for p in SETTINGS}
        self.assertTrue(set(ROLES) <= names, f"roles without settings: {set(ROLES) - names}")
        self.assertIn("boss", names)

    def test_every_settings_file_denies_advisor_and_disables_it(self):
        self.assertGreaterEqual(len(SETTINGS), 12)
        for p in SETTINGS:
            self.assertEqual(problems(json.loads(p.read_text())), [], p.name)

    def test_checker_rejects_missing_deny_or_env(self):
        d = json.loads(SETTINGS[0].read_text())
        no_deny = json.loads(json.dumps(d))
        no_deny["permissions"]["deny"].remove("Advisor")
        no_env = json.loads(json.dumps(d))
        del no_env["env"]["CLAUDE_CODE_DISABLE_ADVISOR_TOOL"]
        self.assertEqual(problems(d), [])
        self.assertEqual(problems(no_deny), ["no advisor deny"])
        self.assertEqual(problems(no_env), ["advisor tool not disabled via env"])


def line(**kw):
    return json.dumps(kw) + "\n"


def tool_use(ts, name, kind="tool_use", text=SECRET):
    return line(type="assistant", timestamp=ts, message={
        "model": "claude-opus-5-5", "content": [{"type": kind, "name": name, "input": {"q": text}}]})


class AdvisorAuditTests(unittest.TestCase):
    def run_audit(self, *args):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            d = root / "-home-x-factory-worktrees-G-014-T-0250"
            d.mkdir()
            (d / "sess-a.jsonl").write_text(
                line(type="user", timestamp="2026-10-25T10:00:00Z", message={"content": SECRET + " advisor"})
                + line(type="system", timestamp="2026-10-25T10:00:01Z",
                       tools=[{"type": "advisor_20260301", "name": "advisor", "model": "claude-fable-5-1"}])
                + tool_use("2026-10-25T10:01:00Z", "advisor", "server_tool_use")
                + tool_use("2026-10-25T10:02:00Z", "Bash")
                + line(type="assistant", timestamp="2026-10-25T10:03:00Z",
                       message={"model": "claude-fable-5-1", "content": [{"type": "text", "text": SECRET}]}))
            (d / "sess-clean.jsonl").write_text(
                tool_use("2026-10-25T11:00:00Z", "Bash")
                + line(type="user", timestamp="t", message={"content": "talks about an advisor tool"}))
            (d / "sess-def.jsonl").write_text(
                line(type="system", timestamp="t", tools=[{"type": "advisor_20260301", "name": "advisor",
                                                           "model": "claude-opus-5-5"}]))
            return subprocess.run([sys.executable, "-m", "web.advisor_audit", "--projects", str(root), *args],
                                  cwd=REPO, capture_output=True, text=True, env={**os.environ, "PYTHONPATH": str(REPO)})

    def test_finds_advisor_call_fable_and_flagged_model_without_content(self):
        p = self.run_audit()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("G-014-T-0250 session=sess-a advisor_calls=1 fable_messages=1", p.stdout)
        self.assertIn("FLAGGED=claude-fable-5-1", p.stdout)
        self.assertIn("2026-10-25T10:01:00Z", p.stdout)
        self.assertIn("2026-10-25T10:03:00Z", p.stdout)
        self.assertNotIn(SECRET, p.stdout + p.stderr)

    def test_clean_and_definition_only_sessions_are_not_reported(self):
        p = self.run_audit("--json")
        rows = json.loads(p.stdout)
        self.assertEqual([r["session"] for r in rows], ["sess-a"])
        self.assertEqual(rows[0]["advisor_calls"], 1)  # the Bash tool_use and the tool definition do not count
        self.assertNotIn(SECRET, p.stdout)

    def test_missing_directory_fails(self):
        p = subprocess.run([sys.executable, "-m", "web.advisor_audit", "--projects", "/nonexistent-zz"],
                           cwd=REPO, capture_output=True, text=True)
        self.assertEqual(p.returncode, 2)
        self.assertIn("transcript directory not found", p.stderr)


if __name__ == "__main__":
    unittest.main()
