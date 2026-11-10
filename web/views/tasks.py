"""Tasks list and task detail with the full PR cycle. Never renders specs, acceptance text, notes or verdict summaries."""
import re
from types import SimpleNamespace

from web import ci, queries
from web.queries import parse_ts
from web.components import (data_table, disclosure, empty_state, filter_bar, ago, chips, ctx_org, href, org_badge, org_href, pr_label, row_status,
                            GROUP_ORDER, COLLAPSED)
from web.prices import PRICES_AS_OF
from web.tokens import attribute, rollup, task_usage
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


# sort keys: request input picks a key of this dict and never reaches SQL (sorting happens here, in Python)
SORT_KEYS = {
    "title": lambda t: (t["title"] or "").lower(),
    "goal": lambda t: t["goal_id"] or "",
    "assignee": lambda t: t["assignee"] or "",
    "pr": lambda t: int(pr_label(t["pr_url"])[1:]) if pr_label(t["pr_url"]).startswith("#") else 0,
    "rejections": lambda t: t["rejections"],
    "updated": lambda t: t["updated"] or "",
}
METRIC_KEYS = {"cost": "cost", "tokens": "tokens", "wall": "wall_s"}   # sort key -> row field; None sorts last in both directions
SORT_KEYS.update({k: (lambda t, fld=fld: t[fld]) for k, fld in METRIC_KEYS.items()})
DEFAULT_SORT = ("updated", "desc")
GOAL_CHIPS = 6


def _metrics(ctx, tasks):
    """task id -> {cost, tokens, wall_s, na}: one rollup of the TokenIndex snapshot per page load (never waits: an index
    that is not ready gives None, shown "-"). Cursor tasks have no token counts: na=True, shown "n/a", never 0.
    wall_s = created -> merge event for merged tasks, else None."""
    snap = ctx.tokens() if getattr(ctx, "tokens", None) else None
    by_task = {}
    if snap and snap.get("ready"):
        by_task = rollup(snap, attribute(snap, ctx.db))["by_task"]
    merged_at = {}
    for r in ctx.db.execute("SELECT entity_id, MAX(ts) ts FROM events WHERE entity='task' AND action='merged' GROUP BY entity_id"):
        merged_at[r["entity_id"]] = r["ts"]
    gorgs = queries.goal_orgs(ctx.db)
    out = {}
    for t in tasks:
        m = {"cost": None, "tokens": None, "wall_s": None, "na": queries.org_of_goal(gorgs, t["goal_id"]) == "cursor"}
        e = by_task.get(t["id"])
        if e is not None and not m["na"]:
            tok = sum(e[k] for k in ("input", "output", "cache_read", "cache_write_5m", "cache_write_1h"))
            if tok:
                m["tokens"] = tok
                if e["cost"] is not None and not e["cost_unknown"]:
                    m["cost"] = e["cost"]
        a, b = parse_ts(t["created"]), parse_ts(merged_at.get(t["id"]))
        if a is not None and b is not None and b >= a:
            m["wall_s"] = round((b - a).total_seconds())
        out[t["id"]] = m
    return out


def _sort_state(q):
    key, d = q.get("sort"), q.get("dir")
    if key in SORT_KEYS and d in ("asc", "desc"):
        return key, d
    return DEFAULT_SORT


