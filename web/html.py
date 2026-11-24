"""Escape-by-default HTML helpers for the web UI (stdlib only).

Every interpolated value is escaped unless it is a `Safe` (the output of these helpers). Views never build HTML
with f-strings around raw DB values. Only the class names in CLASSES may be emitted (plan G-008 section 3.7).
"""
import html as _html
import re

from scout import orgs as _orgs
from urllib.parse import quote

from web.queries import RANGES, obs_value, parse_ts, range_param

_STATUSES = ("todo", "in_progress", "in_review", "blocked", "done", "cancelled", "proposed", "planning", "active",
             "live", "queued", "exited", "lost", "stopped")
_TONES = ("neutral", "info", "success", "warning", "danger", "review")
CLASSES = frozenset({
    "page", "sidebar", "nav-item", "nav-item--active",
    "card", "card-title", "kpi", "kpi-label", "kpi-value", "kpi-note", "grid",
    "table", "table--compact", "num",
    "badge", "badge--ok", "badge--warn", "badge--bad", "badge--info", "badge--muted",
    "bar", "timeline", "timeline-item", "empty", "muted", "mono", "stale",
    "app", "skip", "topbar", "topbar-right", "wordmark", "main", "shellbar", "shellbar--task", "titlebar", "crumbs", "sep", "cluster",
    "updated", "pill", "pill-dot", "hide-phone", "count", "count--bad", "navgroup", "themebox", "seg", "seg-fill",
    "tabbar", "tab", "is-current", "more", "sr-only", "g", "st", "st--pill",
    "dt", "dt-wrap", "grp", "grp-n", "sortlink", "c-st", "c-id", "c-title", "c-who", "c-age", "chips", "chip", "chip--zero", "chip-n",
    "facet", "facet-label", "seg-links", "seg-links--scroll", "disc", "disc--i", "disc-n", "sect", "sect-n", "sect-box", "emptybox", "summary", "summary-line",
    "summary-line--warn", "lrow", "lrow-id", "lrow-meta", "feed", "feed-row", "feed-title", "feed-goal", "feed-sha", "feed-when",
    "agent-who", "agent-role", "agent-task", "rt", "rt-claude", "rt-cursor",
    "org", "org-dot", "orgseg", "lanes", "lane", "lane-head", "lane-title", "lane-health", "lane-hq",
    "lane-stats", "lane-stat", "lane-n", "lane-l", "orgrow", "org-note",
    "ov", "ov-sect", "ov-trend", "ov-bounce", "sect-head", "sect-link", "more-link", "lrow-list", "needs", "nkind", "nsub", "nid", "nage",
    "goalrow", "gm", "gtitle", "chip-p", "gprs", "gact", "gneeds", "goalrow--tree", "gnode", "gsum", "teams", "team", "tsum",
    "tscope", "tm", "tk", "tkrow", "tkmeta", "agentrow", "system", "gauge", "gauge-row", "gauge-stack", "gl", "gv", "gz", "tick",
    "meter", "meter-svg", "lbl", "track", "fill-data1", "fill-success", "fill-warning", "fill-danger", "fill-neutral", "spark", "spark-wrap",
    "spark-cap", "spark-last", "cols", "cols-wrap", "col", "col--zero", "col-val", "col-x", "tiles", "tile", "tile-label", "tile-def",
    "metric", "delta",
    "gchips", "gblk", "crit-text", "crit-tasks", "intent", "gd", "gd-main", "rail", "rail-dl", "metarow", "attempt",
    "cl", "dt-wrap--cards", "tblock", "tblock--wide", "metric-val", "metric--stack", "tile-unit", "tiles--4", "tiles--6", "feed--log", "changelog-meta", "changelog-line-note",
    "ins-list", "ins-row", "ins-name", "ins-gauges", "ins-check", "ins-check--over", "ins-key", "ins-wide",
    "only-phone", "table--id-col", "per-day-grid",
    "nav-item--soon", "soon", "hsel", "hsel-menu", "strip", "strip-item", "strip-label", "strip-value", "strip-delta",
    "banner", "banners", "banner--danger", "banner--warning", "shell-health", "axis", "axis-wrap", "axis-grid", "axis-y", "axis-x", "axis-legend", "lg-sw",
    "b-work", "b-polling", "b-spawn", "warn-cell", "howto", "stages", "stage", "stage-bar", "stage-fill",
    "stage-name", "stage-h", "stage-share", "lane-main", "lane-rework", "lane-side", "lane-other", "stage-lane",
    "drill", "obs-err", "role-1", "role-2", "role-3", "role-4", "role-5", "role-6", "role-7", "role-8", "spark-cell",
    *(f"{p}-{t}" for p in ("fg", "gs", "tone") for t in _TONES),
    *(f"status--{s}" for s in _STATUSES),
    *(f"{p}-{o}" for p in ("org", "org-dot", "lane-") for o in _orgs.NAMES),
    *(f"bar--{n}" for n in range(0, 101, 5)),
})

