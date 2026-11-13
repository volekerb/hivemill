"""T-1214 (G-031 SC4): hc OSC 7501 status mapping; a missing status is never idle."""
import importlib.util
import json
import time
import unittest
from pathlib import Path

from harness import FactoryTestCase

ROOT = Path(__file__).resolve().parents[1]
_FACTORY_CLI = ROOT / "bin" / "factory_cli.py"
NAME = "zz-hc-worker"
DIR = "zz-hc-director"
NOW1 = "2026-03-01T10:00:00+00:00"


def _load_factory_cli():
    spec = importlib.util.spec_from_file_location("factory_cli", _FACTORY_CLI)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class HcStatusMapperTests(FactoryTestCase):
  def setUp(self):
    super().setUp()
    self.ok("config", "terminal_backend", "hc", agent="owner")
    self.f = _load_factory_cli()

  def _row_from_ls(self, status):
    env = {"hidden_finished": 0, "terminals": [{"name": NAME, "agent": "claude", "state": "live", "status": status}]}
    return self.f._hc_unwrap_result(["ls"], env)["agents"][0]["agent_status"]

  def test_osc_object_states(self):
    self.assertEqual(self._row_from_ls({"state": "idle"}), "idle")
    self.assertEqual(self._row_from_ls({"state": "working"}), "working")
    self.assertEqual(self._row_from_ls({"state": "done"}), "done")
    self.assertEqual(self._row_from_ls({"state": "blocked"}), "blocked")
    self.assertEqual(self._row_from_ls({"state": "error"}), "error")
    self.assertEqual(self._row_from_ls({"state": "clear"}), "unknown")
    self.assertEqual(self._row_from_ls(None), "unknown")

  def test_string_status_passthrough(self):
    self.assertEqual(self._row_from_ls("idle"), "idle")
    self.assertEqual(self._row_from_ls("bogus"), "unknown")

  def test_real_ls_rows_kind_and_remote(self):
    """hc 0.1.21 rows: agent `claude-code` maps to `claude`; a row from another machine (`m/t19`) is dropped."""
    env = {"hidden_finished": 0, "machines": [{"name": "m", "state": "connected"}], "terminals": [
      {"address": "t2", "agent": "claude-code", "cwd": "/tmp", "name": NAME, "state": "live",
       "status": "idle", "turn": 0, "where": "space / tab"},
      {"address": "m/t19", "agent": "claude-code", "cwd": "/tmp", "name": DIR, "state": "live",
       "status": "idle", "turn": 0},
      {"address": "t3", "agent": "claude-code", "cwd": "/tmp", "state": "live", "turn": 0},
    ]}
    rows = self.f._hc_unwrap_result(["ls"], env)["agents"]
    self.assertEqual(rows, [{"name": NAME, "agent": "claude", "agent_status": "idle"}, {"agent": "claude"}])


class HcStatusTickTests(FactoryTestCase):
  def setUp(self):
    super().setUp()
    self.ok("config", "terminal_backend", "hc", agent="owner")
    self.env.seed_goal()
    self.env.seed_team()
    self.env.seed_agent(NAME, "coder", goal="G-001", team="g-001-a", task="T-0001", pane_id=NAME)

  def _hc_agent(self, osc_status):
    return {"name": NAME, "agent": "claude", "osc_status": osc_status}

  def _hc_sends(self):
    return [c for c in self.env.calls("hc") if c["argv"][:1] == ["send"]]

  def test_null_osc_status_skips_unread_nudge(self):
    self.env.sql(
      "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'msg','s','b','2026-01-01')",
      (NAME,),
    )
    self.env.set_herdr(agents=[self._hc_agent(None)])
    self.ok("tick", agent="owner")
    self.assertEqual(self._hc_sends(), [])

  def test_idle_osc_status_sends_unread_nudge(self):
    self.env.sql(
      "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'msg','s','b','2026-01-01')",
      (NAME,),
    )
    self.ok("config", "nudge_cooldown_s", "-60", agent="owner")
    self.env.set_herdr(agents=[self._hc_agent({"state": "idle"})])
    self.ok("tick", agent="owner")
    self.assertEqual(len(self._hc_sends()), 1)

  def test_remote_row_with_same_name_is_not_this_agent(self):
    """An idle terminal of the same name on another machine (address `m/t19`) must not trigger a nudge."""
    self.env.sql(
      "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'msg','s','b','2026-01-01')",
      (NAME,),
    )
    self.ok("config", "nudge_cooldown_s", "-60", agent="owner")
    self.env.set_herdr(agents=[{**self._hc_agent({"state": "idle"}), "address": "m/t19"}])
    self.ok("tick", agent="owner")
    self.assertEqual(self._hc_sends(), [])