def data(ctx, goal=None, status=None, **params):
    q = getattr(ctx, "query", None) or {}
    goal = goal or q.get("goal")
    status = status or q.get("status")
    org = ctx_org(ctx)
    gorgs = queries.goal_orgs(ctx.db)
    known = {r[0] for r in ctx.db.execute("SELECT id FROM goals")}
    if org:   # the global org filter: only this org's goals (a task's org is its goal's)
        known = {g for g in known if queries.org_of_goal(gorgs, g) == org}
    goal = goal if goal in known else None
    status = status if status in STATUSES else None
    merged, rej = _merged(ctx.db), _rejections(ctx.db)
    everything = queries.tasks(ctx.db)
    if org:
        everything = [t for t in everything if t["goal_id"] in known]
    in_goal = [t for t in everything if not goal or t["goal_id"] == goal]
    in_status = [t for t in everything if not status or t["status"] == status]
    counts = {s: 0 for s in STATUSES}
    for t in in_goal:
        counts[t["status"]] = counts.get(t["status"], 0) + 1
    goal_counts = {}
    for t in in_status:
        goal_counts[t["goal_id"]] = goal_counts.get(t["goal_id"], 0) + 1
    rows = []
    shown = [t for t in in_goal if not status or t["status"] == status]
    met = _metrics(ctx, shown)
    for t in shown:
        label, _ = _ci_for(t, merged)
        rows.append({"id": t["id"], "goal_id": t["goal_id"], "title": t["title"], "status": t["status"], "waits": t["waits"], "pr_url": t["pr_url"],
                     "ci": label, "rejections": rej.get(t["id"], 0), "attempts": t["attempts"],
                     "assignee": t["assignee"], "updated": t["updated"], **met[t["id"]], "org": queries.org_of_goal(gorgs, t["goal_id"])})
    sort = _sort_state(q)
    return {"org": org, "filter": {"goal": goal, "status": status}, "goals": sorted(known), "statuses": list(STATUSES), "tasks": rows,
            "counts": counts, "goal_counts": dict(sorted(goal_counts.items())), "total": len(in_goal),
            "sort": {"key": sort[0], "dir": sort[1]}}


COLUMNS = (
    {"key": "status", "label": "", "cls": "c-st"},
    {"key": "id", "label": "ID", "cls": "c-id mono"},
    {"key": "title", "label": "Task", "cls": "c-title", "sortable": True},
    {"key": "goal", "label": "Goal", "cls": "c-who", "sortable": True, "phone_label": True},
    {"key": "assignee", "label": "Assignee", "cls": "c-who", "sortable": True, "phone_label": True},
    {"key": "pr", "label": "PR", "cls": "c-who", "sortable": True, "phone_label": True},
    {"key": "rejections", "label": "Rejections", "cls": "c-who", "sortable": True, "phone_label": True},
    {"key": "cost", "label": "Cost", "cls": "c-who num", "sortable": True, "phone_label": True},
    {"key": "tokens", "label": "Tokens", "cls": "c-who num", "sortable": True, "phone_label": True},
    {"key": "wall", "label": "Wall time", "cls": "c-who num", "sortable": True, "phone_label": True},
    {"key": "updated", "label": "Updated", "cls": "c-age num", "sortable": True},
)


CI_WORDS = {"green": "CI passed", "green at merge": "CI passed", "failing": "CI failing", "pending": "CI pending"}


def _ci_badge(label):
    return badge(label, _CI_KIND[label]) if label else "-"


def _ci_word(label):
    """CI word next to the PR (note 4 copy map); nothing for an unknown state."""
    return el("span", CI_WORDS[label], class_="muted") if label in CI_WORDS else ""


def _metric_cell(t, field, fmt):
    """Value, "n/a" for a Cursor task (no token counts), "-" when unknown; never 0 for a missing value."""
    if t[field] is None:
        return "n/a" if t["na"] and field != "wall_s" else "-"
    return fmt(t[field])


def _row(t, now):
    pr = el("span", link(t["pr_url"], pr_label(t["pr_url"])), " ", _ci_word(t["ci"])) if t["pr_url"] else ""
    n = t["rejections"]
    rej = f"{n} rejection{'s' if n != 1 else ''}" + (f" · {t['attempts']} tries" if t["attempts"] and t["attempts"] > 1 else "") if n else ""
    title = el("span", el("a", t["title"], href=f"/task/{t['id']}"), " ", task_badge("blocked", t["waits"]) if t["status"] == "blocked" else None)
    goal = el("span", link(f"/goal/{t['goal_id']}", t["goal_id"]), " ", org_badge(t.get("org")), class_="orgrow") if t["goal_id"] else ""
    return [row_status(t["status"]), t["id"], title, goal,
            t["assignee"] or "", pr, rej, _metric_cell(t, "cost", lambda v: f"${v:,.2f}"), _metric_cell(t, "tokens", lambda v: f"{v:,}"),
            _metric_cell(t, "wall_s", dur), el("span", ago(t["updated"], now), title=t["updated"])]


