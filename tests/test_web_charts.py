"""G-019 B4: SVG chart primitives (web/charts.py) and the strip / banner / needs-you row components."""
import re
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from web import charts  # noqa: E402
from web.components import banner, banners, howto, needs_you_row, spark_cell, strip  # noqa: E402

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


SERIES = (("work_usd", "Work", "b-work"), ("polling_usd", "Polling", "b-polling"), ("spawn_usd", "Spawn", "b-spawn"))
BINS = [("11-20", {"work_usd": 10.0, "polling_usd": 2.5, "spawn_usd": 0.5}),
        ("11-21", {"work_usd": 0, "polling_usd": 0, "spawn_usd": 0}),
        ("11-22", {"work_usd": 31.25, "polling_usd": 4.0, "spawn_usd": 1.0})]


class TestAxisChart(unittest.TestCase):
    """G-069 [SC5]: the daily stacked work/polling/spawn chart, 220 px, y axis with units, legend top right."""

    def test_nice_ticks_use_a_round_step_and_cover_the_top(self):
        self.assertEqual(charts.nice_ticks(36.25), [0, 20, 40])
        self.assertEqual(charts.nice_ticks(7), [0, 2.5, 5, 7.5])
        self.assertEqual(charts.nice_ticks(0), [0.0, 1.0])
        for top in (0.3, 1, 9.99, 123, 48_000_000):
            t = charts.nice_ticks(top)
            self.assertGreaterEqual(t[-1], top)
            self.assertLessEqual(len(t), 5, t)

    def test_every_segment_has_a_title_with_its_value_and_zero_segments_are_skipped(self):
        c = str(charts.axis_chart(BINS, SERIES, "Spend by day", x_label="day"))
        self.assertIn("<title>11-22 Work: $31.25</title>", c)
        self.assertIn("<title>11-20 Polling: $2.5</title>", c)
        self.assertNotIn("11-21 Work", c)
        self.assertEqual(c.count('class="b-work"'), 2)

    def test_height_is_220_and_the_svg_scales_to_its_column(self):
        c = str(charts.axis_chart(BINS, SERIES, "Spend by day"))
        self.assertIn('viewBox="0 0 960 220"', c)
        self.assertIn('height="220"', c)
        self.assertIn('width="100%"', c)

    def test_y_axis_labels_carry_the_unit_and_legend_lists_top_series_first(self):
        c = str(charts.axis_chart(BINS, SERIES, "Spend by day"))
        self.assertEqual(re.findall(r'class="axis-y"[^>]*>([^<]+)<', c), ["$0", "$20", "$40"])
        legend = re.search(r'<ul class="axis-legend">(.*?)</ul>', c).group(1)
        self.assertEqual(re.findall(r"</span>([^<]+)</li>", legend), ["Spawn", "Polling", "Work"])

    def test_screen_reader_table_and_aria_summary(self):
        c = str(charts.axis_chart(BINS, SERIES, "Spend by day", x_label="day"))
        self.assertIn('aria-label="Spend by day: $49.25 over 3 days, peak $36.25"', c)
        self.assertIn("<tr><td>11-22</td><td>$31.25</td><td>$4</td><td>$1</td></tr>", c)

    def test_empty_or_all_zero_data_gives_the_calm_line(self):
        for bins in ([], [("11-21", {"work_usd": 0})]):
            c = str(charts.axis_chart(bins, SERIES, "spend"))
            self.assertIn("No spend in this range", c)
            self.assertNotIn("<svg", c)

    def test_x_labels_thin_out_past_eight_bins(self):
        bins = [(f"h{i:02d}", {"work_usd": 1}) for i in range(24)]
        self.assertEqual(len(re.findall(r'class="axis-x"', str(charts.axis_chart(bins, SERIES, "s")))), 8)

    def test_token_sized_values_never_print_an_exponent(self):
        bins = [("11-21", {"work_usd": 48_567_200}), ("11-22", {"work_usd": 0.000_000_3}), ("11-23", {"work_usd": 1e-9})]
        c = str(charts.axis_chart(bins, SERIES, "tokens", unit=""))
        self.assertNotRegex(c, r"\de[+-]\d")

    def test_compact_rolls_over_to_M_at_the_unit_boundary(self):
        self.assertEqual([charts.compact(n) for n in (999, 1000, 999949, 999950, 999999, 1_000_000)],
                         ["999", "1.0k", "999.9k", "1.0M", "1.0M", "1.0M"])

    def test_chart_markup_is_csp_safe(self):
        for m in (charts.meter(1, 2, "x"), charts.gauge("a", "b", 1, [1], ("success", "OK")), charts.sparkline(DAYS, "m"),
                  charts.axis_chart(BINS, SERIES, "m"), spark_cell([1, 2, 3], "t")):
            self.assertNotRegex(str(m), r'style=|fill="#|stroke="#')


class TestStrip(unittest.TestCase):
    """G-069: the strip replaces KPI tiles."""

    def test_strip_item_has_label_value_unit_and_delta(self):
        t = str(strip([{"label": "Spend", "value": "$62.09", "delta": "up 4% vs previous", "tone": "warn"},
                       {"label": "Merged", "value": 12, "unit": "PRs", "href": "/tasks"}]))
        self.assertIn('<dt class="strip-label">Spend</dt>', t)
        self.assertIn('<dd class="strip-value">$62.09</dd>', t)
        self.assertIn('class="strip-delta fg-warning">! up 4% vs previous<', t)
        self.assertIn('<a href="/tasks">Merged</a>', t)
        self.assertIn("12<small>PRs</small>", t)
        self.assertNotIn("<details", t)

    def test_unknown_value_shows_a_dash_and_definitions_go_in_one_disclosure(self):
        t = str(strip([{"label": "Weekly limit", "value": None, "definition": "Seven-day usage."}, None]))
        self.assertIn('<dd class="strip-value">–</dd>', t)
        self.assertEqual(t.count("<details"), 1)
        self.assertIn("Seven-day usage.", t)

    def test_banner_always_starts_with_a_bang_and_empty_gives_none(self):
        self.assertIn(">! Polling is 25% of spend<", str(banner("Polling is 25% of spend")))
        self.assertIn(">! already<", str(banner("! already")))
        self.assertIsNone(banner(""))
        self.assertIsNone(banners([]))
        self.assertEqual(str(banners(["a", "", "b"])).count('class="banner"'), 2)

    def test_spark_cell_and_howto(self):
        self.assertEqual(spark_cell([0, 0], "t"), "–")
        self.assertIn('aria-label="Trend: 3 points, peak 3"', str(spark_cell([1, 2, 3], "Trend")))
        h = str(howto("docs/obs.md"))
        self.assertIn('href="https://github.com/volekerb/hivemill/blob/main/docs/obs.md"', h)
        self.assertIn("How this is counted", h)


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