# nav groups (G-069): (group label, ((path, label, icon), ...)); path None = not built yet (shown as "soon", no link).
# A flat NAV keeps the old (path, label) shape for the built pages.
GROUPS = (("Now", (("/", "Overview", "grid"), ("/goals", "Goals", "target"), ("/tasks", "Tasks", "list"),
                   ("/agents", "Agents", "user"))),
          ("Analyze", (("/spend", "Spend", "coin"), ("/flow", "Flow", "flow"), ("/quality", "Quality", "check"))),
          ("Improve", ((None, "Recommendations", "bulb"),)),
          ("System", (("/resources", "Resources", "chip"), ("/changelog", "Changelog", "lines"))))
NAV = tuple((p, label) for _, items in GROUPS for p, label, _ in items if p)
TABS = ("/", "/goals", "/tasks", "/agents")

# one label map for every enum shown to people (note 4 copy table)
LABELS = {"todo": "To do", "in_progress": "In progress", "in_review": "In review", "blocked": "Blocked", "done": "Done",
          "cancelled": "Cancelled", "proposed": "Proposed", "planning": "Planning", "active": "Active", "live": "Running",
          "queued": "Queued", "exited": "Ended", "stopped": "Ended", "lost": "Lost", "parked": "Parked"}

_SVG16 = {
    "grid": '<rect x="2" y="2" width="5" height="5" rx="1"/><rect x="9" y="2" width="5" height="5" rx="1"/>'
            '<rect x="2" y="9" width="5" height="5" rx="1"/><rect x="9" y="9" width="5" height="5" rx="1"/>',
    "target": '<circle cx="8" cy="8" r="6"/><circle cx="8" cy="8" r="2.5"/>',
    "list": '<path d="M3 4.5 4.5 6 7 3.5M3 10.5 4.5 12 7 9.5M9.5 5h4M9.5 11h4"/>',
    "user": '<circle cx="8" cy="5.5" r="2.5"/><path d="M3 13.5c.5-3 2.5-4 5-4s4.5 1 5 4"/>',
    "bars": '<path d="M3 13V8M8 13V3M13 13V6"/>',
    "check": '<circle cx="8" cy="8" r="6"/><path d="M5.2 8.2l2 2 3.6-4"/>',
    "db": '<ellipse cx="8" cy="4.5" rx="5" ry="2"/><path d="M3 4.5v7c0 1.1 2.2 2 5 2s5-.9 5-2v-7M3 8c0 1.1 2.2 2 5 2s5-.9 5-2"/>',
    "lines": '<path d="M3 4h10M3 8h10M3 12h6"/>',
    "chip": '<rect x="3" y="3" width="10" height="10" rx="2"/><path d="M6 1.5V3M10 1.5V3M6 13v1.5M10 13v1.5"/>',
    "coin": '<circle cx="8" cy="8" r="6"/><path d="M10 5.8C9.6 5.2 8.9 5 8 5c-1.2 0-2 .6-2 1.5S6.8 7.7 8 8s2 .7 2 1.5S9.2 11 8 11c-.9 0-1.6-.2-2-.8M8 3.8V5M8 11v1.2"/>',
    "flow": '<path d="M2 4h4l2 4h6M8 8l-2 4H2M11.5 5.5 14 8l-2.5 2.5"/>',
    "bulb": '<path d="M6 12.5h4M6.5 14.5h3M8 1.5a4.5 4.5 0 0 0-2.6 8.2c.4.3.6.7.6 1.2v.1h4v-.1c0-.5.2-.9.6-1.2A4.5 4.5 0 0 0 8 1.5Z"/>',
    "more": '<circle cx="3.5" cy="8" r="1"/><circle cx="8" cy="8" r="1"/><circle cx="12.5" cy="8" r="1"/>',
}
_RING = '<circle cx="6" cy="6" r="4.25" fill="none" stroke="currentColor" stroke-width="1.5"/>'
_SVG12 = {  # status glyphs: shape alone tells the states apart (todo/cancelled, in_progress/in_review)
    "todo": ("neutral", _RING),
    "cancelled": ("neutral", _RING + '<path d="M3.2 8.8 8.8 3.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>'),
    "proposed": ("neutral", '<circle cx="6" cy="6" r="4.25" fill="none" stroke="currentColor" stroke-width="1.5" stroke-dasharray="2.2 1.6"/>'),
    "planning": ("info", _RING + '<path d="M6 6V2.9A3.1 3.1 0 0 1 9.1 6Z" fill="currentColor"/>'),
    "in_progress": ("info", _RING + '<path d="M6 2.9A3.1 3.1 0 0 1 6 9.1Z" fill="currentColor"/>'),
    "in_review": ("review", _RING + '<path d="M6 6V2.9A3.1 3.1 0 1 1 2.9 6Z" fill="currentColor"/><circle cx="6" cy="6" r="1" fill="var(--surface-1)"/>'),
    "blocked": ("danger", '<path d="M4 1.2h4L10.8 4v4L8 10.8H4L1.2 8V4Z" fill="currentColor"/><path d="M3.8 6h4.4" stroke="var(--surface-1)" stroke-width="1.5" fill="none"/>'),
    "done": ("success", '<circle cx="6" cy="6" r="5.25" fill="currentColor"/><path d="M3.7 6.2 5.2 7.7 8.4 4.4" stroke="var(--surface-1)" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round"/>'),
    "live": ("success", '<circle cx="6" cy="6" r="3.2" fill="currentColor"/>'),
    "queued": ("warning", _RING + '<path d="M6 3.6V6l1.7 1" stroke="currentColor" stroke-width="1.3" fill="none" stroke-linecap="round"/>'),
    "lost": ("danger", '<path d="M6 1.4 11 10.4H1Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/><path d="M6 4.6v2.6M6 8.6v.1" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>'),
    "exited": ("neutral", '<circle cx="6" cy="6" r="3.2" fill="none" stroke="currentColor" stroke-width="1.5"/>'),
    "parked": ("neutral", '<path d="M4.2 2.5v7M7.8 2.5v7" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>'),
}
_SVG12["active"] = _SVG12["in_progress"]
_SVG12["stopped"] = _SVG12["exited"]

