"""Goals page and goal detail: progress, success criteria, owner-pending items. Never renders plan bodies."""
from threading import local as _thread_local
from types import SimpleNamespace

from web import charts, queries
from web.components import (COLLAPSED, GROUP_ORDER, ago, chip, data_table, disclosure, empty_state, needs_you_row, row_status,
                            section, segmented)
from web.html import CLASSES, LABELS, badge, card, crumbs, el, empty, link, table, ts
from web.views.home import _needs

TITLE = "Goals"

ORDER = ("active", "planning", "proposed", "blocked")
_KIND = {"done": "ok", "active": "info", "in_progress": "info", "in_review": "info", "planning": "warn",
         "proposed": "warn", "todo": "muted", "blocked": "bad", "cancelled": "muted"}
TEAM_SCOPE_MAX = 120
SORTS = ("priority", "progress", "updated")
INTENT_OPEN_MAX = 480   # a longer intent collapses behind a disclosure


class DetailView:
    """A detail page whose h1 is the subject's own title. The server reads `TITLE` after `data()` on the same thread, so
    data() leaves the title in a thread-local and web/server.py needs no change."""

    def __init__(self, fallback, data, render):
        self._fallback, self._data, self.render, self._local = fallback, data, render, _thread_local()

    def data(self, ctx, **params):
        self._local.title = None
        d = self._data(ctx, **params)
        self._local.title = d.get("title") if d.get("found") else None
        return d

    @property
    def TITLE(self):
        return getattr(self._local, "title", None) or self._fallback


def not_found(kind, ident, back, back_label, crumb):
    """Calm not-found state: "No goal G-999" plus a link back to the list."""
    return el("div", crumb, empty_state(f"No {kind} {ident}", (back, back_label)))


def status_badge(status):
    cls = f"badge badge--{_KIND.get(status, 'muted')}"
    if f"status--{status}" in CLASSES:
        cls += f" status--{status}"
    return el("span", LABELS.get(status, status), class_=cls)


def task_badge(status, waits=None):
    """A task status badge. Blocked splits by who it waits on: red only when the owner must act, neutral otherwise."""
    if status == "blocked":
        return badge("blocked: owner", "bad") if waits == "owner" else badge("blocked (internal)", "muted")
    return status_badge(status)


def _rank(g):
    return (ORDER.index(g["status"]) if g["status"] in ORDER else len(ORDER), 99 if g["priority"] is None else g["priority"], g["id"])


def data(ctx, sort=None, **params):
    q = getattr(ctx, "query", None) or {}
    sort = sort or q.get("sort")
    gl = sorted(queries.goals(ctx.db), key=_rank)
    return {"goals": gl, "owner_pending": queries.owner_pending(ctx.db), "internal_blocked": queries.internal_blocked(ctx.db),
            "needs_you": _needs(ctx), "criteria": queries.criteria_by_goal(ctx.db), "last_activity": queries.last_activity_by_goal(ctx.db),
            "sort": sort if sort in SORTS else None}


def _internal_card(tasks):
    """Neutral card: tasks parked by the queue or a freeze are deferred, not waiting on the owner."""
    if not tasks:
        return None
    rows = [(link(f"/task/{t['id']}", t["id"]), t["title"], badge("queued/deferred", "muted")) for t in tasks]
    return card(f"Blocked (internal): {len(tasks)}", table(["Item", "What", "State"], rows, cls="table table--compact"))


def _blocked_cell(g):
    own, internal = g["tasks_blocked_owner"], g["tasks_blocked"] - g["tasks_blocked_owner"]
    parts = ([badge(f"{own} owner", "bad")] if own else []) + ([badge(f"{internal} queued", "muted")] if internal else [])
    return el("span", *parts, class_="gblk") if parts else None


def _sorted(goals, sort, last):
    if sort == "priority":
        return sorted(goals, key=lambda g: (99 if g["priority"] is None else g["priority"], g["id"]))
    if sort == "progress":
        return sorted(goals, key=lambda g: (-(g["tasks_done"] / g["tasks_total"] if g["tasks_total"] else 0), g["id"]))
    if sort == "updated":
        return sorted(goals, key=lambda g: (queries._epoch(last.get(g["id"]) or g["updated"]) or 0, g["id"]), reverse=True)
    return goals