def _sorted(rows, sort):
    key, d = sort
    rows = sorted(rows, key=lambda t: t["id"])
    if key in METRIC_KEYS:   # unknown values go last whichever the direction
        fld = METRIC_KEYS[key]
        known = sorted((t for t in rows if t[fld] is not None), key=SORT_KEYS[key], reverse=d == "desc")
        return known + [t for t in rows if t[fld] is None]
    return sorted(rows, key=SORT_KEYS[key], reverse=d == "desc")


def _chips(d, q):
    f = d["filter"]
    keep = {k: q[k] for k in ("sort", "dir") if k in q}

    def go(**kw):
        return org_href("/tasks", {"status": f["status"], "goal": f["goal"], **keep, **kw}, d.get("org"))
    status_items = [("All", d["total"], go(status=None), not f["status"])]
    status_items += [(LABELS[s], d["counts"].get(s, 0), go(status=s), f["status"] == s) for s in d["statuses"]]
    goal_ids = list(d["goal_counts"])
    shown, more = goal_ids[:GOAL_CHIPS], goal_ids[GOAL_CHIPS:]
    if f["goal"] and f["goal"] in more:   # the selected goal always stays visible
        shown.append(more.pop(more.index(f["goal"])))
    goal_items = [("All", None, go(goal=None), not f["goal"])]
    goal_items += [(g, d["goal_counts"][g], go(goal=g), f["goal"] == g) for g in shown]
    rest = [chips("Goal", goal_items)]
    if more:
        rest.append(disclosure(f"{len(more)} more goals", chips("More goals", [(g, d["goal_counts"][g], go(goal=g), f["goal"] == g) for g in more]),
                               key="tasks-goals"))
    return [filter_bar(chips("Status", status_items), rest, key="tasks-filters", active=1 if f["goal"] else 0)]


def render(ctx, d):
    q = getattr(ctx, "query", None) or {}
    f = d["filter"]
    sort = (d["sort"]["key"], d["sort"]["dir"])

    def sort_href(col):
        nxt = "asc" if not (sort[0] == col and sort[1] == "asc") else "desc"
        if sort[0] != col and col == "updated":
            nxt = "desc"
        return org_href("/tasks", {"status": f["status"], "goal": f["goal"], "sort": col, "dir": nxt}, d.get("org"))
    now = ctx.now
    groups = []
    for s in GROUP_ORDER:
        rows = [t for t in d["tasks"] if t["status"] == s]
        if not rows:
            continue
        groups.append({"key": s, "label": LABELS[s], "count": len(rows), "rows": [_row(t, now) for t in _sorted(rows, sort)],
                       "collapsed": s in COLLAPSED and f["status"] != s})
    note = None
    if f["goal"] or f["status"]:
        note = el("p", f"Filtered: goal={f['goal'] or 'any'}, status={f['status'] or 'any'} · ",
                  el("a", "Clear filters", href=org_href("/tasks", {}, d.get("org"))), class_="muted")
    summary = el("p", f"{len(d['tasks'])} of {d['total']} tasks" if (f["status"]) else f"{d['total']} tasks", class_="summary-line")
    table = data_table(COLUMNS, groups, sort=sort, sort_href=sort_href, key="tasks",
                       empty="No tasks match these filters.")
    if not groups and (f["goal"] or f["status"]):
        table = empty_state("No tasks match these filters.", (org_href("/tasks", {}, d.get("org")), "Clear filters"))
    return el("div", summary, _chips(d, q), note, table)


# ── detail ──

from web.components import section, tile                    # noqa: E402  (the list part above is B3's and keeps its imports)
from web.html import crumbs, glyph, status                  # noqa: E402
from web.views.goals import DetailView, not_found           # noqa: E402

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


