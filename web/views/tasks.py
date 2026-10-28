"""Tasks list and task detail with the full PR cycle. Never renders specs, acceptance text, notes or verdict summaries."""
import re
from types import SimpleNamespace

from web import ci, queries
from web.queries import parse_ts
from web.html import LABELS, badge, card, dur, el, empty, kpi, link, table, ts
from web.views.goals import status_badge, task_badge

TITLE = "Tasks"

STATUSES = ("todo", "in_progress", "in_review", "blocked", "done", "cancelled")
REASON_MAX = 120
_SHA_RE = re.compile(r"^@?([0-9a-f]{7,40})$")
_STATUS_RE = re.compile(r"^status=([a-z_]+)")
_PR_SET_RE = re.compile(r"\bpr=|\bstatus=in_review\b")
_TASK_RE = re.compile(r"\bT-\d+\b")
_CI_KIND = {"green": "ok", "green at merge": "ok", "failing": "bad", "pending": "warn", "unknown": "muted"}
_VERDICT_OK = ("pass", "merge")


def _ci_for(t, merged):
    """CI label for a task row; only open PRs consult the cache (which never blocks)."""
    if t["status"] == "done" and t["id"] in merged:
        return "green at merge", []
    if t["status"] == "in_review" and t["pr_url"]:
        e = ci.get(t["pr_url"])
        return ci.summarize(e), (e or {}).get("checks", [])
    return None, []


def _rejections(con):
    """task id -> rejections, counted like queries.pr_cycle (verdict rows, plus judge-reject events without a judge verdict)."""
    rej, judge_v, judge_e = {}, {}, {}
    for r in con.execute("SELECT task_id, role, COUNT(*) n FROM verdicts WHERE verdict IN ('fail','reject') GROUP BY task_id, role"):
        rej[r["task_id"]] = rej.get(r["task_id"], 0) + r["n"]
        if r["role"] == "judge":
            judge_v[r["task_id"]] = r["n"]
    for r in con.execute("SELECT entity_id, COUNT(*) n FROM events WHERE entity='task' AND action='judge-reject' GROUP BY entity_id"):
        judge_e[r["entity_id"]] = r["n"]
    for tid, n in judge_e.items():
        rej[tid] = rej.get(tid, 0) + max(0, n - judge_v.get(tid, 0))
    return rej


def _merged(con):
    return {r[0] for r in con.execute("SELECT DISTINCT entity_id FROM events WHERE entity='task' AND action='merged'")}


def data(ctx, goal=None, status=None, **params):
    q = getattr(ctx, "query", None) or {}
    goal = goal or q.get("goal")
    status = status or q.get("status")
    known = {r[0] for r in ctx.db.execute("SELECT id FROM goals")}
    goal = goal if goal in known else None
    status = status if status in STATUSES else None
    merged, rej = _merged(ctx.db), _rejections(ctx.db)
    rows = []
    for t in queries.tasks(ctx.db, goal=goal, status=status):
        label, _ = _ci_for(t, merged)
        rows.append({"id": t["id"], "goal_id": t["goal_id"], "title": t["title"], "status": t["status"], "waits": t["waits"], "pr_url": t["pr_url"],
                     "ci": label, "rejections": rej.get(t["id"], 0), "attempts": t["attempts"]})
    return {"filter": {"goal": goal, "status": status}, "goals": sorted(known), "statuses": list(STATUSES), "tasks": rows}


def _ci_badge(label):
    return badge(label, _CI_KIND[label]) if label else "-"


def _filters(d):
    f = d["filter"]
    items = [el("span", link("/tasks", "all"), " ")]
    items += [el("span", link(f"/tasks?status={s}" + (f"&goal={f['goal']}" if f["goal"] else ""), LABELS.get(s, s)), " ") for s in d["statuses"]]
    note = f"Filtered: goal={f['goal'] or 'any'}, status={f['status'] or 'any'}" if f["goal"] or f["status"] else "All tasks"
    return el("p", note, " - ", items, class_="muted")


def render(ctx, d):
    rows = [(el("span", link(f"/task/{t['id']}", t["id"]), " ", t["title"]),
             link(f"/goal/{t['goal_id']}", t["goal_id"]), task_badge(t["status"], t["waits"]),
             el("span", link(t["pr_url"], "PR") if t["pr_url"] else "-", " ", _ci_badge(t["ci"])),
             el("span", f"{t['rejections']} rej / {t['attempts']} try", class_="mono")) for t in d["tasks"]]
    body = table(["Task", "Goal", "Status", "PR / CI", "Rej / tries"], rows, cls="table table--compact") if rows \
        else empty("No tasks match.")
    return el("div", _filters(d), card("Tasks", body))


# ── detail ──

