"""Changelog page: merged tasks grouped by goal, newest goal activity first; ?goal=G-xxx filters to one goal."""
import re

from web import queries
from web.html import card, el, empty, link, table

TITLE = "Changelog"

_GOAL_RE = re.compile(r"^G-\d+$")


def _epoch(g):
    return max((queries._epoch(i["merged"]) or 0 for i in g["items"]), default=0)


def data(ctx, **params):
    groups = sorted(queries.changelog(ctx.db), key=_epoch, reverse=True)
    want = (getattr(ctx, "query", None) or {}).get("goal", "")
    goal = want if _GOAL_RE.match(want) and any(g["goal_id"] == want for g in groups) else None
    shown = [g for g in groups if goal is None or g["goal_id"] == goal]
    return {"goal": goal, "goals": [{"goal_id": g["goal_id"], "goal_title": g["goal_title"], "count": len(g["items"])}
                                    for g in groups], "groups": shown, "total": sum(len(g["items"]) for g in shown)}


def render(ctx, d):
    if not d["groups"]:
        return card("Shipped", empty("Nothing has been merged yet."))
    parts = []
    if len(d["goals"]) > 1 or d["goal"]:
        nav = [el("span", link("/changelog", "All goals"), " ")]
        nav += [el("span", link(f"/changelog?goal={g['goal_id']}", g["goal_id"]), " ") for g in d["goals"] if g["goal_id"]]
        parts.append(el("p", *nav, class_="muted"))
    for g in d["groups"]:
        rows = [(el("span", (i["merged"] or "")[:10], class_="mono"),
                 el("span", link(f"/task/{i['task_id']}", i["title"] or i["task_id"])),
                 link(i["pr_url"], "PR") if i["pr_url"] else "-",
                 el("span", (i["sha"] or "")[:8], class_="mono")) for i in g["items"]]
        title = f"{g['goal_id']} - {g['goal_title'] or ''}" if g["goal_id"] else "No goal"
        parts.append(card(title, table(["Date", "Change", "PR", "Commit"], rows, cls="table table--compact")))
    return el("div", *parts)