_ATTR_RE = re.compile(r"^[a-z-]+$")
_REL_RE = re.compile(r"^/(?!/)[\w./?=&%#:+~-]*$")
_PR_RE = re.compile(r"^https://github\.com/[\w.-]+/[\w.-]+/pull/\d+$")
_TAG_RE = re.compile(r"^[a-z][a-z0-9]*$")
_VOID = frozenset({"meta", "link", "br", "hr", "img"})


class Safe(str):
    """Markup that is already escaped."""


def h(x):
    return "" if x is None else _html.escape(str(x), quote=True)


def _render(x):
    if x is None or x is False:
        return ""
    if isinstance(x, Safe):
        return str(x)
    if isinstance(x, (list, tuple)):
        return "".join(_render(i) for i in x)
    return h(x)


def el(tag, *children, **attrs):
    """<tag attrs>children</tag>; text children and attribute values are escaped, Safe children are not.
    Attribute names must match [a-z-] (`class_` -> class, `http_equiv` -> http-equiv); None/False values are skipped."""
    if not _TAG_RE.match(tag):
        raise ValueError(f"bad tag {tag!r}")
    parts = []
    for k, v in attrs.items():
        name = k[:-1] if k == "class_" else k.replace("_", "-")
        if not _ATTR_RE.match(name):
            raise ValueError(f"bad attribute name {k!r}")
        if v is None or v is False:
            continue
        parts.append(f" {name}" if v is True else f' {name}="{h(v)}"')
    if tag in _VOID:
        return Safe(f"<{tag}{''.join(parts)}>")
    return Safe(f"<{tag}{''.join(parts)}>{_render(children)}</{tag}>")