def _cost_data(ctx, t):
    """The task's list-price cost, from the TokenIndex snapshot like /tokens (rollup by_task over attribute()). A
    state: cursor (no token counts exist), not_ready (index still warming or none wired: the page never waits),
    none (no tokens attributed), unknown (a model without a price), ok. Only this task's rows go into rollup, so
    the figure equals the by_task bucket without bucketing every session on every page load."""
    if queries.org_of_goal(queries.goal_orgs(ctx.db), t["goal_id"]) == "cursor":
        return {"state": "cursor"}
    snap = ctx.tokens() if getattr(ctx, "tokens", None) else None
    if not snap or not snap.get("ready"):
        return {"state": "not_ready"}
    att = attribute(snap, ctx.db)
    mine = {sid for sid, a in att.items() if a["task"] == t["id"]}
    rows = [r for r in snap.get("rows", []) if r["session"] in mine]
    e = rollup({"rows": rows, "sessions": {}}, att)["by_task"].get(t["id"])
    if e is None:
        return {"state": "none"}
    tokens = sum(e[k] for k in ("input", "output", "cache_read", "cache_write_5m", "cache_write_1h"))
    if tokens == 0:
        return {"state": "none"}
    if e["cost"] is None or e["cost_unknown"]:
        return {"state": "unknown", "cost": e["cost"], "tokens": tokens}
    return {"state": "ok", "cost": e["cost"], "tokens": tokens}


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
        steps.append({"ts": i["ts"], "actor": i["actor"], "kind": i["kind"], "label": i["label"], "text": text, "sha": sha, "tone": tone,
                      "links": sorted(set(_TASK_RE.findall(i.get("detail") or ""))) if i["label"] == "follow-up" else []})
    return {"id": id, "found": True, "title": t["title"], "goal_id": t["goal_id"], "role": t["role"], "status": t["status"], "waits": t["waits"],
            "assignee": t["assignee"], "team_id": t["team_id"], "pr_url": t["pr_url"], "follow_up_of": t["follow_up_of"], "follow_ups": follow,
            "created": t["created"], "merged": bool(merged_ev), "ci": label, "ci_checks": checks,
            "rejections": cyc["rejections"], "retries": cyc["retries"], "attempts": cyc["attempts"], "reopens": cyc["reopens"],
            "total_s": total, "steps": steps, "cost": _cost_data(ctx, t),
            "lifecycle": queries.task_lifecycle(con, id, ctx.now), "usage": _usage_data(ctx, id),
            "incidents": _incident_causes(con, id)}


def _usage_data(ctx, task_id):
    """C2 per-agent usage of the task from the same TokenIndex snapshot; None while the index is warming or not wired."""
    snap = ctx.tokens() if getattr(ctx, "tokens", None) else None
    if not snap or not snap.get("ready"):
        return None
    return task_usage(snap, attribute(snap, ctx.db), ctx.db, task_id, ctx.now)


def _incident_causes(con, task_id):
    """cause -> count of this task's rejection_incidents rows (structured cause only, never free text)."""
    out = {}
    for r in con.execute("SELECT cause FROM rejection_incidents WHERE task_id=? ORDER BY id", (task_id,)):
        out[r["cause"]] = out.get(r["cause"], 0) + 1
    return out


_ROLE = {"qa": "QA"}
_OUTCOME = {"pass": "passed", "fail": "failed", "merge": "merged", "reject": "rejected"}
_CI_GLYPH = {"green": "done", "green at merge": "done", "failing": "blocked", "pending": "queued"}
_TIMES = {1: "once"}


def _role(r):
    return _ROLE.get(r, str(r).capitalize())


def _is_reject(s):
    return s["label"] == "judge-reject" or (s["kind"] == "verdict" and s["label"].split(" ", 1)[1] in ("fail", "reject"))