def _goal_row(g, d, now):
    done, total = g["tasks_done"], g["tasks_total"]
    prs = f"{g['open_prs']} open PR" + ("s" if g["open_prs"] != 1 else "") if g["open_prs"] else None
    crit = d["criteria"].get(g["id"])
    act = ago(d["last_activity"].get(g["id"]) or g["updated"], now)
    title = el("span", el("a", g["title"], href=f"/goal/{g['id']}"),
               el("span", f"P{g['priority']}", class_="chip-p") if g["priority"] is not None else None, class_="gtitle")
    meta = el("div", charts.meter(done, total, f"{done}/{total} tasks"),
              el("span", f"criteria {crit[0]}/{crit[1]}", class_="gprs") if crit else None,
              el("span", prs, class_="gprs") if prs else None, _blocked_cell(g),
              el("span", f"last activity {act}", class_="gact") if act else None, class_="gm")
    return el("li", row_status(g["status"]), el("span", g["id"], class_="mono lrow-id"), title, meta, class_="goalrow")


def _goal_list(goals, d, now):
    return el("ul", [_goal_row(g, d, now) for g in goals], class_="lrow-list")


def _chips(goals):
    n = lambda *st: sum(1 for g in goals if g["status"] in st)   # noqa: E731
    items = [("Active", n("active", "planning"), "#open-goals"), ("Blocked", n("blocked"), "#open-goals"),
             ("Proposed", n("proposed"), "#open-goals"), ("Done", n("done", "cancelled"), "#done-goals")]
    return el("div", [chip(t, c, h_) for t, c, h_ in items], class_="chips gchips")


def render(ctx, d):
    goals = d["goals"]
    live = [g for g in goals if g["status"] not in ("done", "cancelled")]
    closed = [g for g in goals if g["status"] in ("done", "cancelled")]
    sort = d.get("sort")
    parts = [_chips(goals)]
    needs = d["needs_you"]
    parts.append(section("Needs you", el("ul", [needs_you_row(i) for i in needs], class_="lrow-list") if needs
                         else empty_state("Nothing is waiting on the owner."), count=len(needs) or None))
    internal = _internal_card(d["internal_blocked"])
    if internal:
        parts.append(internal)
    order = segmented("Sort open goals", [("Default", "/goals", sort is None)] +
                      [(s.capitalize(), f"/goals?sort={s}", sort == s) for s in SORTS])
    body = [order, _goal_list(_sorted(live, sort, d["last_activity"]), d, ctx.now)] if live else [empty_state("No open goals.")]
    parts.append(el("div", section("Open goals", *body, count=len(live) or None), id="open-goals"))
    if closed:
        parts.append(el("div", disclosure("Done", _goal_list(closed, d, ctx.now), key="goals-done", count=len(closed)), id="done-goals"))
    return el("div", *parts)


# ── detail ──

def _detail_data(ctx, id=None, **params):
    g = queries.goal_detail(ctx.db, id) if id else None
    if g is None:
        return {"id": id, "found": False}
    g["found"] = True
    g["teams_detail"] = [dict(t, scope=queries._t(t["scope"], TEAM_SCOPE_MAX)) for t in queries.teams(ctx.db) if t["goal_id"] == id]
    g["tasks"] = queries.tasks(ctx.db, goal=id)
    p = ctx.db.execute("SELECT version, created FROM plans WHERE goal_id=? ORDER BY version DESC LIMIT 1", (id,)).fetchone()
    g["plan"] = {"version": p["version"], "created": p["created"]} if p else None
    ids = {t["id"] for t in g["tasks"]}
    g["blocking"] = [i for i in _needs(ctx) if i.get("ref") == id or i.get("ref") in ids]
    return g


def _crumb(ident):
    return crumbs(("/goals", "Goals"), (f"/goal/{ident}", ident))


def _blocking(d, now):
    """WHAT'S BLOCKING: needs-you items of this goal, then its queued (internal) blocked tasks. None when there are none."""
    shown = {i["ref"] for i in d["blocking"]}
    rows = [needs_you_row(i) for i in d["blocking"]]
    for t in d["tasks"]:
        if t["status"] == "blocked" and t["id"] not in shown:
            rows.append(needs_you_row({"kind": "Queued task", "tone": "neutral", "subject": queries._strip_tags(t["title"]), "ref": t["id"],
                                       "href": f"/task/{t['id']}", "age": f"since {ago(t['updated'], now)}" if ago(t["updated"], now) else ""}))
    return section("What's blocking", el("ul", rows, class_="lrow-list"), count=len(rows)) if rows else None


def _met(d):
    """{criterion n: met} by the /goals rule (D5): every non-cancelled [SCn]-tagged task is done, and at least one exists."""
    by_id = {t["id"]: t for t in d["tasks"]}
    out = {}
    for c in d["criteria"]:
        live = [by_id[t]["status"] for t in c["tasks"] if t in by_id and by_id[t]["status"] != "cancelled"]
        out[c["n"]] = bool(live) and all(s == "done" for s in live)
    return out