def _step_text(i):
    """Label plus the few structured details that are safe to show (never free-text notes)."""
    lab, det = i["label"], i.get("detail") or ""
    kind = i["kind"]
    if kind == "verdict":
        role, verdict = lab.split(" ", 1)
        return f"{role}: {'pass' if verdict in _VERDICT_OK else 'fail'} ({verdict})", i.get("head")
    if kind == "agent":
        return lab, None
    if lab == "update":
        m = _STATUS_RE.match(det)
        return (f"status -> {m.group(1)}" if m else "update"), None
    if lab == "judge-blocked":
        return "judge blocked: " + queries._t(det, REASON_MAX), None
    if lab in ("merged", "merge-failed", "merge-post-error"):
        m = _SHA_RE.match(det)
        return lab.replace("merge-post-error", "merge post-step error"), (m.group(1)[:8] if m else None)
    if lab == "add":
        return "task created", None
    return lab, None


def _fkind(i):
    lab = i["label"]
    if i["kind"] == "verdict":
        return "ok" if lab.split(" ", 1)[1] in _VERDICT_OK else "bad"
    if lab == "merged":
        return "ok"
    if lab in ("judge-reject", "judge-blocked", "merge-failed", "reopen"):
        return "bad"
    return "muted"


def _detail_data(ctx, id=None, **params):
    con = ctx.db
    rows = queries.tasks(con)
    t = next((r for r in rows if r["id"] == id), None)
    if t is None:
        return {"id": id, "found": False}
    cyc = queries.pr_cycle(con, id)
    merged_ev = [i for i in cyc["items"] if i["kind"] == "event" and i["label"] == "merged"]
    label, checks = _ci_for(t, {id} if merged_ev else set())
    start = parse_ts(t["created"])
    end = parse_ts(merged_ev[-1]["ts"]) if merged_ev else (parse_ts(t["updated"]) if t["status"] in ("done", "cancelled") else ctx.now)
    total = (end - start).total_seconds() if start and end else None
    if total is not None and total < 0:   # clock skew / out-of-order timestamps: unknown, never clamped
        total = None
    follow = [{"id": r["id"], "title": r["title"], "status": r["status"], "waits": r["waits"]} for r in rows if r["follow_up_of"] == id]
    steps = []
    pr_seen = False
    # a merge verdict and the merged event usually share a second: the merge itself comes last
    for i in sorted(cyc["items"], key=lambda i: (parse_ts(i["ts"]) or ctx.now, i["label"] == "merged")):
        if i["kind"] == "event" and i["label"] == "note":
            continue
        text, sha = _step_text(i)
        tone = _fkind(i)
        if not pr_seen and i["kind"] == "event" and i["label"] == "update" and _PR_SET_RE.search(i.get("detail") or ""):
            pr_seen = True
            text, tone = "PR opened", "info"
        steps.append({"ts": i["ts"], "actor": i["actor"], "kind": i["kind"], "text": text, "sha": sha, "tone": tone,
                      "links": sorted(set(_TASK_RE.findall(i.get("detail") or ""))) if i["label"] == "follow-up" else []})
    return {"id": id, "found": True, "title": t["title"], "goal_id": t["goal_id"], "role": t["role"], "status": t["status"], "waits": t["waits"],
            "assignee": t["assignee"], "pr_url": t["pr_url"], "follow_up_of": t["follow_up_of"], "follow_ups": follow,
            "created": t["created"], "merged": bool(merged_ev), "ci": label, "ci_checks": checks,
            "rejections": cyc["rejections"], "retries": cyc["retries"], "attempts": cyc["attempts"], "reopens": cyc["reopens"],
            "total_s": total, "steps": steps}


def _detail_render(ctx, d):
    if not d.get("found"):
        return card("Task", empty(f"No task {d.get('id')}."))
    head = el("div",
              kpi("Status", task_badge(d["status"], d["waits"])), kpi("Total time", dur(d["total_s"])),
              kpi("Rejections", d["rejections"]), kpi("Retries", d["retries"]), kpi("CI", _ci_badge(d["ci"])), class_="grid")
    info = el("p", link(f"/goal/{d['goal_id']}", d["goal_id"]), " - ", d["title"], " - ", d["role"], " ",
              el("span", d["assignee"] or "", class_="muted"), " ", link(d["pr_url"], "PR") if d["pr_url"] else "")
    items = []
    for s in d["steps"]:
        extra = [el("span", f" @{s['sha']}", class_="mono")] if s["sha"] else []
        extra += [el("span", " ", link(f"/task/{x}", x)) for x in s["links"]]
        items.append(el("div", ts(s["ts"]), " ", badge(s["text"], s["tone"]), " ", el("span", s["actor"] or "", class_="muted"),
                        *extra, class_="timeline-item"))
    parts = [head, card("Task", info), card("PR cycle", el("div", *items, class_="timeline") if items else empty("No events yet."))]
    fu = [el("span", link(f"/task/{f['id']}", f["id"]), " ", f["title"], " ", task_badge(f["status"], f["waits"]), " ") for f in d["follow_ups"]]
    if d["follow_up_of"]:
        fu.insert(0, el("span", "follow-up of ", link(f"/task/{d['follow_up_of']}", d["follow_up_of"]), " "))
    if fu:
        parts.append(card("Follow-ups", el("p", *fu)))
    return el("div", *parts)


detail = SimpleNamespace(TITLE="Task", data=_detail_data, render=_detail_render)