def _verb(s):
    """Human verb of a timeline step (note 4 copy): "Reviewer passed", "Judge merged", "Coder started", ..."""
    lab, kind, text = s["label"], s["kind"], s["text"]
    if kind == "verdict":
        role, verdict = lab.split(" ", 1)
        return f"{_role(role)} {_OUTCOME.get(verdict, verdict)}"
    if lab == "judge-reject":
        return "Judge rejected"
    if kind == "agent":
        what, role = lab.split(" ", 1)
        return f"{_role(role)} {'started' if what == 'spawn' else 'finished'}"
    if lab == "merged":
        return "Merged"
    return text[:1].upper() + text[1:]


def _line_key(s):
    """A judge verdict row and the judge-reject event of one rejection share a second and collapse to one line (the event
    actor and the verdict author can differ). Every other line is kept."""
    if _is_reject(s) and ("judge" in s["label"]):
        return ("judge-reject", (s["ts"] or "")[:19])
    return None


def _attempt_blocks(steps):
    """Group timeline steps by attempt: an attempt ends at a rejection, a coder spawned again or a reopen starts the next.
    Duplicate lines (same second, see _line_key) merge. Returns [[step, ...], ...] oldest first."""
    blocks, cur, seen, rejected = [], [], set(), False
    for s in steps:
        key = _line_key(s)
        if key is not None and key in seen:
            continue
        spawn = s["label"] == "spawn coder"
        late = s["kind"] == "agent" and s["label"].startswith("end ")   # the failed attempt's own coder ending
        if cur and ((rejected and not late and not _is_reject(s)) or s["label"] == "reopen" or (spawn and any(c["label"] == "spawn coder" for c in cur))):
            blocks.append(cur)
            cur, rejected = [], False
        if key is not None:
            seen.add(key)
        cur.append(s)
        rejected = rejected or _is_reject(s)
    if cur:
        blocks.append(cur)
    return blocks


def _verdict_lines(steps):
    """(role, outcome word, ts) per verdict, duplicates merged; a merged event with no judge merge verdict counts as one."""
    out, seen = [], set()
    for s in steps:
        if s["kind"] == "verdict":
            role, verdict = s["label"].split(" ", 1)
            word = _OUTCOME.get(verdict, verdict)
        elif s["label"] == "judge-reject":
            role, word = "judge", "rejected"
        elif s["label"] == "merged":
            role, word = "judge", "merged"
        else:
            continue
        key = _line_key(s) if word != "merged" else ("merged", role)
        if (key is not None and key in seen) or (word == "merged" and any(r == "judge" and w == "merged" for r, w, _ in out)):
            continue
        if key is not None:
            seen.add(key)
        out.append((role, word, s["ts"]))
    return out


def _runs(words):
    runs = []
    for w in words:
        if runs and runs[-1][0] == w:
            runs[-1][1] += 1
        else:
            runs.append([w, 1])
    return runs


def _phrase(words):
    parts = []
    for n, (w, c) in enumerate(_runs(words)):
        parts.append(w + (f" {_TIMES.get(c, f'{c} times')}" if c > 1 or (len(_runs(words)) > 1 and n == 0) else ""))
    return ", then ".join(parts)


def _verdict_summary(d):
    """C12 one line: "Reviewer passed · QA failed once, then passed · CI passed · Judge merged" (kind and outcome only)."""
    lines = _verdict_lines(d["steps"])
    by_role = {}
    for role, word, _ in lines:
        by_role.setdefault(role, []).append(word)
    order = [r for r in ("reviewer", "qa") if r in by_role] + [r for r in by_role if r not in ("reviewer", "qa", "judge")]
    parts = [f"{_role(r)} {_phrase(by_role[r])}" for r in order]
    if d["ci"] in CI_WORDS:
        parts.append(CI_WORDS[d["ci"]])
    if "judge" in by_role:
        parts.append(f"Judge {_phrase(by_role['judge'])}")
    if not parts:
        return None
    detail = el("ul", [el("li", f"{_role(r)} {w} · ", ts(t)) for r, w, t in lines], class_="lrow-list")
    return disclosure(" · ".join(parts), detail, key="task-verdicts") if lines else el("p", " · ".join(parts), class_="summary-line")


