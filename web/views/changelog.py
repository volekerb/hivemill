"""Changelog page: merged tasks as feed rows, by date (default) or by goal; ?goal=G-xxx filters to one goal."""
import re

from web import queries
from web.components import ago, chips, empty_state, feed, feed_row, href, section, segmented
from web.html import el

TITLE = "Changelog"

_GOAL_RE = re.compile(r"^G-\d+$")


def _epoch(g):
    return max((queries._epoch(i["merged"]) or 0 for i in g["items"]), default=0)


def data(ctx, **params):
    q = getattr(ctx, "query", None) or {}
    groups = sorted(queries.changelog(ctx.db), key=_epoch, reverse=True)
    want = q.get("goal", "")
    goal = want if _GOAL_RE.match(want) and any(g["goal_id"] == want for g in groups) else None
    shown = [g for g in groups if goal is None or g["goal_id"] == goal]
    return {"goal": goal, "goals": [{"goal_id": g["goal_id"], "goal_title": g["goal_title"], "count": len(g["items"])}
                                    for g in groups], "groups": shown, "total": sum(len(g["items"]) for g in shown),
            "group": "goal" if q.get("group") == "goal" else "date"}


def _day(iso, now):
    d = queries.parse_ts(iso)
    if d is None:
        return "Unknown date"
    delta = (now.astimezone().date() - d.astimezone().date()).days
    return "Today" if delta == 0 else "Yesterday" if delta == 1 else d.astimezone().strftime("%b %d")


def _row(i, goal_id, now):
    return feed_row(i["title"] or i["task_id"], f"/task/{i['task_id']}", goal=goal_id, pr_url=i["pr_url"], sha=i["sha"],
                    when=ago(i["merged"], now, " ago"), when_title=i["merged"])


def render(ctx, d):
    if not d["groups"]:
        return section("Shipped", empty_state("Nothing has been merged yet."))
    now, goal, mode = ctx.now, d["goal"], d["group"]
    seg = segmented("Group changelog", [("By date", href("/changelog", {"goal": goal}), mode == "date"),
                                        ("By goal", href("/changelog", {"goal": goal, "group": "goal"}), mode == "goal")])
    parts = [seg]
    if len(d["goals"]) > 1 or goal:
        keep = {"group": "goal" if mode == "goal" else None}
        items = [("All goals", None, href("/changelog", keep), goal is None)]
        items += [(g["goal_id"], g["count"], href("/changelog", {"goal": g["goal_id"], **keep}), goal == g["goal_id"])
                  for g in d["goals"] if g["goal_id"]]
        parts.append(chips("Goal", items))
    if mode == "goal":
        for g in d["groups"]:
            title = f"{g['goal_id']} - {g['goal_title'] or ''}" if g["goal_id"] else "No goal"
            parts.append(section(title, feed([_row(i, None, now) for i in g["items"]]), count=len(g["items"])))
    else:
        items = sorted(((i, g["goal_id"]) for g in d["groups"] for i in g["items"]),
                       key=lambda p: (queries._epoch(p[0]["merged"]) or 0, p[0]["task_id"]), reverse=True)
        days = {}
        for i, gid in items:
            days.setdefault(_day(i["merged"], now), []).append(_row(i, gid, now))
        for day, rows in days.items():
            parts.append(section(day, feed(rows), count=len(rows)))
    return el("div", *parts)
