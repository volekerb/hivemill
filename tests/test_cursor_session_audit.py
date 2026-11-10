"""Tests for ci/cursor_session_audit.py (fixture logs in a temp dir and fake HOME)."""
import json
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "ci", "cursor_session_audit.py")
PRIV = "su" + "do"
HERD = "her" + "dr"


def run(home, logdir, *args):
    env = {"HOME": home, "PATH": os.environ.get("PATH", "")}
    return subprocess.run([sys.executable, "-I", SCRIPT, "--log-dir", logdir, *args],
                          env=env, capture_output=True, text=True)


def ev(kind, data, ts="2026-11-10T10:00:00.000Z"):
    return "[%s] %s %s\n" % (ts, kind, json.dumps(data))


def init(conv, ws="/work/factory"):
    d = {"conversationId": conv}
    if ws:
        d["workspacePath"] = ws
    return ev("conversationClassification.init", d)


def hook(step, status="success", fail_closed=False):
    return ev("analytics.track", {"eventName": "cli.hook.executed", "props": {
        "hookStep": step, "hookSource": "user", "status": status, "failClosed": fail_closed}})


def shell(cmd):
    return ev("logger", {"message": "Executing shell command", "metadata": {"command": cmd}})


class AuditTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.home = os.path.join(tmp.name, "home")
        self.logs = os.path.join(tmp.name, "logs")
        os.makedirs(self.home)
        os.makedirs(self.logs)

    def log(self, name, lines):
        with open(os.path.join(self.logs, name), "w") as fh:
            fh.write("".join(lines))

    def transcript(self, key, sid):
        os.makedirs(os.path.join(self.home, ".cursor", "projects", key, "agent-transcripts", sid))

    def result(self, *args):
        p = run(self.home, self.logs, "--json", *args)
        self.assertEqual(p.returncode, 0, p.stderr)
        return json.loads(p.stdout)

    def test_session_start_only_with_risky_command_is_unguarded(self):
        self.log("session-2026-11-10T10-00-00-000Z-111-1.log", [
            init("c1"), hook("sessionStart"), shell(HERD + " agent list")])
        d = self.result()
        row = d["logs"][0]
        self.assertEqual(row["guarded"], "NO")
        self.assertEqual(row["pid"], 111)
        self.assertEqual(row["risky"], [{"word": HERD, "outcome": "ran unguarded"}])
        self.assertEqual(row["ran"], {PRIV: 0, HERD: 1})
        self.assertEqual(row["session"], "c1")
        self.assertEqual(d["summary"]["unguarded"], 1)
        self.assertEqual(d["summary"]["risky_unguarded"], 1)

    def test_deny_before_privileged_command_is_blocked(self):
        self.log("session-2026-11-10T10-00-00-000Z-222-1.log", [
            init("c2"), hook("beforeShellExecution", "denied"), shell("echo hi | " + PRIV + " true")])
        d = self.result()
        row = d["logs"][0]
        self.assertEqual(row["guarded"], "yes")
        self.assertEqual(row["risky"][0]["outcome"], "blocked by hook")
        self.assertEqual(d["summary"]["unguarded"], 0)
        self.assertEqual(d["summary"]["risky_unguarded"], 0)

    def test_allowed_hook_and_window_resets_after_each_shell_command(self):
        self.log("session-2026-11-10T10-00-00-000Z-333-1.log", [
            init("c3"), hook("beforeShellExecution", "success"), shell(HERD + " a"), shell(PRIV + " b")])
        outcomes = [r["outcome"] for r in self.result()["logs"][0]["risky"]]
        self.assertEqual(outcomes, ["hook ran, allowed", "ran unguarded"])

    def test_no_workspace_is_helper_not_session(self):
        self.log("session-2026-11-10T10-00-00-000Z-444-1.log", [init("c4", ws=None)])
        s = self.result()["summary"]
        self.assertEqual((s["logs"], s["sessions"], s["helper"], s["unguarded"]), (1, 0, 1, 0))

    def test_transcript_in_no_log_is_log_gone(self):
        self.log("session-2026-11-10T10-00-00-000Z-555-1.log", [init("seen")])
        self.transcript("proj-a", "seen")
        self.transcript("proj-a", "orphan")
        d = self.result()
        self.assertEqual(d["transcripts_log_gone"], [
            {"project": "proj-a", "session": "orphan", "state": "log gone (transcript only)"}])
        self.assertEqual(d["summary"]["transcripts_log_gone"], 1)

    def test_malformed_lines_skipped(self):
        self.log("session-2026-11-10T10-00-00-000Z-666-1.log", [
            "garbage line\n", "[2026-11-10T10:00:00.000Z] logger {not json}\n",
            "    at B (index.js:1:2)\n", init("c6"), hook("beforeShellExecution")])
        d = self.result()
        self.assertEqual(d["logs"][0]["guarded"], "yes")

    def test_since_and_summary_last_line_and_oldest(self):
        self.log("session-2026-11-01T08-00-00-000Z-1-1.log", [init("old")])
        self.log("session-2026-11-10T10-00-00-000Z-2-1.log", [init("new")])
        p = run(self.home, self.logs, "--since", "2026-11-05")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout.strip().splitlines()[-1],
                         "logs=1 sessions=1 unguarded=1 risky_unguarded=0 helper=0 "
                         "transcripts_log_gone=0 oldest_log=2026-11-10T10:00:00Z")

    def test_agent_name_from_agents_json_else_unknown(self):
        self.log("session-2026-11-10T10-00-00-000Z-777-1.log", [init("c7")])
        self.log("session-2026-11-10T10-00-01-000Z-778-1.log", [init("c8")])
        af = os.path.join(self.logs, "..", "agents.json")
        with open(af, "w") as fh:
            json.dump([{"pid": 777, "name": "cursor-pm-t-1"}], fh)
        d = self.result("--agents-json", af)
        self.assertEqual({r["pid"]: r["agent"] for r in d["logs"]}, {777: "cursor-pm-t-1", 778: "?"})

    def test_output_has_no_command_or_message_text(self):
        self.log("session-2026-11-10T10-00-00-000Z-888-1.log", [
            init("c9"), ev("logger", {"message": "SECRET prose"}), shell(PRIV + " SECRETARG " + HERD + " x"),
            shell(PRIV + " again")])
        for extra in ([], ["--json"]):
            p = run(self.home, self.logs, *extra)
            self.assertNotIn("SECRET", p.stdout)
            self.assertNotIn("again", p.stdout)
        text = run(self.home, self.logs).stdout
        self.assertIn("%s=yes(2) %s=yes(1)" % (PRIV, HERD), text)
        self.assertIn("beforeShellExecution=no", text)

    def test_innocent_commands_not_risky(self):
        self.log("session-2026-11-10T10-00-00-000Z-999-1.log", [init("c10"), shell("ls -la && git status")])
        self.assertEqual(self.result()["logs"][0]["risky"], [])

    def test_bad_since_exits_2(self):
        self.assertEqual(run(self.home, self.logs, "--since", "yesterday").returncode, 2)

    def test_script_avoids_store_db_and_walk(self):
        with open(SCRIPT) as fh:
            src = fh.read()
        self.assertNotIn("store." + "db", src)
        self.assertNotIn("os." + "walk", src)


if __name__ == "__main__":
    unittest.main()
