"""Tasks list and task detail with the full PR cycle. Never renders specs, acceptance text, notes or verdict summaries."""
import re
from types import SimpleNamespace

from web import ci, queries
from web.queries import parse_ts
from web.components import data_table, disclosure, empty_state, ago, chips, href, pr_label, row_status, GROUP_ORDER, COLLAPSED
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
DEFAULT_SORT = ("updated", "desc")
GOAL_CHIPS = 6


def _sort_state(q):
    key, d = q.get("sort"), q.get("dir")
    if key in SORT_KEYS and d in ("asc", "desc"):
        return key, d
    return DEFAULT_SORT


def data(ctx, goal=None, status=None, **params):
    q = getattr(ctx, "query", None) or {}
    goal = goal or q.get("goal")
    status = status or q.get("status")
    known = {r[0] for r in ctx.db.execute("SELECT id FROM goals")}
    goal = goal if goal in known else None
    status = status if status in STATUSES else None
    merged, rej = _merged(ctx.db), _rejections(ctx.db)
    everything = queries.tasks(ctx.db)
    in_goal = [t for t in everything if not goal or t["goal_id"] == goal]
    in_status = [t for t in everything if not status or t["status"] == status]
    counts = {s: 0 for s in STATUSES}
    for t in in_goal:
        counts[t["status"]] = counts.get(t["status"], 0) + 1
    goal_counts = {}
    for t in in_status:
        goal_counts[t["goal_id"]] = goal_counts.get(t["goal_id"], 0) + 1
    rows = []
    for t in (t for t in in_goal if not status or t["status"] == status):
        label, _ = _ci_for(t, merged)
        rows.append({"id": t["id"], "goal_id": t["goal_id"], "title": t["title"], "status": t["status"], "waits": t["waits"], "pr_url": t["pr_url"],
                     "ci": label, "rejections": rej.get(t["id"], 0), "attempts": t["attempts"],
                     "assignee": t["assignee"], "updated": t["updated"]})
    sort = _sort_state(q)
    return {"filter": {"goal": goal, "status": status}, "goals": sorted(known), "statuses": list(STATUSES), "tasks": rows,
            "counts": counts, "goal_counts": dict(sorted(goal_counts.items())), "total": len(in_goal),
            "sort": {"key": sort[0], "dir": sort[1]}}


COLUMNS = (
    {"key": "status", "label": "", "cls": "c-st"},
    {"key": "id", "label": "ID", "cls": "c-id mono"},
    {"key": "title", "label": "Task", "cls": "c-title", "sortable": True},
    {"key": "goal", "label": "Goal", "cls": "c-who", "sortable": True},
    {"key": "assignee", "label": "Assignee", "cls": "c-who", "sortable": True},
    {"key": "pr", "label": "PR", "cls": "c-who", "sortable": True},
    {"key": "rejections", "label": "Rejections", "cls": "c-who", "sortable": True},
    {"key": "updated", "label": "Updated", "cls": "c-age num", "sortable": True},
)


CI_WORDS = {"green": "CI passed", "green at merge": "CI passed", "failing": "CI failing", "pending": "CI pending"}


def _ci_badge(label):
    return badge(label, _CI_KIND[label]) if label else "-"


def _ci_word(label):
    """CI word next to the PR (note 4 copy map); nothing for an unknown state."""
    return el("span", CI_WORDS[label], class_="muted") if label in CI_WORDS else ""


def _row(t, now):
    pr = el("span", link(t["pr_url"], pr_label(t["pr_url"])), " ", _ci_word(t["ci"])) if t["pr_url"] else ""
    n = t["rejections"]
    rej = f"{n} rejection{'s' if n != 1 else ''}" + (f" · {t['attempts']} tries" if t["attempts"] and t["attempts"] > 1 else "") if n else ""
    title = el("span", el("a", t["title"], href=f"/task/{t['id']}"), " ", task_badge("blocked", t["waits"]) if t["status"] == "blocked" else None)
    return [row_status(t["status"]), t["id"], title, link(f"/goal/{t['goal_id']}", t["goal_id"]) if t["goal_id"] else "",
            t["assignee"] or "", pr, rej, el("span", ago(t["updated"], now), title=t["updated"])]