def table(cols, rows, cls="table"):
    """cols: header labels; rows: iterables of cells (str escaped, Safe kept). A cell may be (value, 'num') to right-align."""
    head = el("tr", [el("th", c) for c in cols])
    body = []
    for r in rows:
        cells = []
        for c in r:
            if isinstance(c, tuple):
                cells.append(el("td", c[0], class_=c[1]))
            else:
                cells.append(el("td", c))
        body.append(el("tr", cells))
    return el("table", el("thead", head), el("tbody", body), class_=cls)


def badge(text, kind="muted"):
    return el("span", text, class_=f"badge badge--{kind}")


def kpi(label, value, note=None):
    """KPI card; an optional `note` (a definition) renders inside the card, below the value."""
    return el("div", el("div", label, class_="kpi-label"), el("div", value, class_="kpi-value"),
              el("p", note, class_="kpi-note") if note else None, class_="kpi")


def bar(pct, label=None, aria=None):
    """Horizontal bar; the fill is a bar--NN class (5% steps, 0-100), so no inline style (strict CSP).
    Without `label` the visible text is the percentage. A bar scaled to the largest row passes its count as
    `label` and "<n> of max <m>" as `aria`, so a share of the maximum is never printed as a rate."""
    try:
        v = max(0, min(100, int(round(float(pct)))))
    except (TypeError, ValueError):
        v = 0
    text = f"{v}%" if label is None else label
    return el("div", el("span", text, class_="mono"), class_=f"bar bar--{int(round(v / 5)) * 5}",
              aria_label=f"{v}%" if aria is None and label is None else aria)


def link(href, text):
    """Anchor for a relative path or a github PR URL; anything else is plain text."""
    href = "" if href is None else str(href)
    if _REL_RE.match(href) or _PR_RE.match(href):
        return el("a", text, href=href)
    return Safe(h(text))


def ts(iso):
    """Short local time with the full ISO string as a tooltip."""
    d = parse_ts(iso)
    if d is None:
        return Safe(h(iso)) if iso else Safe("")
    return el("span", d.astimezone().strftime("%m-%d %H:%M"), title=iso)


def dur(seconds):
    if seconds is None:
        return "-"
    s = int(round(seconds))
    if s < 60:
        return f"{s}s"
    if s < 3600:
        return f"{s // 60}m {s % 60:02d}s"
    if s < 86400:
        return f"{s // 3600}h {s % 3600 // 60:02d}m"
    return f"{s // 86400}d {s % 86400 // 3600}h"


def card(title, *body):
    return el("section", el("h2", title, class_="card-title"), *body, class_="card")


def empty(text):
    return el("p", text, class_="empty")


def _svg(inner, size=16):
    return Safe(f'<svg class="g" viewBox="0 0 16 16" width="{size}" height="{size}" fill="none" stroke="currentColor" '
                f'stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">{inner}</svg>')


def glyph(key):
    """12 px status glyph (aria-hidden) for an enum key, tinted with the tone's solid colour."""
    tone, inner = _SVG12.get(key, _SVG12["todo"])
    return Safe(f'<svg class="g gs-{tone}" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">{inner}</svg>')


def status(key, pill=False):
    """Shape glyph plus the human label of an enum (To do, In progress, ...); `pill` adds the tinted background."""
    tone = _SVG12.get(key, _SVG12["todo"])[0]
    label = LABELS.get(key, str(key).replace("_", " ").capitalize())
    return el("span", glyph(key), label, class_=f"st st--pill tone-{tone}" if pill else f"st fg-{tone}")


def crumbs(*items):
    """Breadcrumb trail; items are (href, text) pairs and the last one is the current page (plain text)."""
    out = []
    for n, (href, text) in enumerate(items):
        if n:
            out.append(el("span", "/", class_="sep", aria_hidden="true"))
        out.append(el("a", text, href=href) if n < len(items) - 1 else el("span", text, aria_current="location"))
    return el("nav", out, aria_label="Breadcrumb", class_="crumbs")


def _wordmark(org=None):
    return el("a", Safe('<svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.5" '
                        'stroke-linejoin="round" aria-hidden="true"><path d="M8 1.5 13.8 4.75v6.5L8 14.5 2.2 11.25v-6.5Z"/>'
                        '<path d="M8 5.2v5.6M5.1 6.9 8 8.5l2.9-1.6" stroke-width="1.2"/></svg>'), el("span", "Hivemill"),
              href=_with_org("/", org), class_="wordmark")