def _attempt(n, block, last, now):
    items, prev = [], None
    for s in block:
        at, was = parse_ts(s["ts"]), prev
        prev = at or prev
        step = f"+{dur((at - was).total_seconds())}" if at and was and at >= was else None
        extra = [el("span", f" @{s['sha']}", class_="mono")] if s["sha"] else []
        extra += [el("span", " ", link(f"/task/{x}", x)) for x in s["links"]]
        items.append(el("li", ts(s["ts"]), " ", badge(_verb(s), s["tone"]), " ", el("span", s["actor"] or "", class_="muted"), *extra,
                        " ", el("span", step, class_="muted") if step else None, class_="timeline-item",
                        data_actor=s["actor"] or "", data_kind=s["label"], data_ts=s["ts"]))
    first, end = parse_ts(block[0]["ts"]), parse_ts(block[-1]["ts"])
    span = dur((end - first).total_seconds()) if first and end and end > first else None
    outcome = "rejected" if _is_reject(block[-1]) else "merged" if block[-1]["label"] == "merged" else "in progress" if last else "ended"
    summ = el("summary", el("span", f"Attempt {n}"), el("span", " · ".join(x for x in (outcome, span) if x), class_="disc-n"))
    return el("details", summ, el("ol", items, class_="timeline"), class_="disc attempt", data_key=f"task-attempt-{n}", open=last or None)


def _fmt_ci(label):
    return el("span", glyph(_CI_GLYPH[label]), CI_WORDS.get(label, label), class_="st") if label in _CI_GLYPH else None


def _meta(d):
    bits = [status(d["status"]), link(f"/goal/{d['goal_id']}", d["goal_id"]) if d["goal_id"] else None, d["team_id"],
            d["assignee"] or d["role"], link(d["pr_url"], pr_label(d["pr_url"])) if d["pr_url"] else None, _fmt_ci(d["ci"])]
    out = []
    for b in (b for b in bits if b):
        out += [" · ", b] if out else [b]
    return el("p", *out, class_="metarow")


def _cost_line(c):
    st = c["state"]
    if st == "cursor":
        return "Cost: not available. The Cursor CLI reports no token counts."
    if st == "not_ready":
        return "Cost: not ready (the token index is still warming up)."
    if st == "none":
        return "Cost: none recorded (no tokens attributed to this task)."
    note = f" · estimated at list prices as of {PRICES_AS_OF} (API-equivalent, not a bill)"
    if st == "unknown":
        part = f"at least ${c['cost']:.2f}, " if c["cost"] else ""
        return f"Cost: unknown ({part}some tokens are on a model without a price)" + note
    return f"Cost: ${c['cost']:.2f}" + note


_WAIT_LABEL = {"ci": "waiting: CI", "blocked": "waiting: blocked"}
_NA = "not available"


def _share(sec, total):
    return f"{sec / total:.0%}" if sec is not None and total else "unknown"


def _lifecycle_section(d):
    lc = d.get("lifecycle")
    if not lc:
        return None
    total = lc["total"]["seconds"]
    rows = [[st["stage"], (dur(st["seconds"]), "num"), (_share(st["seconds"], total), "num")] for st in lc["stages"]]
    for w in lc["waits"]:
        kind = _WAIT_LABEL.get(w["kind"], w["kind"]) + (f" ({w['waits']})" if w["kind"] == "blocked" else "")
        rows.append([el("span", kind, " ", el("span", "inside a stage", class_="muted")), (dur(w["seconds"]), "num"),
                     (_share(w["seconds"], total), "num")])
    if lc["gaps_s"] is not None:
        rows.append(["other (rework, idle)", (dur(lc["gaps_s"]), "num"), (_share(lc["gaps_s"], total), "num")])
    rows.append([el("strong", "total" + (" (open)" if lc["total"]["open"] else "")), (dur(total), "num"), ("100%" if total else "unknown", "num")])
    return section("Lifecycle", table(["Stage", "Duration", "Share"], rows))