def _criteria(d):
    by_id = {t["id"]: t for t in d["tasks"]}
    rows = []
    mets = _met(d)
    for c in d["criteria"]:
        met = mets[c["n"]]
        linked = [el("a", row_status(by_id[t]["status"]), href=f"/task/{t}", title=f"{t} {LABELS.get(by_id[t]['status'], '')}",
                     aria_label=f"{t}, {LABELS.get(by_id[t]['status'], '')}") for t in c["tasks"] if t in by_id]
        rows.append(el("li", row_status("done" if met else "todo"), el("span", "Met" if met else "Open", class_="sr-only"),
                       el("span", f"SC{c['n']}", class_="mono lrow-id"), el("span", c["text"], class_="crit-text"),
                       el("span", linked, class_="crit-tasks") if linked else None, class_="lrow"))
    return section("Success criteria", el("ul", rows, class_="lrow-list") if rows else empty_state("No success criteria."),
                   count=f"{sum(mets.values())}/{len(d['criteria'])}" if rows else None)


def _tasks_table(d, now):
    cols = ({"key": "status", "label": "", "cls": "c-st"}, {"key": "id", "label": "ID", "cls": "c-id mono"},
            {"key": "title", "label": "Task", "cls": "c-title"}, {"key": "assignee", "label": "Assignee", "cls": "c-who"},
            {"key": "pr", "label": "PR", "cls": "c-who"}, {"key": "tries", "label": "Tries", "cls": "c-age num"})
    groups = []
    for s in GROUP_ORDER:
        rows = [t for t in d["tasks"] if t["status"] == s]
        if not rows:
            continue
        cells = [[row_status(t["status"]), t["id"],
                  el("span", el("a", t["title"], href=f"/task/{t['id']}"), " ", task_badge("blocked", t["waits"]) if s == "blocked" else None),
                  t["assignee"] or t["role"], link(t["pr_url"], "PR") if t["pr_url"] else "", t["attempts"] if t["attempts"] else ""]
                 for t in rows]
        groups.append({"key": s, "label": LABELS[s], "count": len(rows), "rows": cells, "collapsed": s in COLLAPSED})
    return section("Tasks", data_table(cols, groups, key="goal-tasks", empty="No tasks yet."), count=len(d["tasks"]) or None)


def _intent(d):
    text = d.get("intent")
    if not text:
        return None
    body = el("p", text, class_="intent")
    if len(text) > INTENT_OPEN_MAX or text.count("\n") >= 6:
        return el("div", disclosure("Intent", body, key="goal-intent"), class_="sect")
    return section("Intent", body)


def _rail(d):
    teams = [el("li", el("span", t["id"], class_="mono"), " ", el("span", t["lead_agent"]), " ", status_badge(t["status"]),
                el("div", t["scope"], class_="muted") if t["scope"] else None) for t in d["teams_detail"]]
    p = d["plan"]
    defs = [("Plan", f"v{p['version']} · " if p else "No plan yet", ts(p["created"]) if p else None),
            ("Created", ts(d["created"]), None), ("Updated", ts(d["updated"]), None)]
    return el("aside", el("h2", "Teams"), el("ul", teams, class_="rail-list") if teams else empty("No team yet."),
              el("dl", [[el("dt", k), el("dd", a, b)] for k, a, b in defs], class_="rail-dl"), class_="rail", aria_label="Goal details")


def _detail_render(ctx, d):
    if not d.get("found"):
        return not_found("goal", d.get("id"), "/goals", "Back to Goals", _crumb(d.get("id") or "?"))
    done, total = d["tasks_done"], d["tasks_total"]
    cr = d["criteria"]
    tagged = any(c["tasks"] for c in cr)
    summary = f"{done}/{total} tasks" + (f" · {sum(_met(d).values())}/{len(cr)} criteria" if tagged else "")
    team = d["teams_detail"][0]["id"] if d["teams_detail"] else None
    meta = el("p", status_badge(d["status"]), " · ", f"P{d['priority']}" if d["priority"] is not None else "no priority",
              " · ", team or "no team", " · updated ", ts(d["updated"]), class_="metarow")
    main = el("div", meta, charts.meter(done, total, summary), _blocking(d, ctx.now), _criteria(d), _tasks_table(d, ctx.now), _intent(d),
              class_="gd-main")
    return el("div", _crumb(d["id"]), el("div", main, _rail(d), class_="gd"))


detail = DetailView("Goal", _detail_data, _detail_render)
