"""Tests for ci/cursor_session_audit.py (fake HOME only)."""
import json
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "ci", "cursor_session_audit.py")
T0 = 1794267815513  # ms


def key_of(path):
    return "-".join(p for p in path.split("/") if p)


def run(home, *args):
    env = {"HOME": home, "PATH": os.environ.get("PATH", "")}
    return subprocess.run([sys.executable, "-I", SCRIPT, *args], env=env, capture_output=True, text=True)


class AuditTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.home = os.path.realpath(self._tmp.name)
        self.factory = os.path.join(self.home, "factory")
        self.wt = os.path.join(self.home, "factory-wt-T1")
        os.makedirs(self.factory)
        os.makedirs(os.path.join(self.wt, ".cursor"))
        open(os.path.join(self.wt, ".cursor", "hooks.json"), "w").close()

    def session(self, root, sid, cmd=None, meta=True, transcript=True, created=T0):
        if transcript:
            d = os.path.join(self.home, ".cursor", "projects", key_of(root), "agent-transcripts", sid)
            os.makedirs(d)
            lines = [{"role": "user", "message": {"content": [{"type": "text", "text": "<user_query>You are `cursor-coder-x`, the coder. SECRET prose</user_query>"}]}}]
            if cmd:
                lines.append({"role": "assistant", "message": {"content": [{"type": "tool_use", "name": "Shell", "input": {"command": cmd}}]}})
            with open(os.path.join(d, sid + ".jsonl"), "w") as fh:
                fh.write("\n".join(json.dumps(x) for x in lines) + "\n")
        if meta:
            d = os.path.join(self.home, ".cursor", "chats", "h1", sid)
            os.makedirs(d)
            with open(os.path.join(d, "meta.json"), "w") as fh:
                json.dump({"createdAtMs": created, "cwd": root, "title": "SECRET title"}, fh)

    def events(self, lines):
        d = os.path.join(self.home, ".local", "state", "factory", "cursor", "p1")
        os.makedirs(d)
        with open(os.path.join(d, "hook-events.jsonl"), "w") as fh:
            fh.write("\n".join(json.dumps(x) for x in lines) + "\n")

    def rows(self, *args):
        p = run(self.home, "--json", *args)
        self.assertEqual(p.returncode, 0, p.stderr)
        data = json.loads(p.stdout)
        return {r["session"]: r for r in data["sessions"]}, data["summary"], p.stdout

    def test_factory_root_without_hooks_json_is_no_and_sudo_listed(self):
        self.session(self.factory, "s-a", cmd="sudo true")
        rows, summary, _ = self.rows()
        r = rows["s-a"]
        self.assertEqual(r["hooks_loaded"], "NO")
        self.assertEqual(r["agent"], "cursor-coder-x")
        self.assertEqual(r["sudo_or_herdr"], [{"command": "sudo true", "outcome": "attempted; outcome not recorded"}])
        self.assertEqual(summary, {"sessions": 1, "hooks_loaded_no": 1, "sudo_or_herdr_attempted": 1, "gone": 0})

    def test_worktree_with_hooks_json_is_not_no(self):
        self.session(self.wt, "s-b", cmd="ls")
        rows, _, _ = self.rows()
        self.assertEqual(rows["s-b"]["hooks_loaded"], "unknown")
        self.assertEqual(rows["s-b"]["sudo_or_herdr"], [])

    def test_missing_transcript_is_gone(self):
        self.session(self.factory, "s-c", transcript=False)
        rows, summary, _ = self.rows()
        self.assertEqual(rows["s-c"]["hooks_loaded"], "gone")
        self.assertEqual(summary["gone"], 1)

    def test_missing_meta_is_gone(self):
        self.session(self.factory, "s-m", meta=False)
        rows, _, _ = self.rows()
        self.assertEqual(rows["s-m"]["hooks_loaded"], "gone")

    def test_hook_events_deny_is_yes_and_blocked(self):
        self.session(self.factory, "s-d", cmd="herdr pane list")
        self.events([{"event": "beforeShellExecution", "conversation_id": "s-d", "decision": "deny"}])
        rows, summary, _ = self.rows()
        self.assertEqual(rows["s-d"]["hooks_loaded"], "yes")
        self.assertEqual(rows["s-d"]["sudo_or_herdr"][0]["outcome"], "blocked")
        self.assertEqual(summary["hooks_loaded_no"], 0)

    def test_vanished_project_is_unknown_not_invented(self):
        self.session(os.path.join(self.home, "removed-worktree"), "s-v")
        rows, _, _ = self.rows()
        self.assertEqual(rows["s-v"]["hooks_loaded"], "unknown")

    def test_since_filters_and_summary_line_last(self):
        self.session(self.factory, "old", created=1000)
        self.session(self.factory, "new", created=T0)
        p = run(self.home, "--since", "2026-11-03")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout.strip().splitlines()[-1],
                         "sessions=1 hooks_loaded_no=1 sudo_or_herdr_attempted=0 gone=0")
        self.assertIn("new", p.stdout)
        self.assertNotIn("old\t", p.stdout)

    def test_output_has_no_message_text_and_excerpt_is_80_chars(self):
        self.session(self.factory, "s-e", cmd="sudo " + "x" * 200)
        p = run(self.home)
        self.assertNotIn("SECRET", p.stdout)
        self.assertIn("sudo " + "x" * 75 + "'", p.stdout)
        self.assertNotIn("x" * 76, p.stdout)

    def test_bad_since_exits_2(self):
        p = run(self.home, "--since", "yesterday")
        self.assertEqual(p.returncode, 2)

    def test_script_avoids_store_db_and_walk(self):
        with open(SCRIPT) as fh:
            src = fh.read()
        self.assertNotIn("store." + "db", src)
        self.assertNotIn("os." + "walk", src)


if __name__ == "__main__":
    unittest.main()
