"""T-0878: Cursor tier, limits, and time spent on the status header, Overview System, and Resources."""
import json
import re
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import NOW, make_db  # noqa: E402
from test_web_server import ServerCase, get  # noqa: E402
from web import db as wdb  # noqa: E402
from web import cursor_limits  # noqa: E402
from web.views import home, resources  # noqa: E402

FRESH = {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1}
MACHINE = {"load": [1.5, 1.0, 1.0], "cpus": 8, "mem_available_mb": 2048}
LIMIT_STATE = {
    "composer-2.5": {"status": "limited", "reset": "2026-11-05", "switch_target": "composer-2.5"},
    "grok-4.7-high": {"status": "ok"},
}


def section(page, sid):
    m = re.search(r'<section id="%s".*?</section>' % sid, page, re.S)
    assert m, f"no section {sid}"
    return m.group(0)


class UiBase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "factory.db"
        self.w = make_db(self.path, seed=True)
        self.addCleanup(self.w.close)
        self.w.execute(
            "INSERT INTO config(key,value) VALUES(?,?)",
            (cursor_limits.CONFIG_KEY, json.dumps(LIMIT_STATE)),
        )
        self.w.execute(
            "INSERT INTO agents(name,role,status,started,ended,model,spawn_args) VALUES(?,?,?,?,?,?,?)",
            (
                "cursor-coder-x",
                "coder",
                "exited",
                "2026-10-24T08:00:00+02:00",
                "2026-10-24T10:00:00+02:00",
                "composer-2.5",
                json.dumps({"runtime": "cursor", "org": "cursor"}),
            ),
        )
        self.w.commit()
        p = mock.patch.object(home, "machine", lambda: MACHINE)
        p.start()
        self.addCleanup(p.stop)
        p2 = mock.patch.object(resources, "machine", lambda: MACHINE)
        p2.start()
        self.addCleanup(p2.stop)

    def ctx(self, usage=FRESH, mem=2048, now=NOW):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        return SimpleNamespace(
            db=con,
            now=now,
            usage_fn=lambda: usage,
            mem_fn=lambda: mem,
            tokens=None,
            defaults={"max_live_agents": "6", "max_five_hour_pct": "92", "max_seven_day_pct": "92"},
            factory_home=None,
            query={},
        )


class TestThreeSurfaces(UiBase):
    def assert_cursor_bits(self, html, *, claude=True):
        self.assertIn('<span class="gl">Cursor usage</span>', html)
        self.assertIn("Pro", html)
        self.assertIn("limited until 11-05", html)
        self.assertIn("2 h", html)
        self.assertNotIn("sessions", html.lower())
        if claude:
            self.assertIn("Claude usage", html)
        self.assertNotRegex(html, r"\$\d")
        self.assertNotIn("cursor-usage-row", html)
        self.assertNotIn("cursor-hdr", html)

    def test_overview_system_row(self):
        _, page = self.page()
        self.assert_cursor_bits(section(page, "system"))

    def test_resources_capacity(self):
        c = self.ctx()
        html = str(resources.render(c, resources.data(c)))
        self.assert_cursor_bits(html)

    def page(self, **kw):
        c = self.ctx(**kw)
        d = home.data(c)
        return d, str(home.render(c, d))


class TestHeaderServer(ServerCase):
    def setUp(self):
        super().setUp()
        self.con.execute(
            "INSERT INTO config(key,value) VALUES(?,?)",
            (cursor_limits.CONFIG_KEY, json.dumps(LIMIT_STATE)),
        )
        self.con.commit()

    def test_header_on_goals_page(self):
        text = self.get("/goals")[2].decode()
        self.assertIn("Cursor usage", text)
        self.assertIn("Pro · limited until 11-05", text)
        self.assertIn('class="pill hide-phone tone-', text)
        self.assertNotIn("cursor-hdr", text)
