"""Shared list components for the web UI (G-019 B3). One helper per design component (T-0429 note 3):
C4 list_row / data_table, C5 row_status, C6 chips / segmented, C13 feed_row, C14 empty_state, C15 disclosure,
C17 agent_row. Markup is built with html.el(), so every value is escaped; Safe is used only for constant SVG.
B4-B6 import from here; keep the signatures documented in the docstrings stable.
"""
import re
from urllib.parse import quote

from web.html import LABELS, Safe, el, glyph, link, status
from web.queries import parse_ts

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


def segmented(label, items):
    """Capsule of 2-4 exclusive options, links with aria-current on the selected one. items: (text, href, current)."""
    return el("nav", [el("a", t, href=h_, aria_current="true" if cur else None) for t, h_, cur in items],
              class_="seg seg-links", aria_label=label)


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


def _table(columns, groups, sort, sort_href, with_header=True):
    head = el("thead", el("tr", [_th(c, sort, sort_href) for c in columns]))
    bodies = []
    for g in groups:
        rows = []
        if with_header and g.get("label"):
            rows.append(el("tr", el("th", g["label"], " ", el("span", g["count"], class_="grp-n"), colspan=len(columns), scope="colgroup"),
                           class_="grp"))
        for r in g["rows"]:
            rows.append(el("tr", [el("td", cell, class_=c.get("cls")) for c, cell in zip(columns, r)]))
        bodies.append(el("tbody", rows))
    return el("table", head, bodies, class_="dt")


def data_table(columns, groups, sort=None, sort_href=None, key="tbl", empty=None):
    """Real <table>, sortable headers, groups by status. columns: dicts {key, label, cls?, sortable?}.
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
    return el("div", out, class_="dt-wrap")


# ── C13 feed row ──

def feed_row(title, url, goal=None, pr_url=None, sha=None, when=None, when_title=None):
    """Changelog row: Done glyph, title link, goal id, PR "#226" (link), short sha, relative time on the right."""
    return el("li", row_status("done"), el("a", title, href=url, class_="feed-title"),
              el("span", goal, class_="feed-goal") if goal else None,
              (el("span", link(pr_url, pr_label(pr_url)), class_="mono") if pr_url else None),   # link() keeps only github pull URLs
              el("span", sha[:8], class_="mono feed-sha") if sha else None,
              el("span", when, title=when_title, class_="feed-when") if when else None, class_="feed-row")


def feed(rows):
    """<ul> wrapper for feed_row items."""
    return el("ul", rows, class_="feed")


# ── C17 agent row ──

AGENT_COLS = (
    {"key": "status", "label": "", "cls": "c-st"},
    {"key": "agent", "label": "Agent", "cls": "c-title"},
    {"key": "task", "label": "Task", "cls": "c-who"},
    {"key": "last", "label": "Last event", "cls": "c-age num"},
)


def runtime_badge(runtime):
    """Small Claude/Cursor chip. Missing/unknown → Claude (factory default); does not invent other kinds."""
    rt = (runtime or "claude").strip().lower()
    if rt not in ("claude", "cursor"):
        rt = "claude"
    return el("span", rt, class_=f"rt rt-{rt}", title=f"Agent runtime: {rt}")


def agent_row(a, now=None):
    """Cells for AGENT_COLS: glyph, name + role (role omitted when equal to the name), task id + title, last event age.
    `a` needs name, role, status; task_id, task_title, last_event, runtime optional. Blocked → Blocked glyph."""
    key = "blocked" if a.get("blocked") else a["status"]
    who = el("span", a["name"], " ", el("span", a["role"], class_="agent-role") if a.get("role") and a["role"] != a["name"] else None,
             " ", runtime_badge(a.get("runtime")),
             class_="agent-who")
    task = el("span", el("a", a["task_id"], href=f"/task/{a['task_id']}", class_="mono"), " ",
              el("span", a.get("task_title"), title=a.get("task_title"), class_="agent-task")) if a.get("task_id") else ""
    last = ago(a.get("last_event"), now)
    return [row_status(key), who, task, last or ""]


# ── C7 metric tile, C18 needs-you row (G-019 B4) ──

def tile(label, value, unit=None, delta=None, definition=None, href=None):
    """C7 metric tile: label, big value (+ small `unit`), optional `delta` line and an (i) definition disclosure.
    `delta` is a text or a (text, tone) pair, tone success|warning|danger|neutral (default neutral); `definition`
    opens as a short paragraph under the label; `href` makes the label a link (to the detail page)."""
    text, tone = (delta if isinstance(delta, tuple) else (delta, "neutral"))
    name = el("a", label, href=href) if href else el("span", label)
    info = (el("details", el("summary", "i", aria_label=f"What is {label}?"), el("p", definition, class_="tile-def"),
               class_="disc--i") if definition else None)
    return el("div", el("div", name, " ", info, class_="tile-label"),
              el("div", value, el("small", unit) if unit else None, class_="metric"),
              el("div", text, class_=f"delta fg-{tone}") if text else None, class_="tile")


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
