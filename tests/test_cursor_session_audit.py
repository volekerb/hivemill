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

    def transcript_file(self, sid, records, key="proj-a"):
        d = os.path.join(self.home, ".cursor", "projects", key, "agent-transcripts", sid)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, sid + ".jsonl"), "w") as fh:
            fh.write("".join(json.dumps(r) + "\n" for r in records))

    def test_agent_name_from_first_user_message_else_unknown(self):
        self.log("session-2026-11-10T10-00-00-000Z-777-1.log", [init("c7")])
        self.log("session-2026-11-10T10-00-01-000Z-778-1.log", [init("c8")])
        self.log("session-2026-11-10T10-00-02-000Z-779-1.log", [init("c9")])
        self.transcript_file("c7", [
            {"role": "assistant", "message": {"content": [{"type": "text", "text": "You are `wrong`"}]}},
            {"role": "user", "message": {"content": [{"type": "text",
             "text": "<timestamp>x</timestamp>\nYou are `cursor-pm-t-1`, the pm."}]}},
            {"role": "user", "message": {"content": [{"type": "text", "text": "You are `second`"}]}}])
        self.transcript_file("c8", [{"role": "user", "message": {"content": [{"type": "text", "text": "hello"}]}},
                                    {"role": "user", "message": {"content": [{"type": "text", "text": "You are `late`"}]}}])
        d = self.result()
        self.assertEqual({r["pid"]: r["agent"] for r in d["logs"]}, {777: "cursor-pm-t-1", 778: "?", 779: "?"})

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

    def test_since_unpadded_equals_padded(self):
        self.log("session-2026-11-01T08-00-00-000Z-1-1.log", [init("old")])
        self.log("session-2026-11-03T10-00-00-000Z-2-1.log", [init("mid")])
        self.log("session-2026-11-10T10-00-00-000Z-3-1.log", [init("new")])
        a = self.result("--since", "2026-11-3")
        b = self.result("--since", "2026-11-03")
        self.assertEqual([r["log"] for r in a["logs"]], [r["log"] for r in b["logs"]])
        self.assertEqual(len(a["logs"]), 2)

    def test_text_row_with_tab_and_newline_stays_one_line(self):
        self.log("session-2026-11-10T10-00-00-000Z-1-1.log", [init("plain", "/work/a")])
        self.log("session-2026-11-10T10-00-00-000Z-2-1.log", [init("c\tx\ry", "/work/a\tb\nc")])
        out = run(self.home, self.logs).stdout.splitlines()
        rows = [ln for ln in out if ln.startswith(("plain\t", "c x y\t"))]
        self.assertEqual(len(rows), 2)
        self.assertEqual(len({ln.count("\t") for ln in rows}), 1)
        self.assertEqual(len(out), 3)  # two rows plus the summary line
        self.assertIn("/work/a b c", rows[1])

    def test_text_outcome_labels(self):
        self.log("session-2026-11-10T10-00-00-000Z-1-1.log", [init("c1"), shell(PRIV + " ls")])
        text = run(self.home, self.logs).stdout
        self.assertIn("outcomes=ran_unguarded=1,blocked_by_hook=0,hook_allowed=0", text)

    def test_json_and_summary_keys_unchanged(self):
        self.log("session-2026-11-10T10-00-00-000Z-1-1.log", [init("c1"), shell(PRIV + " ls")])
        self.transcript("proj", "gone1")
        d = self.result()
        self.assertEqual(set(d), {"logs", "transcripts_log_gone", "summary"})
        self.assertEqual(set(d["summary"]), {"logs", "sessions", "unguarded", "risky_unguarded",
                                             "helper", "transcripts_log_gone", "oldest_log"})
        self.assertEqual(set(d["logs"][0]), {"log", "pid", "session", "start", "kind", "workspace",
                                             "agent", "guarded", "hooks", "risky", "ran"})
        self.assertEqual(d["logs"][0]["risky"][0]["outcome"], "ran unguarded")
        self.assertEqual(set(d["transcripts_log_gone"][0]), {"project", "session", "state"})

    def test_bad_since_exits_2(self):
        self.assertEqual(run(self.home, self.logs, "--since", "yesterday").returncode, 2)

    def test_script_avoids_store_db_and_walk(self):
        with open(SCRIPT) as fh:
            src = fh.read()
        self.assertNotIn("store." + "db", src)
        self.assertNotIn("os." + "walk", src)


if __name__ == "__main__":
    unittest.main()