def _badges(shell):
    """Nav badges mean only "needs attention": Overview = Needs-you count, Agents = queued/blocked agents. Else none."""
    shell = shell or {}
    n_needs, n_agents = shell.get("needs", 0), shell.get("agents_attention", 0)
    return {"/": (n_needs, f"{n_needs} items need you", True), "/agents": (n_agents, f"{n_agents} agents queued or blocked", False)}


def _nav_count(path, shell):
    n, aria, bad = _badges(shell).get(path, (0, "", False))
    return el("span", n, class_="count count--bad" if bad else "count", aria_label=aria) if n else None


def org_dot(org):
    """Small round swatch in the org's colour (aria-hidden; the label next to it carries the meaning)."""
    return el("span", class_=f"org-dot org-dot-{org}" if org in _orgs.ORGS else "org-dot", aria_hidden="true")


def header_params(query, org=None):
    """The validated header state carried on every header link: org (a known org), project (obs_value) and range (a
    RANGES key). Unknown values are dropped, never echoed back."""
    q = query or {}
    proj = obs_value(q.get("project"))
    rng = q.get("range") if q.get("range") in {k for k, _, _ in RANGES} else None
    return {"org": org if org in _orgs.ORGS else None, "project": proj or None, "range": rng}


def _hlink(path, state, **change):
    st = {**state, **change}
    qs = "&".join(f"{k}={quote(str(st[k]), safe='')}" for k in ("org", "project", "range") if st.get(k))
    return f"{path}?{qs}" if qs else path


def org_filter(path, query, current):
    """Global factory (org) filter (G-028): segmented "All · Claude · Cursor" links to `path` with ?org= set; the
    project and range of the header stay on the links. `current` is the whitelisted org or None (= all)."""
    state = header_params(query, current)
    items = [el("a", "All", href=_hlink(path, state, org=None), aria_current="true" if current is None else None)]
    items += [el("a", org_dot(o), _orgs.label(o), href=_hlink(path, state, org=o), aria_current="true" if current == o else None,
                 class_=f"org-{o}") for o in _orgs.NAMES]
    return el("nav", items, class_="seg seg-links orgseg", aria_label="Factory")


def project_filter(path, query, org, projects):
    """Project selector: a closed <details> menu of links (no script). `projects` are (id, name) pairs."""
    state = header_params(query, org)
    cur = state["project"]
    names = dict(projects or ())
    label = "All projects" if not cur else names.get(cur, cur)
    items = [el("li", el("a", "All projects", href=_hlink(path, state, project=None), aria_current="true" if not cur else None))]
    items += [el("li", el("a", name, href=_hlink(path, state, project=pid), aria_current="true" if cur == pid else None))
              for pid, name in projects or ()]
    return el("details", el("summary", el("span", "Project", class_="sr-only"), label), el("ul", items, class_="hsel-menu"),
              class_="hsel", data_key="hdr-project")


def range_filter(path, query, org):
    """Time range: segmented 24h · 3d · 7d · 30d links (default 7d), the other header state kept."""
    state = header_params(query, org)
    cur = range_param(query or {})[0]
    return el("nav", [el("a", k, href=_hlink(path, state, range=k), title=lab, aria_current="true" if k == cur else None)
                      for k, _, lab in RANGES], class_="seg seg-links", aria_label="Time range")


def shell_header(title, shell, crumb_trail=None, org_nav=None, path=None):
    """Page header: breadcrumb + h1 on the left; the factory, project and time-range selectors on the right (G-069:
    no status pills; health lives on Resources, Needs-you on Overview). `org_nav` is the selector cluster."""
    shell_cls = "shellbar shellbar--task" if path and str(path).startswith("/task/") else "shellbar"
    return el("header", el("div", crumb_trail, el("h1", title), class_="titlebar"), el("div", org_nav, class_="cluster"),
              class_=shell_cls)


def health_banner(shell):
    """Box health as G-069 "!" banners (no pills): only what needs attention (warning or danger: memory low, usage
    cap, gate closed, usage high; a Cursor model on its usage limit). All normal or unknown shows nothing."""
    from web import cursor_limits

    shell = shell or {}
    items = []
    h = shell.get("health") or {}
    if h.get("tone") in ("danger", "warning") and h.get("text"):
        items.append((h["tone"], h["text"]))
    cur = shell.get("cursor") or {}
    if any(m.get("status") == "limited" for m in cur.get("models") or []):
        items.append(("warning", f"Cursor {cursor_limits.plan_line(cur)}"))
    if not items:
        return None
    return el("div", [el("p", f"! {text}", class_=f"banner banner--{tone}", role="status") for tone, text in items],
              class_="banners shell-health")


