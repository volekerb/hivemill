"""G-019 B4: SVG chart primitives (web/charts.py) and the tile / needs-you row components."""
import re
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from web import charts  # noqa: E402
from web.components import needs_you_row, tile  # noqa: E402

DAYS = [(f"2026-10-{d:02d}", n) for d, n in zip(range(14, 28), (0, 1, 0, 3, 0, 1, 1, 0, 2, 1, 0, 1, 0, 2))]


def text(markup):
    return re.sub(r"<[^>]+>", " ", str(markup))


class TestMeter(unittest.TestCase):
    def test_meter_fill_width_is_exact_not_a_five_percent_step(self):
        m = str(charts.meter(3, 9, "3/9 tasks"))
        self.assertIn('<rect class="fill-data1" x="0" y="0" width="33.3" height="6"></rect>', m)
        self.assertIn('aria-label="3 of 9 tasks done"', m)
        self.assertIn('role="img"', m)
        self.assertIn('viewBox="0 0 100 6"', m)

    def test_meter_label_sits_beside_the_svg_not_inside(self):
        m = str(charts.meter(3, 9, "3/9 tasks"))
        self.assertRegex(m, r"</svg><span class=\"lbl\">3/9 tasks</span>")
        self.assertNotIn("<text", m)

    def test_meter_edges_and_escaping(self):
        self.assertIn('width="0.0"', str(charts.meter(0, 0, "none")))
        self.assertIn('width="100.0"', str(charts.meter(9, 5, "over")))
        self.assertNotIn("<script>", str(charts.meter(1, 2, "<script>")))


class TestGauge(unittest.TestCase):
    def test_gauge_shows_zone_word_ticks_and_tone_class(self):
        g = str(charts.gauge("Agents", "8 / 6", 80, [60], ("danger", "Over cap"), "2 queued"))
        self.assertIn('class="fill-danger"', g)
        self.assertIn('width="80.0"', g)
        self.assertEqual(g.count('class="tick"'), 1)
        self.assertIn('x1="60.0" x2="60.0"', g)
        self.assertIn(">Over cap</b>", g)
        self.assertIn("2 queued", g)
        self.assertIn('aria-label="Agents 8 / 6, Over cap, 2 queued"', g)

    def test_gauge_clamps_out_of_range_values(self):
        g = str(charts.gauge("Load", "x", 250, [-5, 400], ("success", "OK")))
        self.assertIn('width="100.0"', g)
        self.assertIn('x1="0.0"', g)
        self.assertIn('x1="100.0"', g)


class TestSparkline(unittest.TestCase):
    def test_sparkline_summary_caption_and_table(self):
        s = str(charts.sparkline(DAYS, "merges"))
        self.assertIn('aria-label="12 merges in 14 days, peak 3 on 10-17"', s)
        self.assertIn('viewBox="0 0 120 24"', s)
        self.assertIn(">per day<", s)
        self.assertRegex(s, r'</svg><table class="sr-only">')
        self.assertEqual(s.count("<tr><td>"), 14)
        self.assertEqual(s.count("<circle"), 1)

    def test_sparkline_with_no_points_has_no_line(self):
        s = str(charts.sparkline([], "merges"))
        self.assertNotIn("<polyline", s)
        self.assertIn('aria-label="0 merges in 0 days"', s)


class TestColumns(unittest.TestCase):
    def test_every_day_has_a_rect_with_a_title_holding_the_value(self):
        c = str(charts.columns(DAYS, "merges"))
        rects = re.findall(r'<rect class="col[^"]*"[^>]*><title>([^<]*)</title></rect>', c)
        self.assertEqual(len(rects), 14)
        self.assertEqual(rects[17 - 14], "10-17: 3 merges")
        self.assertRegex(c, r'</svg><table class="sr-only">')

    def test_value_labels_only_on_non_zero_bars_and_x_label_every_seventh(self):
        c = str(charts.columns(DAYS, "merges"))
        self.assertEqual(len(re.findall(r'class="col-val"', c)), 8)
        self.assertEqual(re.findall(r'class="col-x"[^>]*>([^<]*)<', c), ["10-14", "10-21"])

    def test_no_value_labels_above_fourteen_days(self):
        pts = [(f"2026-09-{d:02d}", 1) for d in range(1, 21)]
        self.assertNotIn("col-val", str(charts.columns(pts, "merges")))

    def test_empty_data_gives_the_calm_line_and_no_axis(self):
        for pts in ([], [("2026-10-14", 0), ("2026-10-15", 0)]):
            c = str(charts.columns(pts, "merges"))
            self.assertIn("No merges in this range", c)
            self.assertNotIn("<svg", c)

    def test_chart_markup_is_csp_safe(self):
        for m in (charts.meter(1, 2, "x"), charts.gauge("a", "b", 1, [1], ("success", "OK")), charts.sparkline(DAYS, "m"),
                  charts.columns(DAYS, "m")):
            self.assertNotRegex(str(m), r'style=|fill="#|stroke="#')


class TestTile(unittest.TestCase):
    def test_tile_has_value_unit_delta_and_definition_disclosure(self):
        t = str(tile("First-try merge rate", "57", unit="%", delta=("up 12 pts", "success"), definition="Share never rejected."))
        self.assertIn('<div class="metric">57<small>%</small></div>', t)
        self.assertIn('class="delta fg-success">up 12 pts<', t)
        self.assertIn("<details", t)
        self.assertIn("Share never rejected.", t)

    def test_tile_without_extras_and_with_href(self):
        t = str(tile("Open PRs", 4, href="/tasks"))
        self.assertIn('<a href="/tasks">Open PRs</a>', t)
        self.assertNotIn("<details", t)
        self.assertNotIn("delta", t)


class TestNeedsYouRow(unittest.TestCase):
    def test_id_shown_once_and_linked(self):
        row = str(needs_you_row({"kind": "Blocked task", "tone": "danger", "subject": "Add stub", "ref": "T-0022",
                                 "href": "/task/T-0022", "age": "waiting 26 h"}))
        self.assertEqual(text(row).count("T-0022"), 1)
        self.assertIn('<a href="/task/T-0022"', row)

    def test_escalation_without_ref_shows_message_number_as_plain_text(self):
        row = str(needs_you_row({"kind": "Escalation", "tone": "warning", "subject": "Keys expired", "message": 26}))
        self.assertIn("Message #26", row)
        self.assertNotIn("<a ", row)

    def test_null_subject_gets_a_placeholder(self):
        self.assertIn("(no subject)", str(needs_you_row({"kind": "Escalation", "tone": "warning", "subject": None})))

    def test_subject_is_escaped(self):
        row = str(needs_you_row({"kind": "Escalation", "tone": "warning", "subject": "<b>x</b>"}))
        self.assertNotIn("<b>x", row)


if __name__ == "__main__":
    unittest.main()
