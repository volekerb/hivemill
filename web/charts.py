"""SVG chart primitives for the web UI (G-019 B4, design components C8-C10). Stdlib only.

Every function returns html.Safe built with html.el(), so labels are escaped. Geometry lives in SVG attributes
(no inline style attributes, the CSP is style-src 'self'); colours come from the app.css classes (fill-*, track, tick, spark, col),
so dark mode and the tokens apply. Every chart has a role="img" aria-label summary; sparkline and axis_chart are
followed by a visually hidden <table class="sr-only"> with the same data.

B5 and B6 import from here; keep the signatures stable:
  meter(value, total, label, noun="tasks")           C8 progress bar with the label beside it
  gauge(label, value_text, pct, thresholds, zone, caption)   C9 labelled threshold gauge row
  gauge_bar(pct, thresholds, tone, aria)             the bare C9 bar (stack two for 5-hour and weekly)
  sparkline(points, label, caption="per day")        C10 120 x 24 trend line
  axis_chart(bins, series, label, unit="$")          G-069 stacked columns with a y axis, gridlines and a legend
`points` are (day "YYYY-MM-DD", number) pairs, oldest first.
"""
import math

from web.components import empty_state
from web.html import Safe, el, h

TONES = ("success", "warning", "danger", "neutral")
SPARK_W, SPARK_H, SPARK_PAD = 120, 24, 3


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


def compact(n):
    """Short figure for a count: 1500 -> "1.5k", 48567200 -> "48.6M"; below 1000 the number as it is. Bar labels, the
    aria-label and web.views.tokens use it, so no figure ever prints as 4.85672e+07."""
    n = n or 0
    if abs(n) >= 1_000_000:
        return f"{n / 1_000_000:.1f}M"
    if abs(n) >= 1_000:
        k = f"{n / 1_000:.1f}"
        return f"{k}k" if abs(float(k)) < 1000 else f"{n / 1_000_000:.1f}M"   # 999,999 is 1.0M, not 1000.0k
    return f"{n:g}"


def _exact(v):
    """Exact value for a tooltip or table cell: whole numbers with thousands separators (48,567,200), never an exponent."""
    return f"{int(v):,}" if float(v).is_integer() else compact(v)


def _day(iso):
    return iso[5:10] if len(iso) >= 10 else iso


def _data_table(points, label, caption, unit="day"):
    rows = [el("tr", el("td", d), el("td", _exact(n))) for d, n in points]
    return el("table", el("caption", caption), el("thead", el("tr", el("th", unit.capitalize(), scope="col"), el("th", label.capitalize(), scope="col"))),
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


def _summary(points, label, unit="day"):
    total = sum(n for _, n in points)
    peak = max((n for _, n in points), default=0)
    text = f"{compact(total)} {label} in {len(points)} {unit}s"
    if peak > 0:
        day = [d for d, n in points if n == peak][-1]
        text += f", peak {compact(peak)} on {_day(day)}"
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


AXIS_H = 220                 # G-069: every chart is 220 px tall
AXIS_W = 960                 # viewBox width; the svg scales to its column (max width 1440 minus the sidebar)
AXIS_LEFT, AXIS_RIGHT, AXIS_TOP, AXIS_BOTTOM = 56, 8, 10, 22


def nice_ticks(top, n=3):
    """Gridline values 0..>=top on a 1/2/2.5/5 x 10^k step with at most n+1 lines (3-4 gridlines). top <= 0 -> [0, 1]."""
    if not top or top <= 0:
        return [0.0, 1.0]
    raw = top / n
    mag = 10 ** math.floor(math.log10(raw))
    step = next(m * mag for m in (1, 2, 2.5, 5, 10) if m * mag >= raw)
    k = math.ceil(top / step - 1e-9)
    return [round(i * step, 10) for i in range(k + 1)]


def _tick_text(v, unit, exact=False):
    """Axis tick (whole units from 10 up) or, with exact, a data value to the cent; trailing zeros dropped."""
    if not exact and (v >= 10 or float(v).is_integer()):
        t = f"{v:,.0f}"
    else:
        t = f"{v:,.2f}".rstrip("0").rstrip(".")
    return f"${t}" if unit == "$" else f"{t} {unit}" if unit else t


def axis_chart(bins, series, label, unit="$", x_label=None):
    """G-069 chart: stacked columns with a y axis (unit on the tick labels), 3-4 gridlines and a legend at the top right.
    `bins` are (x label, {series key: value}) pairs, oldest first; `series` are (key, legend label, class) in stack order
    from the baseline. Each column segment has a <title> with its exact value; a sr-only table repeats the data. No data
    (no bins or all zero) gives the calm empty line."""
    bins = list(bins)
    if not bins or not any(v for _, vals in bins for v in vals.values() if v):
        return empty_state(f"No {label} in this range")
    totals = [sum(float(vals.get(k) or 0) for k, _, _ in series) for _, vals in bins]
    ticks = nice_ticks(max(totals))
    top = ticks[-1]
    plot_h = AXIS_H - AXIS_TOP - AXIS_BOTTOM
    plot_w = AXIS_W - AXIS_LEFT - AXIS_RIGHT
    n = len(bins)
    slot = plot_w / n
    bw = max(1.0, slot * 0.7)
    y = lambda v: AXIS_TOP + plot_h - (v / top) * plot_h   # noqa: E731
    shapes = []
    for t in ticks:
        shapes.append(Safe(f'<line class="axis-grid" x1="{AXIS_LEFT}" x2="{AXIS_W - AXIS_RIGHT}" y1="{_num(y(t))}" y2="{_num(y(t))}"/>'))
        shapes.append(el("text", _tick_text(t, unit), class_="axis-y", x=AXIS_LEFT - 6, y=_num(y(t) + 4), text_anchor="end"))
    every = max(1, math.ceil(n / 8))
    for i, (xl, vals) in enumerate(bins):
        x = AXIS_LEFT + i * slot + (slot - bw) / 2
        base = 0.0
        for k, lab, cls in series:
            v = float(vals.get(k) or 0)
            if v <= 0:
                continue
            y0, y1 = y(base), y(base + v)
            shapes.append(el("rect", el("title", f"{xl} {lab}: {_tick_text(v, unit, True)}"), class_=cls,
                             x=_num(x), y=_num(y1), width=_num(bw), height=_num(max(0.5, y0 - y1))))
            base += v
        if i % every == 0:
            shapes.append(el("text", xl, class_="axis-x", x=_num(x + bw / 2), y=AXIS_H - 6, text_anchor="middle"))
    total = sum(totals)
    aria = f"{label}: {_tick_text(total, unit, True)} over {n} {x_label or 'bin'}{'' if n == 1 else 's'}, peak {_tick_text(max(totals), unit, True)}"
    svg = _svg(shapes, f"0 0 {AXIS_W} {AXIS_H}", height=AXIS_H, width="100%", role="img", focusable="false",
               preserveAspectRatio="none", **{"aria-label": aria}, **{"class": "axis"})
    legend = el("ul", [el("li", el("span", class_=f"lg-sw {cls}", aria_hidden="true"), lab) for _, lab, cls in reversed(series)],
                class_="axis-legend")
    head = el("tr", el("th", (x_label or "bin").capitalize(), scope="col"), [el("th", lab, scope="col") for _, lab, _ in series])
    rows = [el("tr", el("td", xl), [el("td", _tick_text(float(vals.get(k) or 0), unit, True)) for k, _, _ in series]) for xl, vals in bins]
    table = el("table", el("caption", label), el("thead", head), el("tbody", rows), class_="sr-only")
    return el("figure", legend, svg, table, class_="axis-wrap")
