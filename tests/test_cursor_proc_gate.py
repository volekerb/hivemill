"""T-0827: spawn gate and flagless audit count Cursor agent processes."""
import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import subprocess
import sys
import unittest
import warnings
from contextlib import contextmanager
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from harness import REPO, FactoryTestCase

HOOKS = REPO / "roles" / "hooks"


def fake_proc(env, pid, ppid, argv):
    d = env.proc_dir / str(pid)
    d.mkdir(exist_ok=True)
    (d / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
    name = os.path.basename(argv[0]) if argv else "proc"
    (d / "stat").write_text(f"{pid} ({name}) S {ppid} {pid} {pid} 0 -1 0\n")


def fake_claude(env, pid, ppid=1, **kw):
    argv = kw.get("argv") or ("claude", "--session-id", kw.get("sid", "sid-a"),
                              "--settings", f"/x/settings/{kw.get('role', 'coder')}.json")
    fake_proc(env, pid, ppid, argv)


def fake_cursor(env, pid, workspace, ppid=1, binary="cursor-agent"):
    argv = (binary, "--workspace", str(workspace), "--model", "composer-2.5", "--trust", "--force")
    fake_proc(env, pid, ppid, argv)


def cursor_workspace(env, name):
    return env.home_dir / "run" / "cursor" / name


def write_cursor_hooks(workspace, role="coder", ok=True):
    import scout.cursor_harness as ch

    ch.materialize_cursor_policy(
        workspace,
        role,
        hooks_dir=HOOKS,
        claude_settings={"permissions": {"deny": [], "allow": []}},
        system_prompt="# test\n",
    )
    if not ok:
        hooks = workspace / ".cursor" / "hooks.json"
        data = json.loads(hooks.read_text())
        for entries in data.get("hooks", {}).values():
            if entries:
                entries[0]["failClosed"] = False
        hooks.write_text(json.dumps(data, indent=2) + "\n")


@contextmanager
def load_factory_from(env, src_path):
    keys = set(env.env()) | {"FACTORY_AGENT", "FACTORY_ROLE", "CLAUDECODE", "HERDR_PANE_ID"}
    saved = {k: os.environ.get(k) for k in keys}
    for k in keys - set(env.env()):
        os.environ.pop(k, None)
    os.environ.update(env.env())
    loader = importlib.machinery.SourceFileLoader(f"factory_{id(src_path)}", str(src_path))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    mod = importlib.util.module_from_spec(spec)
    opened, real_db = [], None
    try:
        with warnings.catch_warnings():
            warnings.simplefilter("ignore", ResourceWarning)
            loader.exec_module(mod)
        real_db = mod.db
        mod.db = lambda *a, **k: opened.append(real_db(*a, **k)) or opened[-1]
        with contextlib.redirect_stdout(io.StringIO()):
            yield mod
    finally:
        for con in opened:
            con.close()
        for k, v in saved.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v


def gate_flagless_snapshot(env, mod):
    con = mod.db()
    try:
        rows = con.execute("SELECT * FROM agents WHERE status='live'").fetchall()
        ok, why, stats = mod.gate(con)
        scan_out = mod.flagless_scan(con, rows)
        stable = {k: stats[k] for k in stats if k not in ("mem_mb", "usage", "tmp", "load1", "load_cap")}
        return ok, why, stable, scan_out
    finally:
        con.close()


class CursorProcGateTests(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_task(status="todo", pr_url=None)
        self.env.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        agent = self.env.stub_bin / "agent"
        agent.write_text("#!/bin/sh\necho ok\n")
        agent.chmod(0o755)
        self.env.set_herdr(agents=[])

    def _main_factory_path(self):
        p = self.env.home / "factory_cli_origin_main.py"
        if not p.is_file():
            p.write_text(subprocess.check_output(
                ["git", "-C", str(REPO), "show", "origin/main:bin/factory_cli.py"], text=True))
        return p

    def test_claude_only_fixture_matches_origin_main(self):
        self.ok("config", "agent_runtimes", "", agent="zz-test-director")
        fake_claude(self.env, 100, sid="sid-live", role="coder")
        fake_claude(self.env, 101, sid="sid-flagless")  # no --settings
        self.env.seed_agent("zz-live", "coder", goal="G-001", pane_id="p-live")
        self.env.seed_agent("zz-flag", "coder", goal="G-001", pane_id="p-flag")
        self.env.sql("UPDATE agents SET session_id=? WHERE name=?", ("sid-live", "zz-live"))
        self.env.sql("UPDATE agents SET session_id=? WHERE name=?", ("sid-flagless", "zz-flag"))
        self.env.set_herdr(agents=[
            {"name": "zz-live", "agent": "claude", "pane_id": "p-live"},
            {"name": "zz-flag", "agent": "claude", "pane_id": "p-flag"},
        ])
        main_path = self._main_factory_path()
        with load_factory_from(self.env, REPO / "bin" / "factory_cli.py") as cur:
            cur_snap = gate_flagless_snapshot(self.env, cur)
        with load_factory_from(self.env, main_path) as main:
            main_snap = gate_flagless_snapshot(self.env, main)
        self.assertEqual(cur_snap, main_snap)

    def test_cap_counts_cursor_at_max_total_claude(self):
        n = 3
        self.ok("config", "max_total_claude", str(n), agent="zz-test-director")
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")
        self.ok("config", "max_live_agents", "20", agent="zz-test-director")
        for i in range(n):
            ws = self.env.home / f"ws-{i}"
            ws.mkdir()
            fake_cursor(self.env, 200 + i, ws)
            fake_proc(self.env, 300 + i, 200 + i, ("node", "/usr/bin/cursor-agent"))
        with self.env.load_factory() as mod:
            got = mod.total_claude_procs()
            self.assertEqual(got["total"], n)
        p = self.ok("spawn", "coder", "--name", "zz-cap-coder", "--goal", "G-001",
                    "--team", "g-001-a", "--task", "T-0001", agent="zz-test-director")
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("claude processes", p.stdout)

    def test_relaunch_own_slot_and_second_spawn_refused_at_cap(self):
        n = 2
        self.ok("config", "max_total_claude", str(n), agent="zz-test-director")
        for i in range(n):
            fake_cursor(self.env, 400 + i, self.env.home / f"cap-ws-{i}")
        with self.env.load_factory() as mod:
            con = mod.db()
            ok, why, stats = mod.gate(con, own_slot=True)
            self.assertTrue(ok, why)
            self.assertEqual(stats["total"], n)
            ok2, why2, _ = mod.gate(con)
            self.assertFalse(ok2)
            self.assertIn("claude processes", why2)
            con.close()

    def test_fresh_cursor_spawn_refused_on_load(self):
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")
        cap = 10.0  # load_cap with FACTORY_LOADAVG=0 in env; use high load
        p = self.ok("spawn", "coder", "--name", "zz-load-coder", "--goal", "G-001",
                    "--team", "g-001-a", "--task", "T-0001", agent="zz-test-director",
                    extra_env={"FACTORY_LOADAVG": str(cap + 5)})
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("load1", p.stdout)

    def test_foreign_cursor_and_claude_panes_in_other(self):
        self.env.set_herdr(agents=[
            {"name": "zz-foreign-c", "agent": "cursor", "pane_id": "pc"},
            {"name": "zz-foreign-cl", "agent": "claude", "pane_id": "pl"},
        ])
        with self.env.load_factory() as mod:
            con = mod.db()
            _, _, stats = mod.gate(con)
            self.assertEqual(stats["other"], 2)
            con.close()

    def test_flagless_cursor_ok(self):
        name = "zz-cur-ok"
        ws = cursor_workspace(self.env, name)
        write_cursor_hooks(ws)
        fake_cursor(self.env, 500, ws)
        spawn_args = json.dumps({"runtime": "cursor", "cursor_workspace": str(ws)})
        self.env.sql("INSERT INTO agents(name,role,goal_id,status,spawn_args,pane_id) VALUES(?,?,?,?,?,?)",
                     (name, "coder", "G-001", "live", spawn_args, "p-ok"))
        with self.env.load_factory() as mod:
            row = mod.db().execute("SELECT * FROM agents WHERE name=?", (name,)).fetchone()
            state, pid = mod.flagless_state(row)
            self.assertEqual(state, "ok")
            self.assertEqual(pid, 500)
            mod.db().close()

    def test_flagless_cursor_flagged_scan_no_kill(self):
        name = "zz-cur-bad"
        ws = cursor_workspace(self.env, name)
        write_cursor_hooks(ws, ok=False)
        fake_cursor(self.env, 501, ws)
        spawn_args = json.dumps({"runtime": "cursor", "cursor_workspace": str(ws)})
        self.env.sql("INSERT INTO agents(name,role,goal_id,status,spawn_args,pane_id,flagless) VALUES(?,?,?,?,?,?,0)",
                     (name, "coder", "G-001", "live", spawn_args, "p-bad",))
        with self.env.load_factory() as mod:
            con = mod.db()
            row = con.execute("SELECT * FROM agents WHERE name=?", (name,)).fetchone()
            state, pid = mod.flagless_state(row)
            self.assertEqual(state, "flagless")
            self.assertEqual(pid, 501)
            found = mod.flagless_scan(con, [row])
            self.assertEqual(len(found), 1)
            self.assertEqual(row["status"], "live")
            ev = con.execute("SELECT action FROM events WHERE entity_id=? ORDER BY id DESC", (name,)).fetchone()
            self.assertEqual(ev["action"], "flagless")
            con.close()
        closes = [c for c in self.env.calls("herdr") if c["argv"][:2] == ("pane", "close")]
        self.assertEqual(closes, [])

    def test_unreadable_proc_skips_cap(self):
        self.env.proc_dir.chmod(0)
        self.addCleanup(self.env.proc_dir.chmod, 0o755)
        with self.env.load_factory() as mod:
            got = mod.total_claude_procs()
            self.assertIsNone(got["total"])
            con = mod.db()
            ok, why, stats = mod.gate(con)
            self.assertTrue(ok, why)
            self.assertIsNone(stats["total"])
            con.close()

    def test_node_child_not_double_counted(self):
        fake_cursor(self.env, 600, self.env.home / "parent-ws")
        fake_proc(self.env, 601, 600, ("node", "/usr/bin/cursor-agent"))
        with self.env.load_factory() as mod:
            got = mod.total_claude_procs()
            self.assertEqual(got["total"], 1)
            self.assertEqual(got["pids"], [600])

    def test_two_agents_same_workspace_both_in_total(self):
        ws = cursor_workspace(self.env, "zz-a1")
        ws.mkdir(parents=True, exist_ok=True)
        fake_cursor(self.env, 700, ws)
        fake_cursor(self.env, 701, ws)
        with self.env.load_factory() as mod:
            self.assertEqual(mod.total_claude_procs()["total"], 2)

    def test_combined_total_requires_cursor_procs(self):
        """C4: dropping cursor from the host scan would miss cap enforcement."""
        ws = self.env.home / "c4-ws"
        ws.mkdir()
        fake_cursor(self.env, 800, ws)
        with self.env.load_factory() as mod:
            self.assertEqual(mod.total_claude_procs()["total"], 1)
            scan = mod._scan_host_agent_procs()
            claude_only = {p: v for p, v in (scan or {}).items()
                           if any(os.path.basename(x) == "claude" for x in v[0][:3])}
            self.assertEqual(len(claude_only), 0)


if __name__ == "__main__":
    unittest.main()