def _n(v):
    return _NA if v is None else f"{v:,}"


def _money(v):
    return "unknown" if v is None else f"${v:,.2f}"


def _usage_cells(r):
    if r.get("state") == "not_available":
        return [(_NA, "num")] * 5
    return [(_n(r["input"]), "num"), (_n(r["output"]), "num"), (_n(r["cache_read"]), "num"), (_n(r["cache_write"]), "num"),
            (_money(r["cost"]), "num")]


def _rework_line(d):
    u, inc = d.get("usage"), d.get("incidents") or {}
    t = (u or {}).get("totals")
    if t and t.get("rework_cost") is not None and t.get("rework_share") is not None:
        cost = f"{_money(t['rework_cost'])} ({t['rework_share']:.0%} of cost)"
    else:
        cost = "unknown"
    causes = [f"{c} ×{n}" for c, n in sorted(inc.items())]
    missing = (d.get("rejections") or 0) - sum(inc.values())
    if missing > 0:
        causes.append(f"cause not recorded ×{missing}")
    if not causes and not (d.get("rejections") or 0):
        return "Rework: none (no rejections)"
    return f"Rework: {cost} · causes: {', '.join(causes)}"


def _usage_section(d):
    u = d.get("usage")
    if u is None:
        return section("Cost and tokens", el("p", "Not ready (the token index is still warming up).", class_="muted"),
                       el("p", _rework_line(d), class_="muted"))
    head = el("p", f"Estimated at list prices as of {PRICES_AS_OF} (API-equivalent, not a bill)", class_="muted")
    if not u["rows"]:
        return section("Cost and tokens", head, empty_state("No tokens attributed to this task."), el("p", _rework_line(d), class_="muted"))
    rows = [[r["role"] or "-", r["agent"], (str(r["sessions"]), "num"), *_usage_cells(r)] for r in u["rows"]]
    t = u["totals"]
    rows.append([el("strong", "Total"), "", (str(t["sessions"]), "num"), *_usage_cells(t)])
    note = el("p", "Totals cover the Claude agents only; Cursor reports no token counts.", class_="muted") if t["cursor_rows"] else None
    return section("Cost and tokens", head, table(["Role", "Agent", "Sessions", "Input", "Output", "Cache read", "Cache write", "Cost"], rows),
                   note, el("p", _rework_line(d), class_="muted"))


def _detail_render(ctx, d):
    crumb = crumbs(("/tasks", "Tasks"), (f"/task/{d.get('id')}", d.get("id") or "?"))
    if not d.get("found"):
        return not_found("task", d.get("id"), "/tasks", "Back to Tasks", crumb)
    tiles = el("div", tile("Total time", dur(d["total_s"])), tile("Rejections", d["rejections"]), tile("Attempts", d["attempts"] or 0),
               class_="tiles")
    blocks = _attempt_blocks(d["steps"])
    cycle = ([_attempt(n, b, n == len(blocks), ctx.now) for n, b in enumerate(blocks, 1)] if blocks else [empty_state("No events yet.")])
    parts = [el("div", crumb, _meta(d), el("p", _cost_line(d["cost"]), class_="muted"), class_="tblock tblock--wide"), section("Verdicts", _verdict_summary(d) or empty_state("No verdicts yet.")), tiles,
             _lifecycle_section(d), _usage_section(d), section("PR cycle", *cycle)]
    fu = [el("span", link(f"/task/{f['id']}", f["id"]), " ", f["title"], " ", task_badge(f["status"], f["waits"]), " ") for f in d["follow_ups"]]
    if d["follow_up_of"]:
        fu.insert(0, el("span", "follow-up of ", link(f"/task/{d['follow_up_of']}", d["follow_up_of"]), " "))
    if fu:
        parts.append(section("Follow-ups", el("p", *fu)))
    return el("div", *parts)


detail = DetailView("Task", _detail_data, _detail_render)
