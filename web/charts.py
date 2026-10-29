"""SVG chart primitives for the web UI (G-019 B4, design components C8-C10). Stdlib only.

Every function returns html.Safe built with html.el(), so labels are escaped. Geometry lives in SVG attributes
(no inline style attributes, the CSP is style-src 'self'); colours come from the app.css classes (fill-*, track, tick, spark, col),
so dark mode and the tokens apply. Every chart has a role="img" aria-label summary; sparkline and columns are
followed by a visually hidden <table class="sr-only"> with the same data.

B5 and B6 import from here; keep the signatures stable:
  meter(value, total, label, noun="tasks")           C8 progress bar with the label beside it
  gauge(label, value_text, pct, thresholds, zone, caption)   C9 labelled threshold gauge row
  gauge_bar(pct, thresholds, tone, aria)             the bare C9 bar (stack two for 5-hour and weekly)
  sparkline(points, label, caption="per day")        C10 120 x 24 trend line
  columns(points, label, height=96)                  C10 column chart, one bar per day
`points` are (day "YYYY-MM-DD", number) pairs, oldest first.
"""
from web.components import empty_state
from web.html import Safe, el, h

TONES = ("success", "warning", "danger", "neutral")
SPARK_W, SPARK_H, SPARK_PAD = 120, 24, 3
COL_W = 24
COL_TOP, COL_BOTTOM = 14, 16


def _svg(inner, view_box, **attrs):
    """<svg> root. html.el() only takes lower-case attribute names, and SVG needs viewBox and preserveAspectRatio, so the
    root is assembled here; every value goes through html.h()."""
    a = {"viewBox": view_box, **attrs}
    return Safe("<svg" + "".join(f' {k}="{h(v)}"' for k, v in a.items() if v is not None) + ">" + "".join(str(i) for i in inner) + "</svg>")


def _line(x, y1, y2, **attrs):
    """Vertical <line>; html.el() rejects attribute names with digits (x1), so it is assembled here."""
    a = {"x1": x, "x2": x, "y1": y1, "y2": y2, **attrs}
    return Safe("<line" + "".join(f' {k}="{h(v)}"' for k, v in a.items()) + "/>")


def _pct(v):
    try:
        return max(0.0, min(100.0, float(v)))
    except (TypeError, ValueError):
        return 0.0


def _num(v):
    return f"{v:.1f}"


def _day(iso):
    return iso[5:10] if len(iso) >= 10 else iso


def _data_table(points, label, caption):
    rows = [el("tr", el("td", d), el("td", n)) for d, n in points]
    return el("table", el("caption", caption), el("thead", el("tr", el("th", "Day", scope="col"), el("th", label.capitalize(), scope="col"))),
              el("tbody", rows), class_="sr-only")


def meter(value, total, label, noun="tasks"):
    """C8: a 100 x 6 track with a fill whose width is exact (value / total, one decimal; 3 of 9 -> 33.3), not a 5 % step.
    `label` ("3/9 tasks") sits beside the bar, not inside it. aria-label "3 of 9 tasks done" (`noun` is the unit)."""
    w = _pct(100 * value / total) if total else 0.0
    bar = _svg([el("rect", class_="track", x=0, y=0, width=100, height=6),
                el("rect", class_="fill-data1", x=0, y=0, width=_num(w), height=6)], "0 0 100 6",
               preserveAspectRatio="none", role="img", focusable="false", **{"aria-label": f"{value} of {total} {noun} done"},
               **{"class": "meter-svg"})
    return el("div", bar, el("span", label, class_="lbl"), class_="meter")


def gauge_bar(pct, thresholds, tone, aria):
    """The bare C9 bar: 100 x 8 track, a fill classed by `tone` (success|warning|danger) and a 2 px tick at each
    threshold percentage. Colour is never the only signal: callers put the zone word in text next to it."""
    tone = tone if tone in TONES else "success"
    ticks = [_line(_num(_pct(t)), 0, 8, **{"class": "tick"}) for t in thresholds or ()]
    return _svg([el("rect", class_="track", x=0, y=0, width=100, height=8),
                 el("rect", class_=f"fill-{tone}", x=0, y=0, width=_num(_pct(pct)), height=8), *ticks], "0 0 100 8",
                preserveAspectRatio="none", role="img", focusable="false", **{"aria-label": aria}, **{"class": "gauge"})