def _topbar(shell, org=None):
    return el("header", _wordmark(org), class_="topbar")


def _with_org(path, org):
    """`path` plus ?org= when an org filter is on (org is whitelisted by the caller: a known org name)."""
    return f"{path}?org={org}" if org in _orgs.ORGS else path


def _item_link(path, label, icon, active, shell, cls, size=16, org=None):
    """Nav link; a None `path` (a page not built yet) is a plain "soon" item with aria-disabled. Only the Main nav item (cls "nav-item") carries aria-current, so every page has exactly one.
    With an org filter on, the link keeps it (?org=), so the filter follows the owner from page to page."""
    if path is None:
        return el("span", _svg(_SVG16[icon], size), el("span", label), el("span", "soon", class_="soon"),
                  class_=" ".join(c for c in (cls, "nav-item--soon") if c), aria_disabled="true")
    here = path == active
    main_nav = cls == "nav-item"
    classes = " ".join(c for c in (cls, "nav-item--active" if here and main_nav else "is-current" if here else "") if c)
    count = _nav_count(path, shell)
    return el("a", _svg(_SVG16[icon], size), el("span", label), count,
              href=_with_org(path, org), class_=classes or None, aria_current="page" if here and main_nav else None)


def _shell_navs(active, shell, org=None):
    groups = [el("div", el("h2", g), el("ul", [el("li", _item_link(p, label, ic, active, shell, "nav-item", org=org))
                                              for p, label, ic in items]),
                 class_="navgroup") for g, items in GROUPS]
    side = el("nav", _wordmark(org), groups, el("div", data_theme_slot=True, class_="themebox"), aria_label="Main", class_="sidebar")
    flat = {p: (label, ic) for _, items in GROUPS for p, label, ic in items if p}
    tabs = [_item_link(p, flat[p][0], flat[p][1], active, shell, "tab", 20, org=org) for p in TABS]
    more_active = active not in TABS
    tabs.append(el("a", _svg(_SVG16["more"], 20), el("span", "More"), href="#more", class_="tab is-current" if more_active else "tab"))
    more = el("nav", [[el("h2", g), el("ul", [el("li", _item_link(p, label, ic, active, None, "", org=org))
                                            for p, label, ic in items])] for g, items in GROUPS],
              el("h2", "Theme"), el("div", data_theme_slot=True), id="more", aria_label="All sections", class_="more")
    return side, more, el("nav", tabs, aria_label="Primary tabs", class_="tabbar")


def page(title, body, active="/", refresh=True, shell=None, crumb_trail=None, org=None, path=None, query=None):
    """Full HTML5 document. `body` is trusted markup (the output of a view's render). `shell` is the dict from
    queries.shell_status plus "now"; without it the header omits the status cluster (error pages). `path` (the request
    path) turns on the org filter in the header; `org` is the whitelisted ?org= value (None = all) and `query` the
    request's query dict, kept on the filter's links."""
    org = org if org in _orgs.ORGS else None
    side, more, tabs = _shell_navs(active, shell, org)
    head = el("head",
              el("meta", charset="utf-8"),
              el("meta", name="viewport", content="width=device-width, initial-scale=1"),
              el("meta", name="color-scheme", content="light dark"),
              el("meta", http_equiv="refresh", content="60") if refresh else "",
              el("title", f"{title} - factory"),
              el("link", rel="icon", href="/static/favicon.svg", type="image/svg+xml"),
              el("link", rel="stylesheet", href="/static/app.css"),
              el("script", src="/static/theme.js", defer=True))
    org_nav = None
    if path and shell:
        org_nav = [org_filter(path, query, org), project_filter(path, query, org, shell.get("projects")),
                   range_filter(path, query, org)]
    main = el("div", shell_header(title, shell, crumb_trail, org_nav, path), health_banner(shell),
              el("main", Safe(body) if not isinstance(body, Safe) else body, id="content", class_="page"),
              more, class_="main")
    shell_el = el("div", _topbar(shell, org), side, main, class_="app")
    return "<!doctype html>\n" + str(el("html", head, el("body", el("a", "Skip to content", href="#content", class_="skip"), shell_el, tabs), lang="en"))
