"""T-1240 (G-038 C3): lifecycle, cost-and-tokens and rework panel on /task/<id>, through the real server."""
import re
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_detail import A_INS, E_INS, T_INS, V_INS, DetailCase  # noqa: E402
from test_web_queries import ts  # noqa: E402
from web.prices import PRICES_AS_OF  # noqa: E402

WORDING = f"Estimated at list prices as of {PRICES_AS_OF} (API-equivalent, not a bill)"


def row(sid, model="claude-opus-5-5", inp=1_000_000, out=100_000):
    return {"session": sid, "model": model, "day": "2026-11-05", "fast": False, "input": inp, "output": out,
            "cache_read": 2000, "cache_write_5m": 0, "cache_write_1h": 0, "requests": 1}


class MetricsCase(DetailCase):
    def seed_task(self, tid="T-90", rejections=()):
        x = self.con.execute
        x("INSERT INTO goals VALUES('G-90','p1','Metrics goal','intent','1. a','active',2,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
        x(T_INS, (tid, "G-90", None, "Metrics", "s", "a", "coder", "done", "coder-90", None, 1, ts(9, 0), ts(12, 0)))
        x(A_INS, ("coder-90", "coder", "G-90", None, tid, "done", "m", ts(10, 0), ts(11, 0)))
        x("UPDATE agents SET session_id='sess-90' WHERE name='coder-90'")
        x(E_INS, (ts(10, 30), "coder-90", "task", tid, "update", "status=in_review pr=u"))
        x(V_INS, (tid, "u", "aaaaaaaa11", "qa", "pass", "ok", "qa-90", ts(11, 30)))
        x(E_INS, (ts(12, 0), "judge", "task", tid, "merged", "@deadbeef"))
        for cause in rejections:
            x(V_INS, (tid, "u", "bbbbbbbb22", "qa", "fail", "no", "qa-90", ts(10, 45)))
            x("INSERT INTO rejection_incidents(cause,kind,task_id,created) VALUES(?,?,?,?)", (cause, "verdict", tid, ts(10, 45)))
        self.con.commit()

    def snapshot(self, rows):
        sessions = {r["session"]: {"first_ts": ts(10, 0), "project": "", "cwd": "", "agent_name": None} for r in rows}
        snap = {"ready": True, "rows": rows, "sessions": sessions}
        self.httpd.app.tokens = lambda: snap

    def text(self, tid="T-90"):
        return re.sub(r"<[^>]+>", " ", self.page(f"/task/{tid}"))


class TestMetricsPanel(MetricsCase):
    def test_lifecycle_rows_have_duration_and_share(self):
        self.seed_task()
        self.snapshot([row("sess-90")])
        page = self.page("/task/T-90")
        sec = re.search(r"<h2>Lifecycle.*?</section>", page, re.S).group(0)
        for stage in ("queued", "coding", "review", "judge", "total"):
            self.assertIn(stage, sec)
        self.assertIn("%", sec)

    def test_exact_estimate_wording_and_cost_row(self):
        self.seed_task()
        self.snapshot([row("sess-90")])
        text = self.text()
        self.assertIn(WORDING, text)
        sec = re.search(r"<h2>Cost and tokens.*?</section>", self.page("/task/T-90"), re.S).group(0)
        self.assertIn("coder-90", sec)
        self.assertIn("1,000,000", sec)
        self.assertIn("$6.00", sec)   # opus: 1M in + 0.1M out at the task_usage price

    def test_cursor_row_says_not_available_never_zero_dollars(self):
        self.seed_task()
        self.con.execute("UPDATE agents SET name='cursor-coder-90' WHERE name='coder-90'")
        self.con.commit()
        self.snapshot([row("unrelated")])
        page = self.page("/task/T-90")
        sec = re.search(r"<h2>Cost and tokens.*?</section>", page, re.S).group(0)
        self.assertIn("cursor-coder-90", sec)
        self.assertIn("not available", sec)
        self.assertNotIn("$0.00", page)

    def test_rework_line_names_causes(self):
        self.seed_task(rejections=("bug", "missing_test"))
        self.snapshot([row("sess-90")])
        text = " ".join(self.text().split())
        self.assertRegex(text, r"Rework: .* · causes: bug ×1, missing_test ×1")

    def test_rejection_without_incident_reads_cause_not_recorded(self):
        self.seed_task(rejections=("bug",))
        self.con.execute(V_INS, ("T-90", "u", "cccccccc33", "reviewer", "fail", "no", "r", ts(10, 50)))
        self.con.commit()
        self.snapshot([row("sess-90")])
        text = " ".join(self.text().split())
        self.assertIn("bug ×1", text)
        self.assertIn("cause not recorded ×1", text)

    def test_unknown_share_is_unknown_not_zero(self):
        self.seed_task(rejections=("bug",))
        self.snapshot([row("sess-90", model="mystery-model-9")])
        text = " ".join(self.text().split())
        self.assertIn("Rework: unknown", text)
        self.assertNotIn("0% of cost", text)

    def test_index_not_ready_still_renders_page(self):
        self.seed_task()
        self.httpd.app.tokens = None
        text = self.text()
        self.assertIn("Lifecycle", text)
        self.assertIn("Cost and tokens", text)
        self.assertNotIn("Estimated at list prices", text)

    def test_not_ready_index_keeps_causes_and_says_cost_unknown(self):
        self.seed_task(rejections=("bug",))
        self.httpd.app.tokens = None
        text = " ".join(self.text().split())
        self.assertIn("Rework: unknown · causes: bug ×1", text)

    def test_no_rejections_reads_none_and_total_row_sums(self):
        self.seed_task()
        self.snapshot([row("sess-90")])
        text = " ".join(self.text().split())
        self.assertIn("Rework: none (no rejections)", text)
        self.assertRegex(text, r"Total\s+1 1,000,000 100,000 2,000 0 \$6\.00")

    def test_free_text_summary_never_rendered(self):
        self.seed_task()
        self.con.execute("UPDATE verdicts SET summary='SECRET-NOTE-TEXT'")
        self.con.commit()
        self.snapshot([row("sess-90")])
        self.assertNotIn("SECRET-NOTE-TEXT", self.page("/task/T-90"))


if __name__ == "__main__":
    unittest.main()