def gauge(label, value_text, pct, thresholds, zone, caption=None):
    """C9: row of label, bar, value. `pct` is the fill (0-100), `thresholds` the tick positions (percentages), `zone`
    a (tone, word) pair such as ("danger", "Over cap"); the word is always shown, so colour is not the only signal."""
    tone, word = zone
    aria = f"{label} {value_text}, {word}" + (f", {caption}" if caption else "")
    sub = el("span", el("b", word, class_=f"gz fg-{tone}"), f" · {caption}" if caption else None)
    return el("div", el("span", label, class_="gl"), gauge_bar(pct, thresholds, tone, aria),
              el("span", value_text, sub, class_="gv"), class_="gauge-row")


def _summary(points, label):
    total = sum(n for _, n in points)
    peak = max((n for _, n in points), default=0)
    text = f"{total:g} {label} in {len(points)} days"
    if peak > 0:
        day = [d for d, n in points if n == peak][-1]
        text += f", peak {peak:g} on {_day(day)}"
    return text


def sparkline(points, label, caption="per day"):
    """C10: 120 x 24 polyline of `points` with the last point emphasised, aria-label "8 merges in 14 days, peak 3 on
    10-21" (`label` is the plural noun), the sr-only data table and the visible `caption` ("per day")."""
    points = list(points)
    n = len(points)
    top = max((v for _, v in points), default=0) or 1
    step = (SPARK_W - 2 * SPARK_PAD) / (n - 1) if n > 1 else 0
    xy = [(SPARK_PAD + i * step, SPARK_H - SPARK_PAD - (v / top) * (SPARK_H - 2 * SPARK_PAD)) for i, (_, v) in enumerate(points)]
    shapes = []
    if xy:
        shapes = [el("polyline", points=" ".join(f"{_num(x)},{_num(y)}" for x, y in xy)),
                  el("circle", class_="spark-last", cx=_num(xy[-1][0]), cy=_num(xy[-1][1]), r=2.5)]
    svg = _svg(shapes, f"0 0 {SPARK_W} {SPARK_H}", width=SPARK_W, height=SPARK_H, role="img", focusable="false",
               **{"aria-label": _summary(points, label)}, **{"class": "spark"})
    return el("span", svg, _data_table(points, label, f"{label.capitalize()} per day, last {n} days"),
              el("span", caption, class_="spark-cap"), class_="spark-wrap")


def columns(points, label, height=96):
    """C10: one <rect> per day, each with a <title> holding its exact value; value labels on non-zero bars when there
    are 14 days or fewer; an x label every 7th day. Empty data (no points, or all zero) gives the calm C14 line
    "No <label> in this range" instead of an empty axis."""
    points = list(points)
    if not points or not any(v for _, v in points):
        return empty_state(f"No {label} in this range")
    n = len(points)
    top = max(v for _, v in points)
    plot = height - COL_TOP - COL_BOTTOM
    bars, texts = [], []
    for i, (day, v) in enumerate(points):
        bh = max(1.0, v / top * plot) if v else 1.0
        x = i * COL_W + 4
        bars.append(el("rect", el("title", f"{_day(day)}: {v:g} {label}"), class_="col col--zero" if not v else "col",
                       x=x, y=_num(height - COL_BOTTOM - bh), width=COL_W - 8, height=_num(bh)))
        if v and n <= 14:
            texts.append(el("text", f"{v:g}", class_="col-val", x=_num(x + (COL_W - 8) / 2), y=_num(height - COL_BOTTOM - bh - 3),
                            text_anchor="middle"))
        if i % 7 == 0:
            texts.append(el("text", _day(day), class_="col-x", x=_num(x + (COL_W - 8) / 2), y=height - 3, text_anchor="middle"))
    svg = _svg([*bars, *texts], f"0 0 {n * COL_W} {height}", width=n * COL_W, height=height, role="img", focusable="false",
               **{"aria-label": _summary(points, label)}, **{"class": "cols"})
    return el("div", svg, _data_table(points, label, f"{label.capitalize()} per day, last {n} days"), class_="cols-wrap")