class HcStatusUnblockTests(FactoryTestCase):
  def setUp(self):
    super().setUp()
    self.ok("config", "terminal_backend", "hc", agent="owner")
    self.env.seed_goal()
    self.env.seed_team()
    self.env.seed_agent("zz-director", "director", goal="G-001", pane_id="p-dir")
    self.env.seed_agent(NAME, "coder", goal="G-001", team="g-001-a", task="T-0001", pane_id=NAME)
    self.env.sql("UPDATE agents SET blocked_since=? WHERE name=?", (time.time() - 3600, NAME))

  def test_unblock_null_status_prints_and_skips_send(self):
    self.env.set_herdr(agents=[{"name": NAME, "agent": "claude", "osc_status": None}])
    p = self.ok("unblock", NAME, "--reason", "no", agent="zz-director")
    self.assertIn("missing status is not idle", p.stdout)
    sends = [c for c in self.env.calls("hc") if c["argv"][:1] == ["send"]]
    self.assertEqual(sends, [])


class HcStatusStartConfirmTests(FactoryTestCase):
  def setUp(self):
    super().setUp()
    self.ok("config", "terminal_backend", "hc", agent="owner")
    self.ok("config", "tick_async_start", "1", agent="owner")
    self.env.seed_goal()
    self.env.seed_team()
    self.env.seed_agent(NAME, "pm", goal="G-001", team="g-001-a", pane_id=NAME)

  def _pending(self):
    self.env.sql(
      "UPDATE agents SET start_pending=?, start_pending_info=? WHERE name=?",
      (time.time(), json.dumps({"kick": "kickoff text"}), NAME),
    )

  def test_null_osc_keeps_start_pending_without_send(self):
    self._pending()
    self.env.set_herdr(agents=[{"name": NAME, "agent": "claude", "osc_status": None}])
    self.ok("tick", agent="owner")
    self.assertIsNotNone(self.env.sql("SELECT start_pending FROM agents WHERE name=?", (NAME,))[0][0])
    self.assertEqual([c for c in self.env.calls("hc") if c["argv"][:1] == ["send"]], [])

  def test_working_osc_clears_start_pending(self):
    self._pending()
    self.env.set_herdr(agents=[{"name": NAME, "agent": "claude", "osc_status": {"state": "working"}}])
    self.ok("tick", agent="owner")
    self.assertIsNone(self.env.sql("SELECT start_pending FROM agents WHERE name=?", (NAME,))[0][0])


class HcStatusRecycleTests(FactoryTestCase):
  def setUp(self):
    super().setUp()
    self.ok("config", "terminal_backend", "hc", agent="owner")
    self.ok("config", "recycle_context_tokens", "100000", agent="owner")
    self.env.seed_goal()
    self.env.seed_team()
    self.env.seed_agent(DIR, "director", goal="G-001", pane_id=DIR)
    self.env.sql("UPDATE agents SET session_id=? WHERE name=?", ("sess-dir", DIR))
    d = self.env.user_home / ".claude" / "projects" / "-zz-proj"
    d.mkdir(parents=True, exist_ok=True)
    lines = [
      {"type": "assistant", "message": {"id": "m0", "usage": {"input_tokens": 5, "cache_read_input_tokens": 1}}},
      {"type": "assistant", "message": {"id": "m1", "usage": {
        "input_tokens": 10, "cache_read_input_tokens": 150000 - 30, "cache_creation_input_tokens": 20}}},
    ]
    (d / "sess-dir.jsonl").write_text("\n".join(json.dumps(x) for x in lines) + "\n")

  def test_null_osc_never_queues_recycle(self):
    self.env.set_herdr(agents=[{"name": DIR, "agent": "claude", "osc_status": None}])
    self.ok("tick", agent="owner", extra_env={"FACTORY_NOW": NOW1})
    rel = self.env.sql("SELECT relaunch_requested_at FROM agents WHERE name=?", (DIR,))[0][0]
    self.assertIsNone(rel)
    queued = self.env.sql("SELECT 1 FROM events WHERE entity_id=? AND action='recycle-queued'", (DIR,))
    self.assertEqual(queued, [])


if __name__ == "__main__":
  unittest.main()
