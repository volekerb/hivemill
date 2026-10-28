"""Goals page and goal detail: progress, success criteria, owner-pending items. Never renders plan bodies."""
from types import SimpleNamespace

from web import queries
from web.html import CLASSES, LABELS, badge, bar, card, el, empty, kpi, link, table, ts

TITLE = "Goals"

ORDER = ("active", "planning", "proposed", "blocked")
_KIND = {"done": "ok", "active": "info", "in_progress": "info", "in_review": "info", "planning": "warn",
         "proposed": "warn", "todo": "muted", "blocked": "bad", "cancelled": "muted"}
_CRIT = {"met": ("met", "ok"), "in_progress": ("in progress", "info"), "untracked": ("untracked", "muted")}
TEAM_SCOPE_MAX = 120


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


def data(ctx, **params):
    gl = sorted(queries.goals(ctx.db), key=_rank)
    return {"goals": gl, "owner_pending": queries.owner_pending(ctx.db), "internal_blocked": queries.internal_blocked(ctx.db)}


def _owner_card(op):
    rows = []
    for g in op["goals"]:
        rows.append((link(f"/goal/{g['id']}", g["id"]), g["title"], status_badge(g["status"])))
    for t in op["tasks_blocked"]:
        rows.append((link(f"/task/{t['id']}", t["id"]), t["title"], badge("blocked: owner", "bad")))
    for t in op["judge_blocked_auto_merge"]:
        rows.append((link(f"/task/{t['task_id']}", t["task_id"]), t["title"], badge("auto-merge off", "warn")))
    for m in op["escalations"]:
        rows.append((el("span", f"msg {m['id']}", class_="mono"), f"{m['subject']} (from {m['sender']})", badge("escalation", "bad")))
    if not rows:
        return card("Waiting on owner", empty("Nothing is waiting on the owner."))
    return card("Waiting on owner", table(["Item", "What", "Why"], rows, cls="table table--compact"))


def _internal_card(tasks):
    """Neutral card: tasks parked by the queue or a freeze are deferred, not waiting on the owner."""
    if not tasks:
        return None
    rows = [(link(f"/task/{t['id']}", t["id"]), t["title"], badge("queued/deferred", "muted")) for t in tasks]
    return card(f"Blocked (internal): {len(tasks)}", table(["Item", "What", "State"], rows, cls="table table--compact"))


def _blocked_cell(g):
    own, internal = g["tasks_blocked_owner"], g["tasks_blocked"] - g["tasks_blocked_owner"]
    parts = ([badge(f"{own} owner", "bad")] if own else []) + ([badge(f"{internal} queued", "muted")] if internal else [])
    return el("span", *parts) if parts else (0, "num")


def _goal_table(goals):
    rows = []
    for g in goals:
        done, total = g["tasks_done"], g["tasks_total"]
        rows.append((el("span", link(f"/goal/{g['id']}", g["id"]), " ", g["title"]),
                     el("span", status_badge(g["status"]), " ", el("span", f"P{g['priority']}", class_="muted")),
                     el("div", bar(100 * done / total if total else 0), el("span", f"{done}/{total}", class_="muted")),
                     _blocked_cell(g), (g["open_prs"], "num")))
    return table(["Goal", "Status", "Done", "Blocked", "PRs"], rows, cls="table table--compact")


def render(ctx, d):
    goals = d["goals"]
    live = [g for g in goals if g["status"] not in ("done", "cancelled")]
    closed = [g for g in goals if g["status"] in ("done", "cancelled")]
    parts = [_owner_card(d["owner_pending"])]
    internal = _internal_card(d["internal_blocked"])
    if internal:
        parts.append(internal)
    parts.append(card("Open goals", _goal_table(live)) if live else card("Open goals", empty("No open goals.")))
    if closed:
        parts.append(card("Done", el("details", el("summary", f"{len(closed)} goals"), _goal_table(closed))))
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
    return g


def _detail_render(ctx, d):
    if not d.get("found"):
        return card("Goal", empty(f"No goal {d.get('id')}."))
    done, total = d["tasks_done"], d["tasks_total"]
    head = el("div",
              kpi("Status", status_badge(d["status"])), kpi("Priority", f"P{d['priority']}"),
              kpi("Project", d["project_id"] or "-"),
              kpi("Tasks", el("div", bar(100 * done / total if total else 0), f"{done}/{total}")),
              kpi("Created", ts(d["created"])), kpi("Updated", ts(d["updated"])), class_="grid")
    parts = [head, card(f"{d['id']} - {d['title']}", el("p", d["intent"]) if d.get("intent") else empty("No intent recorded."))]
    crit = []
    for c in d["criteria"]:
        label, kind = _CRIT[c["state"]]
        crit.append((c["n"], c["text"], badge(label, kind),
                     el("span", [el("span", link(f"/task/{t}", t), " ") for t in c["tasks"]]) if c["tasks"] else "-"))
    parts.append(card("Success criteria", table(["#", "Criterion", "State", "Tasks"], crit, cls="table table--compact")
                      if crit else empty("No success criteria.")))
    teams = [(t["id"], t["lead_agent"], status_badge(t["status"]), t["scope"]) for t in d["teams_detail"]]
    parts.append(card("Teams", table(["Team", "Lead", "Status", "Scope"], teams, cls="table table--compact")
                      if teams else empty("No team yet.")))
    rows = [(el("span", link(f"/task/{t['id']}", t["id"]), " ", t["title"]),
             el("span", t["role"], " ", el("span", t["assignee"] or "", class_="muted")), task_badge(t["status"], t["waits"]),
             link(t["pr_url"], "PR") if t["pr_url"] else "-", (t["attempts"], "num")) for t in d["tasks"]]
    parts.append(card("Tasks", table(["Task", "Role", "Status", "PR", "Tries"], rows, cls="table table--compact")
                      if rows else empty("No tasks yet.")))
    p = d["plan"]
    parts.append(card("Plan", el("p", f"Version {p['version']}, created ", ts(p["created"])) if p else empty("No plan yet.")))
    return el("div", *parts)


detail = SimpleNamespace(TITLE="Goal", data=_detail_data, render=_detail_render)
