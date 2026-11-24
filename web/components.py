"""Shared list components for the web UI (G-019 B3). One helper per design component (T-0429 note 3):
C4 list_row / data_table, C5 row_status, C6 chips / segmented, C13 feed_row, C14 empty_state, C15 disclosure,
C17 agent_row. Markup is built with html.el(), so every value is escaped; Safe is used only for constant SVG.
B4-B6 import from here; keep the signatures documented in the docstrings stable.
"""
import re
from urllib.parse import quote

from scout import orgs as _orgs
from web.html import LABELS, Safe, el, glyph, h as _h, link, org_dot, status
from web.queries import DEFAULT_ORG, agent_org, org_param, org_runtime, parse_ts

# status group order of the task list (note 4 section 4); the last two start collapsed
GROUP_ORDER = ("blocked", "in_review", "in_progress", "todo", "done", "cancelled")
COLLAPSED = ("done", "cancelled")
_PR_NUM = re.compile(r"/pull/(\d+)$")
_CHECK = '<path d="M2.5 6.3 5 8.8 9.5 3.6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>'


def href(path, params):
    """`path` plus the non-empty `params` (dict, insertion order kept) as a query string; values percent-encoded."""
    qs = "&".join(f"{k}={quote(str(v), safe=',')}" for k, v in params.items() if v not in (None, "", []))
    return f"{path}?{qs}" if qs else path


