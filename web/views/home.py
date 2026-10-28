"""Home: headline KPIs with links to the detail pages."""
from web import queries
from web.html import el, kpi, link

TITLE = "Overview"


def data(ctx, **params):
    gl = queries.goals(ctx.db)
    by_status = {}
    for g in gl:
        by_status[g["status"]] = by_status.get(g["status"], 0) + 1
    ag = queries.agents(ctx.db, now=ctx.now)
    u = ctx.usage_fn() if ctx.usage_fn else {}
    return {"utilization": queries.utilization_sample(ctx.db, now=ctx.now),
            "live_agents": sum(1 for a in ag if a["status"] == "live"),
            "queued_agents": sum(1 for a in ag if a["status"] == "queued"),
            "open_prs": sum(g["open_prs"] for g in gl),
            "goals_by_status": by_status,
            "owner_pending": sum(len(v) for v in queries.owner_pending(ctx.db).values()),
            "usage": {"five_hour": u.get("five_hour"), "seven_day": u.get("seven_day"),
                      "fresh": u.get("fresh"), "age_min": u.get("age_min")}}


def _pct(v):
    return "n/a" if v is None else f"{v:.0f}%"


def render(ctx, d):
    u = d["usage"]
    goals = ", ".join(f"{n} {s}" for s, n in sorted(d["goals_by_status"].items())) or "none"
    ut = d["utilization"]
    ut_text = ("utilization: no fresh tick sample" if ut is None
               else ut["line"] + (" (UNDER TARGET)" if ut["under"] else ""))
    return el("div",
              el("div",
                 link("/agents", kpi("Live agents", f"{d['live_agents']} (+{d['queued_agents']} queued)")),
                 link("/tasks", kpi("Open PRs", d["open_prs"])),
                 link("/goals", kpi("Goals", goals)),
                 kpi("Utilization", ut_text),
                 link("/goals", kpi("Waiting on owner", d["owner_pending"])),
                 link("/resources", kpi("5h / week usage", f"{_pct(u['five_hour'])} / {_pct(u['seven_day'])}")),
                 class_="grid"))
