"""Quality page: rejections, gate refusals, retries, CI pass rate, per-team table, rejections per week.

Formulas are the ones in the queries.quality docstring. Rejections per week use the same rule
(fail/reject verdict rows, plus judge-reject events that have no judge verdict row).
"""
import datetime as dt

from web import queries
from web.html import badge, bar, card, el, empty, kpi, table

TITLE = "Quality"
WEEKS = 12
FORMULA = ("Rejections per merged PR = rejection events / merged PRs; retries = attempts beyond the first; "
           "CI pass rate = merged / (merged + tasks the judge blocked for red checks).")
NOTES = ("Rejections count failed QA/review verdicts, plus old judge rejections that left no verdict row. "
         "Merged counts tasks with a merge event. Tasks without a team are counted in every team of their goal. "
         "Rework time is 0 when a task was never sent back.")


def _pct(x):
    return "-" if x is None else f"{round(100 * x)}%"


def _rpm(x):
    return "-" if x is None else f"{x:.2f}"


def _week(iso):
    d = queries.parse_ts(iso)
    if d is None:
        return None
    day = d.astimezone().date()
    return (day - dt.timedelta(days=day.weekday())).isoformat()


def _weekly(con):
    counts = {}
    vcount = {}
    for r in con.execute("SELECT task_id, role, created FROM verdicts WHERE verdict IN ('fail','reject')"):
        w = _week(r["created"])
        if w:
            counts[w] = counts.get(w, 0) + 1
        if r["role"] == "judge":
            vcount[r["task_id"]] = vcount.get(r["task_id"], 0) + 1
    evs = {}
    for r in con.execute("SELECT entity_id, ts FROM events WHERE entity='task' AND action='judge-reject' ORDER BY id"):
        evs.setdefault(r["entity_id"], []).append(r["ts"])
    for tid, tss in evs.items():
        extra = max(0, len(tss) - vcount.get(tid, 0))
        for t in tss[len(tss) - extra:]:
            w = _week(t)
            if w:
                counts[w] = counts.get(w, 0) + 1
    return [{"week": w, "rejections": counts[w]} for w in sorted(counts)][-WEEKS:]


ADDITIVE = ("tasks", "merged", "rejections", "retries", "merged_first_try", "merged_after_rejection",
            "rej_reviewer", "rej_qa", "rej_judge", "gate_judge_blocked", "gate_merge_failed", "gate_refusals")


def _with_unassigned(con, q):
    """Add the team_id-NULL tasks of a team's goal to that team, the way bin/factory scorecard_rows attributes them.

    The per-team ratios are recomputed after the addition, so they never go stale.
    """
    extra = q["unassigned"]
    goal = {r["id"]: r["goal_id"] for r in con.execute("SELECT id, goal_id FROM teams")}
    out = []
    for t in q["teams"]:
        e = extra.get(goal.get(t["team_id"]))
        if e:
            t = dict(t)
            for k in ADDITIVE:
                t[k] += e[k]
            t["rejections_per_merged"] = round(t["rejections"] / t["merged"], 2) if t["merged"] else None
        out.append(t)
    return out


def data(ctx, **params):
    q = queries.quality(ctx.db)
    last = {}
    for r in ctx.db.execute("SELECT team_id, score, created FROM evaluations ORDER BY id"):
        last[r["team_id"]] = {"score": r["score"], "date": (r["created"] or "")[:10]}
    teams = [dict(t, eval_date=(last.get(t["team_id"]) or {}).get("date")) for t in _with_unassigned(ctx.db, q)]
    return {"overall": q["overall"], "teams": teams, "weeks": _weekly(ctx.db), "formula": FORMULA}


def render(ctx, d):
    o = d["overall"]
    scored = [t for t in d["teams"] if t["score"] is not None]
    scores = ", ".join(f"{t['team_id']} {t['score']}/5" for t in scored) or "-"
    retries = (o["retries"] / o["merged"]) if o["merged"] else None
    rpm = o["rejections_per_merged"]

    def k(label, value, definition):
        return kpi(label, value, definition)

    head = el("div",
              k("Merged PRs", o["merged"], "Distinct tasks with a merge event."),
              k("Merged first try", o["merged_first_try"], "Merged tasks that were never rejected."),
              k("Merged after >= 1 rejection", o["merged_after_rejection"],
                "Merged tasks rejected at least once before they merged."),
              k("Rejections", o["rejections"],
                "Reviewer, QA and judge fail or reject verdicts, plus judge rejections without a verdict row."),
              k("Rejections per merged PR", _rpm(rpm),
                "Rejections divided by merged PRs."),
              k("Gate refusals", o["gate_refusals"],
                "Judge-blocked and merge-failed events: stale verdict, red or pending CI, conflicts."),
              k("Rejections by cause", "n/a, see G-013", "No cause is stored yet (counts incidents, not events)."),
              k("Retries per merged task", "-" if retries is None else f"{retries:.2f}",
                "Attempts beyond the first, per merged task."),
              k("CI pass rate", _pct(o["ci_pass_rate"]), "Merged / (merged + tasks the judge blocked for red checks)."),
              k("Latest team scores", scores, "Newest evaluation per team."), class_="grid")
    formula = el("p", d["formula"], class_="muted")
    notes = el("p", NOTES, class_="muted")
    if d["teams"]:
        rows = [(t["team_id"],
                 el("span", f"{t['merged']}/{t['tasks']}"), (f"{t['rejections']} / {t['retries']}", "num"),
                 (_rpm(t["rejections_per_merged"]), "num"), (t["gate_refusals"], "num"),
                 el("span", badge(f"{t['score']}/5", "info"), " ", el("span", t["eval_date"] or "", class_="muted"))
                 if t["score"] is not None else el("span", "-", class_="muted")) for t in d["teams"]]
        teams = card("Per team", table(["Team", "Merged / tasks", "Rejections / retries", "Rejections per merged PR",
                                        "Gate refusals", "Score"], rows, cls="table table--compact"))
    else:
        teams = card("Per team", empty("No teams yet."))
    src = card("Rejections and gate refusals by source", table(["Source", "Count", "Kind"], [
        ("Reviewer", (o["rej_reviewer"], "num"), "rejection"),
        ("QA", (o["rej_qa"], "num"), "rejection"),
        ("Judge", (o["rej_judge"], "num"), "rejection"),
        ("Gate: judge-blocked", (o["gate_judge_blocked"], "num"), "gate refusal"),
        ("Gate: merge-failed", (o["gate_merge_failed"], "num"), "gate refusal")], cls="table table--compact"))
    if d["weeks"]:
        top = max(w["rejections"] for w in d["weeks"])
        weeks = card("Rejections per week", table(["Week of", "Rejections"], [
            (w["week"], bar(100 * w["rejections"] / top, w["rejections"], f"{w['rejections']} of max {top}"))
            for w in d["weeks"]], cls="table table--compact"))
    else:
        weeks = card("Rejections per week", empty("No rejections recorded."))
    return el("div", head, formula, notes, teams, src, weeks)