def ago(iso, now, suffix=""):
    """Relative age of an ISO timestamp against `now`: "now", "5 m", "3 h", "2 d" (+ `suffix`); "" when unparseable."""
    d = parse_ts(iso)
    if d is None or now is None:
        return ""
    s = max(0, int((now - d).total_seconds()))
    if s < 60:
        return "now" if not suffix else "just now"
    n, unit = (s // 60, "m") if s < 3600 else (s // 3600, "h") if s < 86400 else (s // 86400, "d")
    return f"{n} {unit}{suffix}"


def pr_label(url):
    """"#226" for a github pull URL, else "PR"."""
    m = _PR_NUM.search(url or "")
    return f"#{m.group(1)}" if m else "PR"


# ── C5 row status ──

def row_status(key, label=False):
    """Quiet in-row status: the shape glyph plus the human word as screen-reader text. `label=True` shows the word
    too (html.status(), for headers and filters)."""
    if label:
        return status(key)
    return el("span", glyph(key), el("span", LABELS.get(key, str(key).replace("_", " ").capitalize()), class_="sr-only"),
              class_="st")


# ── C14 empty state, C15 disclosure ──

def empty_state(text, action=None):
    """One calm line inside a section; `action` is an optional (href, text) pair such as a "Clear filters" link."""
    return el("p", text, *([" ", el("a", action[1], href=action[0])] if action else []), class_="empty emptybox")


def disclosure(label, *body, key, count=None, open=False):
    """<details> with a chevron row. `key` is the stable data-key (unique per page) theme.js uses to keep the open
    state across the 60 s refresh."""
    summ = el("summary", el("span", label), el("span", count, class_="disc-n") if count is not None else None)
    return el("details", summ, *body, class_="disc", data_key=key, open=open or None)


def section(title, *body, count=None):
    """Titled box: h2 (+ count) outside, rows inside."""
    head = el("h2", title, " ", el("span", count, class_="sect-n") if count is not None else None)
    return el("section", head, el("div", *body, class_="sect-box"), class_="sect")


# ── C6 chips and segmented control ──

def chip(text, count, link, current=False):
    """One filter chip (a link, no JS). The current chip has aria-current="true" and a check glyph. A zero count
    stays visible and is dimmed."""
    mark = Safe(f'<svg class="g" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">{_CHECK}</svg>') if current else None
    return el("a", mark, el("span", text), " ", el("span", count, class_="chip-n") if count is not None else None,
              href=link, class_="chip chip--zero" if count == 0 else "chip", aria_current="true" if current else None)


def chips(label, items):
    """Labelled wrapping row of chips. items: (text, count|None, href, current) tuples."""
    return el("div", el("span", label, class_="facet-label"), el("div", [chip(*i) for i in items], class_="chips"), class_="facet")


def filter_bar(main, rest=(), *, key="filters", active=0):
    """One visible chips row (`main`) plus the secondary filters in a closed disclosure titled "Filters" with the
    count of active non-default filters. No `rest`: no details. All links stay in the HTML (works without script)."""
    parts = [x for x in (main if isinstance(main, (list, tuple)) else [main]) if x is not None]
    rest = [x for x in rest if x is not None]
    if rest:
        parts.append(disclosure("Filters", *rest, key=key, count=active or None))
    return el("div", *parts)


def segmented(label, items, scroll_strip=False):
    """Capsule of 2-4 exclusive options, links with aria-current on the selected one. items: (text, href, current).
    When scroll_strip is True (many short tabs on a narrow card), links stay inline and the row scrolls instead of
    stretching each tab to fill the width (which clips the last label)."""
    cls = "seg seg-links" + (" seg-links--scroll" if scroll_strip else "")
    return el("nav", [el("a", t, href=h_, aria_current="true" if cur else None) for t, h_, cur in items], class_=cls, aria_label=label)


# ── C4 list row and data table ──

def list_row(glyph_key, ident, title, url=None, meta=()):
    """List row (overview, grouped lists): glyph, mono id, title (the one link), flexible gap, right metadata.
    `meta` values that are empty render nothing."""
    t = el("a", title, href=url) if url else el("span", title)
    return el("li", row_status(glyph_key), el("span", ident, class_="mono lrow-id") if ident else None, t,
              el("span", [el("span", m) for m in meta if m not in (None, "")], class_="lrow-meta"), class_="lrow")


def _th(col, sort, sort_href):
    cls = col.get("cls")
    if not col.get("sortable"):
        return el("th", col["label"] if col.get("label") else el("span", col["key"], class_="sr-only"), scope="col", class_=cls)
    cur = sort and sort[0] == col["key"]
    arrow = "ascending" if cur and sort[1] == "asc" else "descending" if cur else None
    return el("th", el("a", col["label"], href=sort_href(col["key"]), class_="sortlink"), scope="col", class_=cls, aria_sort=arrow)


def _labelled(col, cell):
    """Cell content. A column with `phone_label` (True = its header label, or a text) gets a `cl` span before a
    non-empty cell: real text, shown by the phone card layout and hidden on desktop where the <th> names the column.
    Empty cells get no label. Columns without the key are unchanged."""
    want = col.get("phone_label")
    if not want or cell in ("", None):
        return cell
    return [el("span", want if isinstance(want, str) else col["label"], class_="cl"), cell]


def _table(columns, groups, sort, sort_href, with_header=True):
    head = el("thead", el("tr", [_th(c, sort, sort_href) for c in columns]))
    bodies = []
    for g in groups:
        rows = []
        if with_header and g.get("label"):
            rows.append(el("tr", el("th", g["label"], " ", el("span", g["count"], class_="grp-n"), colspan=len(columns), scope="colgroup"),
                           class_="grp"))
        for r in g["rows"]:
            rows.append(el("tr", [el("td", _labelled(c, cell), class_=c.get("cls")) for c, cell in zip(columns, r)]))
        bodies.append(el("tbody", rows))
    return el("table", head, bodies, class_="dt")


def data_table(columns, groups, sort=None, sort_href=None, key="tbl", empty=None):
    """Real <table>, sortable headers, groups by status. columns: dicts {key, label, cls?, sortable?, phone_label?}.
    groups: dicts {key, label, count, rows, collapsed?}; rows are lists of cells (str escaped, Safe kept), one per
    column. `sort` is the effective (column key, "asc"|"desc") and gets aria-sort; `sort_href(col_key)` builds the
    header link. Open groups share one table (group header row "In review 1"); collapsed groups follow as
    disclosures keyed f"{key}-{group key}", each with its own table of the same columns. With no groups the
    `empty` text shows as an empty state."""
    groups = [g for g in groups if g["rows"]]
    if not groups:
        return empty_state(empty or "Nothing to show.")
    sort_href = sort_href or (lambda k: "#")
    openg = [g for g in groups if not g.get("collapsed")]
    out = [_table(columns, openg, sort, sort_href)] if openg else []
    for g in groups:
        if g.get("collapsed"):
            out.append(disclosure(g["label"], _table(columns, [{"rows": g["rows"]}], None, sort_href),
                                  key=f"{key}-{g['key']}", count=g["count"]))
    cards = any(c.get("phone_label") for c in columns)   # opt-in: only tables with a phone-labelled column get the card layout
    return el("div", out, class_="dt-wrap dt-wrap--cards" if cards else "dt-wrap")


# ── C13 feed row ──

def feed_row(title, url, goal=None, pr_url=None, sha=None, when=None, when_title=None):
    """Changelog row: Done glyph, title link, goal id, PR "#226" (link), short sha, relative time on the right."""
    return el("li", row_status("done"), el("a", title, href=url, class_="feed-title"),
              el("span", goal, class_="feed-goal") if goal else None,
              (el("span", link(pr_url, pr_label(pr_url)), class_="mono") if pr_url else None),   # link() keeps only github pull URLs
              el("span", sha[:8], class_="mono feed-sha") if sha else None,
              el("span", when, title=when_title, class_="feed-when") if when else None, class_="feed-row")


def feed(rows, modifier=None):
    """<ul> wrapper for feed_row items. `modifier` ("log") adds the class feed--log (a bounded line length on desktop);
    without it the markup is what every other page already renders."""
    return el("ul", rows, class_=f"feed feed--{modifier}" if modifier else "feed")


# ── C17 agent row ──

AGENT_COLS = (
    {"key": "status", "label": "", "cls": "c-st"},
    {"key": "agent", "label": "Agent", "cls": "c-title"},
    {"key": "task", "label": "Task", "cls": "c-who", "phone_label": True},
    {"key": "last", "label": "Last event", "cls": "c-age num", "phone_label": "Last"},
)


def runtime_badge(runtime):
    """Small Claude/Cursor chip. Missing/unknown → Claude (factory default); does not invent other kinds."""
    rt = (runtime or "claude").strip().lower()
    if rt not in ("claude", "cursor"):
        rt = "claude"
    return el("span", rt, class_=f"rt rt-{rt}", title=f"Agent runtime: {rt}")


def org_of(a):
    """The org of an agent dict: its `org` key (queries.agents sets it), else derived from name and runtime."""
    o = a.get("org")
    if o in _orgs.ORGS:
        return o
    rt = a.get("runtime")
    return agent_org(None, a.get("name"), rt if rt in ("claude", "cursor") else None)


def org_href(path, params, org):
    """href() with ?org= appended when an org filter is on: in-page links keep the global filter."""
    return href(path, {**params, "org": org if org in _orgs.ORGS else None})


def ctx_org(ctx):
    """The whitelisted ?org= of a request context, or None (= all orgs)."""
    return org_param(getattr(ctx, "query", None))


def agent_row(a, now=None):
    """Cells for AGENT_COLS: glyph, name + role (role omitted when equal to the name), task id + title, last event age.
    `a` needs name, role, status; task_id, task_title, last_event optional. Blocked → Blocked glyph. No org chip on
    rows (G-069): the header's factory selector filters by org."""
    key = "blocked" if a.get("blocked") else a["status"]
    who = el("span", a["name"], " ", el("span", a["role"], class_="agent-role") if a.get("role") and a["role"] != a["name"] else None,
             class_="agent-who")
    task = el("span", el("a", a["task_id"], href=f"/task/{a['task_id']}", class_="mono"), " ",
              el("span", a.get("task_title"), title=a.get("task_title"), class_="agent-task")) if a.get("task_id") else ""
    last = ago(a.get("last_event"), now)
    return [row_status(key), who, task, last or ""]


# ── G-069 strip, banner, sparkline cell; C18 needs-you row ──

STRIP_TONES = ("good", "bad", "warn", "neutral")
_TONE_CLASS = {"good": "fg-success", "bad": "fg-danger", "warn": "fg-warning", "neutral": "muted"}


def _strip_item(item):
    label, value = item["label"], item.get("value")
    value = "–" if value in (None, "") else value
    tone = item.get("tone") if item.get("tone") in STRIP_TONES else "neutral"
    delta = item.get("delta")
    if delta and tone == "warn" and not str(delta).startswith("!"):
        delta = f"! {delta}"   # amber is never the only signal
    name = el("a", label, href=item["href"]) if item.get("href") else label
    return el("div", el("dt", name, class_="strip-label"),
              el("dd", value, el("small", item["unit"]) if item.get("unit") else None, class_="strip-value"),
              el("dd", delta, class_=f"strip-delta {_TONE_CLASS[tone]}") if delta else None, class_="strip-item")


def strip(items, key="strip"):
    """One row of headline figures (G-069 replaces the KPI tiles): label above a 22 px tabular value, an optional
    delta line under it. items: dicts {label, value, unit?, delta?, tone? good|bad|warn|neutral, href?, definition?}.
    A None or "" value shows "–". A warn delta gets a leading "!". Definitions, when any item has one, follow in one
    closed "How these are counted" disclosure (word for word, so a figure is never left unexplained)."""
    items = [i for i in items if i]
    defs = [(i["label"], i["definition"]) for i in items if i.get("definition")]
    out = [el("dl", [_strip_item(i) for i in items], class_="strip")]
    if defs:
        out.append(disclosure("How these are counted", el("dl", [[el("dt", lab), el("dd", d)] for lab, d in defs], class_="rail-dl"),
                              key=f"{key}-defs"))
    return el("div", out)


def howto(doc):
    """The page's one "How this is counted" link (G-069), to a repo doc such as docs/obs.md, served locally by
    web.server at /docs/<name> (no external host)."""
    return el("p", el("a", "How this is counted", href="/" + doc), class_="howto")


def banner(text):
    """One-line page warning (G-069 rules): always starts with "!", so colour is not the only signal."""
    text = str(text or "").strip()
    if not text:
        return None
    return el("p", text if text.startswith("!") else f"! {text}", class_="banner", role="note")


def banners(texts):
    """The page's banners in order, or None."""
    rows = [banner(t) for t in texts or () if t]
    return el("div", rows, class_="banners") if rows else None


def spark_cell(values, label):
    """Tiny 64 x 16 sparkline for a table cell (G-069), values oldest first; aria-label gives the range. Empty or all-zero
    values give "–"."""
    vals = [float(v or 0) for v in values or ()]
    if not vals or not any(vals):
        return "–"
    w, hgt, pad = 64, 16, 2
    top = max(vals)
    step = (w - 2 * pad) / (len(vals) - 1) if len(vals) > 1 else 0
    pts = " ".join(f"{pad + i * step:.1f},{hgt - pad - v / top * (hgt - 2 * pad):.1f}" for i, v in enumerate(vals))
    aria = f"{label}: {len(vals)} points, peak {top:g}"
    return Safe(f'<svg class="spark spark-cell" viewBox="0 0 {w} {hgt}" width="{w}" height="{hgt}" role="img" '
                f'focusable="false" aria-label="{_h(aria)}"><polyline points="{pts}"/></svg>')


_NEEDS_GLYPH = {"danger": "blocked", "warning": "queued", "neutral": "proposed"}


def needs_you_row(item):
    """C18 row of the Needs-you list. `item`: kind (Blocked task, Escalation, Proposed goal, ...), tone
    (danger|warning|neutral), subject, ref (goal/task id; shown ONCE, so the subject must not repeat it), href (the
    ref's page), message (message id, used only when there is no ref: shown as plain text "Message #26"), age (text).
    Escalations pass the subject only, never the message body."""
    tone = item.get("tone", "neutral")
    subject = item.get("subject") or "(no subject)"
    ref = item.get("ref")
    if ref:
        ident = el("a", ref, href=item["href"], class_="mono nid") if item.get("href") else el("span", ref, class_="mono nid")
    elif item.get("message") is not None:
        ident = el("span", f"Message #{item['message']}", class_="mono nid")
    else:
        ident = None
    return el("li", glyph(_NEEDS_GLYPH.get(tone, "proposed")), el("span", item["kind"], class_=f"nkind fg-{tone}"),
              el("span", subject, title=subject, class_="nsub"), ident,
              el("span", item.get("age"), class_="nage") if item.get("age") else None, class_="needs")