def _sorted(rows, sort):
    key, d = sort
    rows = sorted(rows, key=lambda t: t["id"])
    return sorted(rows, key=SORT_KEYS[key], reverse=d == "desc")


def _chips(d, q):
    f = d["filter"]
    keep = {k: q[k] for k in ("sort", "dir") if k in q}

    def go(**kw):
        return href("/tasks", {"status": f["status"], "goal": f["goal"], **keep, **kw})
    status_items = [("All", d["total"], go(status=None), not f["status"])]
    status_items += [(LABELS[s], d["counts"].get(s, 0), go(status=s), f["status"] == s) for s in d["statuses"]]
    goal_ids = list(d["goal_counts"])
    shown, more = goal_ids[:GOAL_CHIPS], goal_ids[GOAL_CHIPS:]
    if f["goal"] and f["goal"] in more:   # the selected goal always stays visible
        shown.append(more.pop(more.index(f["goal"])))
    goal_items = [("All", None, go(goal=None), not f["goal"])]
    goal_items += [(g, d["goal_counts"][g], go(goal=g), f["goal"] == g) for g in shown]
    parts = [chips("Status", status_items), chips("Goal", goal_items)]
    if more:
        parts.append(disclosure(f"{len(more)} more goals", chips("More goals", [(g, d["goal_counts"][g], go(goal=g), f["goal"] == g) for g in more]),
                                key="tasks-goals"))
    return parts


def render(ctx, d):
    q = getattr(ctx, "query", None) or {}
    f = d["filter"]
    sort = (d["sort"]["key"], d["sort"]["dir"])

    def sort_href(col):
        nxt = "asc" if not (sort[0] == col and sort[1] == "asc") else "desc"
        if sort[0] != col and col == "updated":
            nxt = "desc"
        return href("/tasks", {"status": f["status"], "goal": f["goal"], "sort": col, "dir": nxt})
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
        note = el("p", f"Filtered: goal={f['goal'] or 'any'}, status={f['status'] or 'any'} · ", el("a", "Clear filters", href="/tasks"), class_="muted")
    summary = el("p", f"{len(d['tasks'])} of {d['total']} tasks" if (f["status"]) else f"{d['total']} tasks", class_="summary-line")
    table = data_table(COLUMNS, groups, sort=sort, sort_href=sort_href, key="tasks",
                       empty="No tasks match these filters.")
    if not groups and (f["goal"] or f["status"]):
        table = empty_state("No tasks match these filters.", ("/tasks", "Clear filters"))
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
            "total_s": total, "steps": steps}


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


def _detail_render(ctx, d):
    crumb = crumbs(("/tasks", "Tasks"), (f"/task/{d.get('id')}", d.get("id") or "?"))
    if not d.get("found"):
        return not_found("task", d.get("id"), "/tasks", "Back to Tasks", crumb)
    tiles = el("div", tile("Total time", dur(d["total_s"])), tile("Rejections", d["rejections"]), tile("Attempts", d["attempts"] or 0),
               class_="tiles")
    blocks = _attempt_blocks(d["steps"])
    cycle = ([_attempt(n, b, n == len(blocks), ctx.now) for n, b in enumerate(blocks, 1)] if blocks else [empty_state("No events yet.")])
    parts = [crumb, _meta(d), section("Verdicts", _verdict_summary(d) or empty_state("No verdicts yet.")), tiles,
             section("PR cycle", *cycle)]
    fu = [el("span", link(f"/task/{f['id']}", f["id"]), " ", f["title"], " ", task_badge(f["status"], f["waits"]), " ") for f in d["follow_ups"]]
    if d["follow_up_of"]:
        fu.insert(0, el("span", "follow-up of ", link(f"/task/{d['follow_up_of']}", d["follow_up_of"]), " "))
    if fu:
        parts.append(section("Follow-ups", el("p", *fu)))
    return el("div", *parts)


detail = DetailView("Task", _detail_data, _detail_render)
