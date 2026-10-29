"""G-010 T-0406: the tick's claude audit prunes `sandbox-escaped:<pid>:<start>` throttle rows of dead processes
and rows older than 7 days; no other throttle key is touched."""
import os
import time

from harness import FactoryTestCase

NOW = 1_800_000_000.0
DAY = 86400


class PruneCase(FactoryTestCase):
    def live(self, pid, start):
        d = self.env.proc_dir / str(pid)
        d.mkdir(parents=True, exist_ok=True)
        (d / "stat").write_text(f"{pid} (claude) S 1 {pid} {pid} 0 -1 0 " + "0 " * 12 + f"{start}\n")

    def prune(self, rows):
        with self.env.load_factory() as mod:
            con = mod.db()
            for k, ts in rows.items():
                con.execute("INSERT INTO throttle(key,ts) VALUES(?,?)", (k, str(ts)))
            n = mod.prune_escape_throttle(con, NOW)
            keys = sorted(r["key"] for r in con.execute("SELECT key FROM throttle"))
            con.commit()
        return n, keys

    def test_dead_row_pruned_live_row_kept(self):
        self.live(100, "555")   # live, start matches
        self.live(200, "777")   # pid reused: start differs from the row
        n, keys = self.prune({"sandbox-escaped:100:555": NOW, "sandbox-escaped:200:111": NOW,
                              "sandbox-escaped:300:42": NOW})
        self.assertEqual(keys, ["sandbox-escaped:100:555"])
        self.assertEqual(n, 2)

    def test_other_keys_untouched(self):
        others = {"claude-audit": NOW - 30 * DAY, "drift": NOW - 30 * DAY, "sandbox_refused:x": NOW - 30 * DAY,
                  "sandbox-escapedX": NOW - 30 * DAY}
        n, keys = self.prune(others)
        self.assertEqual(n, 0)
        self.assertEqual(keys, sorted(others))

    def test_none_start_rows_pruned_by_ttl_only(self):
        n, keys = self.prune({"sandbox-escaped:1:None": NOW - 8 * DAY, "sandbox-escaped:2:None": NOW - DAY})
        self.assertEqual(keys, ["sandbox-escaped:2:None"])
        self.assertEqual(n, 1)

    def test_old_row_of_live_process_pruned_by_ttl(self):
        self.live(100, "555")
        n, keys = self.prune({"sandbox-escaped:100:555": NOW - 8 * DAY})
        self.assertEqual(keys, [])

    def test_audit_tick_prunes(self):
        self.env.seed_agent("zz-director", "director", pane_id="p-dir")
        self.env.sql("INSERT INTO throttle(key,ts) VALUES('sandbox-escaped:300:42',?)", (str(time.time()),))
        self.env.sql("INSERT INTO throttle(key,ts) VALUES('drift',?)", (str(time.time()),))
        self.ok("tick")
        keys = [r["key"] for r in self.env.sql("SELECT key FROM throttle WHERE key LIKE 'sandbox-escaped:%' OR key='drift'")]
        self.assertEqual(keys, ["drift"])

    def test_live_row_kept_no_second_alert(self):
        self.env.seed_agent("zz-director", "director", pane_id="p-dir")
        self.env.seed_agent("zz-c", "coder", pane_id="p-c")
        with self.env.load_factory() as mod:
            con = mod.db()
            mod.herdr = lambda *a, **k: None
            self.live(100, "555")
            (self.env.proc_dir / "100" / "ns").mkdir()
            os.symlink("user:[4026531837]", self.env.proc_dir / "100" / "ns" / "user")
            (self.env.proc_dir / "1" / "ns").mkdir(parents=True)
            os.symlink("user:[4026531837]", self.env.proc_dir / "1" / "ns" / "user")
            self.assertTrue(mod.sandbox_escape_close(con, "zz-c", "p-c", 100))
            self.assertEqual(mod.prune_escape_throttle(con), 0)  # process still live: row stays
            self.assertTrue(mod.sandbox_escape_close(con, "zz-c", "p-c", 100))
            con.commit()
        n = self.env.sql("SELECT COUNT(*) c FROM messages WHERE subject LIKE 'sandbox escaped%'")
        self.assertEqual(n[0]["c"], 1)
