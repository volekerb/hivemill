"""Read-only queries for the factory web UI.

Pure functions f(con, ...) -> list[dict] | dict on a connection from web.db.connect.
No HTML, no writes. Results are plain JSON-serialisable dicts.

Never selected: agents.spawn_args, messages.body, plans.body, tasks.spec, tasks.acceptance.
goals.intent is returned truncated to 600 characters (goal_detail only).
Timestamps are the DB's ISO strings.
"""
import datetime as dt
import json
import re
import sqlite3

from web.db import has_column

INTENT_MAX = 600
SUBJECT_MAX = 120
DETAIL_MAX = 300
ENDED_AGENT = ("done", "exited", "lost", "stopped")
PR_CYCLE_ACTIONS = ("add", "update", "note", "reopen", "follow-up", "judge-reject",
                    "judge-blocked", "merge-failed", "merged", "merge-post-error")
_TAG_RE = re.compile(r"\[(SC\d+(?:\s*,\s*SC\d+)*)\]")
_NUM_RE = re.compile(r"^\s*(\d+)[.)]\s*(.*)$")
_CHUNK = 400


# ───────────────────────────── helpers ─────────────────────────────

def parse_ts(s):
    """ISO string -> aware datetime (naive = local time); None for None/garbage."""
    if not s or not isinstance(s, str):
        return None
    try:
        d = dt.datetime.fromisoformat(s.strip())
    except ValueError:
        return None
    return d if d.tzinfo else d.astimezone()


def _epoch(s):
    d = parse_ts(s)
    return d.timestamp() if d else None


def _t(s, n):
    """Truncate to n characters (with an ellipsis); None stays None."""
    if s is None:
        return None
    s = str(s)
    return s if len(s) <= n else s[:n - 1] + "…"


def _dicts(rows):
    return [dict(r) for r in rows]


def _now(now):
    return (now or dt.datetime.now().astimezone())


def _in(con, sql, ids, *pre):
    """Run `sql` containing one `{in}` placeholder for each chunk of ids; return all rows."""
    ids = list(ids)
    out = []
    for i in range(0, len(ids), _CHUNK):
        part = ids[i:i + _CHUNK]
        out += con.execute(sql.format(**{"in": ",".join("?" * len(part))}), (*pre, *part)).fetchall()
    return out


def _depends(raw):
    try:
        v = json.loads(raw) if raw else []
    except (TypeError, ValueError):
        return []
    return [str(x) for x in v] if isinstance(v, list) else []


def _tags(title):
    """Set of criterion numbers from [SCn] / [SC1,SC3] tags in a task title."""
    out = set()
    for m in _TAG_RE.finditer(title or ""):
        out |= {int(n) for n in re.findall(r"\d+", m.group(1))}
    return out


def _strip_tags(title):
    return re.sub(r"\s+", " ", _TAG_RE.sub(" ", title or "")).strip()


def parse_criteria(text):
    """Split success_criteria into [(n, text)]: items start on lines like '1.' or '2)'; fallback one per line."""
    lines = [l.rstrip() for l in (text or "").splitlines()]
    items = []
    for l in lines:
        m = _NUM_RE.match(l)
        if m:
            items.append([int(m.group(1)), m.group(2).strip()])
        elif items and l.strip():
            items[-1][1] = (items[-1][1] + " " + l.strip()).strip()
    if items:
        return [(n, t) for n, t in items]
    return [(i, l.strip()) for i, l in enumerate((l for l in lines if l.strip()), 1)]


# ───────────────────────────── goals ─────────────────────────────

def goals(con):
    """One row per goal with task counts (all tasks, cancelled included) and team ids."""
    counts = {r["goal_id"]: r for r in con.execute(
        "SELECT goal_id, COUNT(*) total, SUM(status='done') done, SUM(status='blocked') blocked, "
        + ("SUM(status='blocked' AND waits='owner') blocked_owner, " if has_column(con, "tasks", "waits")
           else "0 blocked_owner, ")
        + "SUM(status='in_review' AND pr_url IS NOT NULL AND pr_url<>'') prs FROM tasks GROUP BY goal_id")}
    teams = {}
    for r in con.execute("SELECT id, goal_id FROM teams ORDER BY id"):
        teams.setdefault(r["goal_id"], []).append(r["id"])
    out = []
    for g in con.execute("SELECT id,title,status,priority,project_id,created,updated FROM goals ORDER BY priority, id"):
        c = counts.get(g["id"])
        d = dict(g)
        d.update(tasks_total=c["total"] if c else 0, tasks_done=(c["done"] or 0) if c else 0,
                 tasks_blocked=(c["blocked"] or 0) if c else 0,
                 tasks_blocked_owner=(c["blocked_owner"] or 0) if c else 0, open_prs=(c["prs"] or 0) if c else 0,
                 teams=teams.get(g["id"], []))
        out.append(d)
    return out


def goal_detail(con, gid):
    """goals() row + intent (truncated) + criteria [{n, text, tasks, state}]; None for an unknown goal.

    state: met = every tagged (non-cancelled) task is done and at least one has a `merged` event;
    in_progress = tagged tasks exist but not met; untracked = no task carries the [SCn] tag.
    A goal with status done shows every criterion met.
    """
    base = next((g for g in goals(con) if g["id"] == gid), None)
    if base is None:
        return None
    row = con.execute("SELECT intent, success_criteria FROM goals WHERE id=?", (gid,)).fetchone()
    tasks = con.execute("SELECT id,title,status FROM tasks WHERE goal_id=? ORDER BY id", (gid,)).fetchall()
    merged = {r[0] for r in con.execute(
        "SELECT DISTINCT e.entity_id FROM events e JOIN tasks t ON t.id=e.entity_id "
        "WHERE e.entity='task' AND e.action='merged' AND t.goal_id=?", (gid,))}
    base["intent"] = _t(row["intent"], INTENT_MAX)
    crit = []
    for n, text in parse_criteria(row["success_criteria"]):
        tagged = [t for t in tasks if n in _tags(t["title"])]
        live = [t for t in tagged if t["status"] != "cancelled"]
        if base["status"] == "done":
            state = "met"
        elif not live:
            state = "untracked"
        elif all(t["status"] == "done" for t in live) and any(t["id"] in merged for t in live):
            state = "met"
        else:
            state = "in_progress"
        crit.append({"n": n, "text": text, "tasks": [t["id"] for t in tagged], "state": state})
    base["criteria"] = crit
    return base


def _blocked_tasks(con, waits):
    """status='blocked' tasks waiting on `waits` ('owner' or 'internal'). A DB from before the waits column
    has no owner-blocked tasks: every blocked task there is internal."""
    if not has_column(con, "tasks", "waits"):
        return [] if waits == "owner" else _dicts(con.execute(
            "SELECT id,goal_id,title,status,updated FROM tasks WHERE status='blocked' ORDER BY updated DESC"))
    return _dicts(con.execute(
        "SELECT id,goal_id,title,status,updated FROM tasks WHERE status='blocked' AND waits=? ORDER BY updated DESC", (waits,)))


def internal_blocked(con):
    """Blocked tasks that wait on the factory itself (queue, freeze, dependency), not on the owner."""
    return _blocked_tasks(con, "internal")


def owner_pending(con):
    """Things waiting on the owner/boss: owner-blocked tasks, unread escalations/requests, proposed or blocked
    goals, auto-merge-off tasks. Internal-blocked tasks are not here (see internal_blocked). Never returns message bodies."""
    gl = _dicts(con.execute(
        "SELECT id,title,status,priority,updated FROM goals WHERE status IN ('proposed','blocked') ORDER BY priority, id"))
    tl = _blocked_tasks(con, "owner")
    latest = {}
    for r in con.execute("SELECT entity_id, ts, detail FROM events WHERE entity='task' AND action='judge-blocked' ORDER BY id"):
        latest[r["entity_id"]] = r
    status = {r["id"]: r for r in con.execute("SELECT id, goal_id, title, status FROM tasks")}
    am0 = []
    for tid, r in latest.items():
        t = status.get(tid)
        if "auto_merge=0" in (r["detail"] or "") and t and t["status"] != "done":
            am0.append({"task_id": tid, "goal_id": t["goal_id"], "title": t["title"], "status": t["status"], "ts": r["ts"]})
    msgs = _dicts(con.execute(
        "SELECT id, sender, kind, substr(subject,1,?) subject, created FROM messages "
        "WHERE read_at IS NULL AND recipient IN ('owner','boss') AND kind IN ('escalation','request') ORDER BY id", (SUBJECT_MAX,)))
    return {"goals": gl, "tasks_blocked": tl, "judge_blocked_auto_merge": am0, "escalations": msgs}


# ───────────────────────────── teams / agents / tasks ─────────────────────────────

def teams(con):
    return _dicts(con.execute("SELECT id, goal_id, scope, lead_agent, status, created FROM teams ORDER BY id"))


def agents(con, include_ended=False, now=None):
    """Agents with age_s (live/queued: since started/queued_at until now; ended: started -> ended)."""
    want = ["name", "role", "goal_id", "team_id", "task_id", "status", "model", "pane_id", "started", "ended",
            "queued_at", "session_id", "stop_requested_at"]
    sel = ", ".join(c if has_column(con, "agents", c) else f"NULL AS {c}" for c in want)
    where = "" if include_ended else "WHERE status NOT IN (%s)" % ",".join("?" * len(ENDED_AGENT))
    rows = con.execute(f"SELECT {sel} FROM agents {where} ORDER BY name", () if include_ended else ENDED_AGENT)
    now_e = _now(now).timestamp()
    out = []
    for r in rows:
        d = dict(r)
        start = _epoch(d["started"]) or _epoch(d["queued_at"])
        end = _epoch(d["ended"]) if d["status"] in ENDED_AGENT else now_e
        d["age_s"] = round(end - start) if start is not None and end is not None else None
        out.append(d)
    return out


UTIL_FRESH_S = 300


def utilization_sample(con, now=None):
    """Latest util_samples row as {line, under}, or None when there is no row, the newest is older than 5 min or the
    table is missing (an old DB). Reads the table only: the tick writes it (docs/utilization.md)."""
    try:
        r = con.execute("SELECT * FROM util_samples ORDER BY id DESC LIMIT 1").fetchone()
    except sqlite3.OperationalError:
        return None
    t = parse_ts(r["ts"]) if r else None
    if t is None or not 0 <= (_now(now) - t).total_seconds() <= UTIL_FRESH_S:
        return None
    try:
        p1 = json.loads(r["p1_starved"])
    except ValueError:
        p1 = []
    line = (f"live {r['live']}: workers {r['workers']} / leads {r['leads']} / judges {r['judges']} / HQ {r['hq']} / "
            f"queued {r['queued']}; P1 goals without workers: {', '.join(p1) or 'none'}")
    return {"line": line, "under": r["workers"] < r["target"] and bool(r["demand"])}


def tasks(con, goal=None, status=None):
    sel = ("id, goal_id, team_id, title, role, status, assignee, depends_on, pr_url, attempts, "
           + ("follow_up_of" if has_column(con, "tasks", "follow_up_of") else "NULL AS follow_up_of")
           + (", waits" if has_column(con, "tasks", "waits") else ", 'internal' AS waits")
           + ", created, updated")
    cond, args = [], []
    if goal:
        cond.append("goal_id=?"); args.append(goal)
    if status:
        cond.append("status=?"); args.append(status)
    rows = con.execute(f"SELECT {sel} FROM tasks {'WHERE ' + ' AND '.join(cond) if cond else ''} ORDER BY id", args)
    out = []
    for r in rows:
        d = dict(r)
        d["depends_on"] = _depends(d["depends_on"])
        out.append(d)
    return out


# ───────────────────────────── PR cycle ─────────────────────────────

def pr_cycle(con, tid):
    """Timeline for one task: events + verdicts + agent spawns/ends, ordered by time.

    Item: {ts, kind (event|verdict|agent), actor, label, detail}; verdicts also carry `head`.
    rejections = verdict rows fail/reject (reviewer, qa, judge) + judge-reject events that have no
    judge reject verdict row (older data), never both for the same rejection.
    attempts = tasks.attempts; retries = max(attempts-1, 0); reopens = `reopen` events.
    """
    items = []
    ev = {a: 0 for a in ("judge-reject", "reopen")}
    ph = ",".join("?" * len(PR_CYCLE_ACTIONS))
    for r in con.execute(f"SELECT id,ts,actor,action,detail FROM events WHERE entity='task' AND entity_id=? "
                         f"AND action IN ({ph}) ORDER BY id", (tid, *PR_CYCLE_ACTIONS)):
        ev[r["action"]] = ev.get(r["action"], 0) + 1
        items.append({"ts": r["ts"], "kind": "event", "actor": r["actor"], "label": r["action"],
                      "detail": _t(r["detail"], DETAIL_MAX), "_o": r["id"]})
    rej_v = judge_rej_v = 0
    for r in con.execute("SELECT id,role,verdict,head_sha,created,author,summary FROM verdicts WHERE task_id=? ORDER BY id", (tid,)):
        if r["verdict"] in ("fail", "reject"):
            rej_v += 1
            judge_rej_v += r["role"] == "judge"
        items.append({"ts": r["created"], "kind": "verdict", "actor": r["author"], "label": f"{r['role']} {r['verdict']}",
                      "head": (r["head_sha"] or "")[:8] or None, "detail": _t(r["summary"], DETAIL_MAX), "_o": r["id"]})
    for r in con.execute("SELECT name, role, started, ended, status FROM agents WHERE task_id=?", (tid,)):
        if r["started"]:
            items.append({"ts": r["started"], "kind": "agent", "actor": r["name"], "label": f"spawn {r['role']}",
                          "detail": None, "_o": 0})
        if r["ended"]:
            items.append({"ts": r["ended"], "kind": "agent", "actor": r["name"], "label": f"end {r['role']}",
                          "detail": r["status"], "_o": 0})
    items.sort(key=lambda i: (_epoch(i["ts"]) if _epoch(i["ts"]) is not None else float("inf"), i["kind"], i["_o"]))
    for i in items:
        del i["_o"]
    t = con.execute("SELECT attempts FROM tasks WHERE id=?", (tid,)).fetchone()
    attempts = t["attempts"] if t else 0
    return {"task_id": tid, "items": items,
            "rejections": rej_v + max(0, ev["judge-reject"] - judge_rej_v),
            "attempts": attempts, "retries": max(attempts - 1, 0), "reopens": ev["reopen"]}


# ───────────────────────────── events / messages ─────────────────────────────

def events(con, since=None, actions=None, entity=None, limit=500):
    """Newest first. `since` is an ISO timestamp (compared as an instant, not as text)."""
    cond, args = [], []
    if actions:
        cond.append("action IN (%s)" % ",".join("?" * len(actions))); args += list(actions)
    if entity:
        cond.append("entity=?"); args.append(entity)
    rows = con.execute(f"SELECT id,ts,actor,entity,entity_id,action,detail FROM events "
                       f"{'WHERE ' + ' AND '.join(cond) if cond else ''} ORDER BY id DESC", args)
    cut = _epoch(since) if since else None
    out = []
    for r in rows:
        if cut is not None:
            e = _epoch(r["ts"])
            if e is not None and e < cut:
                continue
        d = dict(r)
        d["detail"] = _t(d["detail"], DETAIL_MAX)
        out.append(d)
        if len(out) >= limit:
            break
    return out


def messages_meta(con, kinds=("decision", "escalation", "report", "alert"), limit=200):
    """Message metadata only (no body); subject truncated to 120 chars. Newest first."""
    kinds = list(kinds)
    rows = con.execute(
        "SELECT id, sender, recipient, kind, substr(subject,1,?) subject, goal_id, task_id, created, "
        "read_at IS NOT NULL AS read FROM messages WHERE kind IN (%s) ORDER BY id DESC LIMIT ?" % ",".join("?" * len(kinds)),
        (SUBJECT_MAX, *kinds, limit))
    out = _dicts(rows)
    for d in out:
        d["read"] = bool(d["read"])
    return out


# ───────────────────────────── throughput ─────────────────────────────

def _union_len(spans):
    spans = sorted(spans)
    total, cur_s, cur_e = 0.0, None, None
    for s, e in spans:
        if cur_e is None or s > cur_e:
            if cur_e is not None:
                total += cur_e - cur_s
            cur_s, cur_e = s, e
        else:
            cur_e = max(cur_e, e)
    if cur_e is not None:
        total += cur_e - cur_s
    return total


def _stages(merged_e, evs, verdicts, spawns):
    """Stage durations in seconds for one merged task; None where an input is missing (never a guess)."""
    def d(a, b):
        return round(b - a) if a is not None and b is not None and b >= a else None
    first_spawn = min((s for _, s in spawns), default=None)
    coder_spawn = min((s for r, s in spawns if r == "coder"), default=None)
    ups = [(_epoch(r["ts"]), r["detail"] or "") for r in evs if r["action"] == "update"]
    ups = [(t, x) for t, x in ups if t is not None]
    pr_set = min((t for t, x in ups if "pr=" in x or "status=in_review" in x), default=None)
    reviews = sorted(t for t, x in ups if "status=in_review" in x)
    passes = [_epoch(v["created"]) for v in verdicts if v["role"] in ("reviewer", "qa") and v["verdict"] == "pass"]
    passes = [p for p in passes if p is not None]
    last_pass = max(passes, default=None)
    in_review = max((t for t in reviews if last_pass is not None and t <= last_pass), default=None)
    spans = []
    reversed_span = False
    for v in verdicts:
        t0 = _epoch(v["created"])
        if v["verdict"] in ("fail", "reject") and t0 is not None:
            nxt = min((t for t in reviews if t > t0), default=merged_e)
            if nxt < t0:  # fail verdict recorded after the merge: out-of-order events, duration unknown
                reversed_span = True
            spans.append((t0, nxt))
    rework = None if reversed_span else round(_union_len(spans))
    return {"total_s": d(first_spawn, merged_e), "coding_s": d(coder_spawn, pr_set),
            "review_s": d(in_review, last_pass), "judge_s": d(last_pass, merged_e), "rework_s": rework}


def throughput(con, days=14, now=None):
    """Merges per local day, done tasks per goal, and per merged task stage durations (seconds).

    Stages: total = first agent spawn for the task -> merged; coding = first coder spawn -> first
    `update` event setting pr= or status=in_review; review_s = last in_review update before the final
    reviewer/QA pass -> that pass; judge_s = last reviewer/QA pass -> merged; rework_s = union of
    intervals from each fail/reject verdict to the next in_review update (or merge). None = unknown.
    """
    now_d = _now(now).astimezone()
    first_day = now_d.date() - dt.timedelta(days=days - 1)
    cutoff = dt.datetime.combine(first_day, dt.time.min).astimezone().timestamp()
    last = {}
    for r in con.execute("SELECT id, ts, entity_id FROM events WHERE entity='task' AND action='merged' ORDER BY id"):
        e = _epoch(r["ts"])
        if e is not None:
            last[r["entity_id"]] = e
    per_day = {first_day + dt.timedelta(days=i): 0 for i in range(days)}
    win = {}
    for tid, e in last.items():
        if e >= cutoff:
            day = dt.datetime.fromtimestamp(e).astimezone().date()
            if day in per_day:
                per_day[day] += 1
            win[tid] = e
    ids = sorted(win)
    evs, vds, sps = {}, {}, {}
    for r in _in(con, "SELECT entity_id, ts, action, detail FROM events WHERE entity='task' AND action='update' "
                      "AND entity_id IN ({in}) ORDER BY id", ids):
        evs.setdefault(r["entity_id"], []).append(r)
    for r in _in(con, "SELECT task_id, role, verdict, created FROM verdicts WHERE task_id IN ({in}) ORDER BY id", ids):
        vds.setdefault(r["task_id"], []).append(r)
    for r in _in(con, "SELECT task_id, role, started FROM agents WHERE started IS NOT NULL AND task_id IN ({in})", ids):
        s = _epoch(r["started"])
        if s is not None:
            sps.setdefault(r["task_id"], []).append((r["role"], s))
    meta = {r["id"]: r for r in _in(con, "SELECT id, goal_id, title FROM tasks WHERE id IN ({in})", ids)}
    items = []
    for tid in ids:
        st = _stages(win[tid], evs.get(tid, []), vds.get(tid, []), sps.get(tid, []))
        st.update(task_id=tid, goal_id=meta[tid]["goal_id"] if tid in meta else None,
                  merged=dt.datetime.fromtimestamp(win[tid]).astimezone().isoformat(timespec="seconds"))
        items.append(st)
    per_goal = _dicts(con.execute(
        "SELECT t.goal_id, g.title, COUNT(*) done FROM tasks t LEFT JOIN goals g ON g.id=t.goal_id "
        "WHERE t.status='done' GROUP BY t.goal_id ORDER BY t.goal_id"))
    return {"days": days, "merges_per_day": [{"day": d.isoformat(), "merges": n} for d, n in per_day.items()],
            "tasks_per_goal": per_goal, "tasks": items}


# ───────────────────────────── quality / changelog ─────────────────────────────

def quality(con):
    """Per team and overall quality numbers.

    merged = distinct tasks with a `merged` event. rejections = verdict rows fail/reject plus judge-reject
    events with no judge reject verdict row. retries = sum(max(attempts-1, 0)).
    rej_reviewer / rej_qa / rej_judge split the rejections by source (the judge share includes those events).
    gate_judge_blocked / gate_merge_failed count the mechanical refusals (judge-blocked and merge-failed
    events: stale verdict, red or pending CI, conflicts); gate_refusals is their sum. They are not rejections.
    merged_first_try = merged tasks with no rejection; merged_after_rejection = merged - merged_first_try;
    rejections_per_merged = rejections / merged (2 decimals, None when merged is 0).
    ci_blocked = judge-blocked events whose detail contains "checks not green";
    ci_pass_rate = merged / (merged + distinct tasks with such a block), None when the denominator is 0.
    score = the latest evaluation for the team (None if never evaluated).
    unassigned = {goal_id: row} for the tasks with team_id NULL, to be added to every team of that goal.
    """
    tinfo = {r["id"]: r for r in con.execute("SELECT id, team_id, goal_id, attempts FROM tasks")}
    merged = {r[0] for r in con.execute("SELECT DISTINCT entity_id FROM events WHERE entity='task' AND action='merged'")}
    src = {}    # task -> {reviewer, qa, judge: rejection count}
    for r in con.execute("SELECT task_id, role FROM verdicts WHERE verdict IN ('fail','reject')"):
        d = src.setdefault(r["task_id"], {})
        d[r["role"]] = d.get(r["role"], 0) + 1
    judge_e = {}
    for r in con.execute("SELECT entity_id FROM events WHERE entity='task' AND action='judge-reject'"):
        judge_e[r[0]] = judge_e.get(r[0], 0) + 1
    for tid, n in judge_e.items():
        d = src.setdefault(tid, {})
        d["judge"] = d.get("judge", 0) + max(0, n - d.get("judge", 0))
    rej = {tid: sum(d.values()) for tid, d in src.items()}
    gate = {}   # task -> {judge-blocked, merge-failed: event count}
    for r in con.execute("SELECT entity_id, action FROM events WHERE entity='task' "
                         "AND action IN ('judge-blocked','merge-failed')"):
        d = gate.setdefault(r["entity_id"], {})
        d[r["action"]] = d.get(r["action"], 0) + 1
    ci_events = {}
    for r in con.execute("SELECT entity_id FROM events WHERE entity='task' AND action='judge-blocked' "
                         "AND detail LIKE '%checks not green%'"):
        ci_events[r[0]] = ci_events.get(r[0], 0) + 1
    score = {}
    for r in con.execute("SELECT team_id, score FROM evaluations ORDER BY id"):
        score[r["team_id"]] = r["score"]

    def row(team, ids):
        m = sum(1 for i in ids if i in merged)
        first = sum(1 for i in ids if i in merged and not rej.get(i))
        rj = sum(rej.get(i, 0) for i in ids)
        blocked = [i for i in ids if i in ci_events]
        gb = sum(gate.get(i, {}).get("judge-blocked", 0) for i in ids)
        gf = sum(gate.get(i, {}).get("merge-failed", 0) for i in ids)
        return {"team_id": team, "tasks": len(ids), "merged": m, "rejections": rj,
                "rej_reviewer": sum(src.get(i, {}).get("reviewer", 0) for i in ids),
                "rej_qa": sum(src.get(i, {}).get("qa", 0) for i in ids),
                "rej_judge": sum(src.get(i, {}).get("judge", 0) for i in ids),
                "gate_judge_blocked": gb, "gate_merge_failed": gf, "gate_refusals": gb + gf,
                "merged_first_try": first, "merged_after_rejection": m - first,
                "rejections_per_merged": round(rj / m, 2) if m else None,
                "retries": sum(max((tinfo[i]["attempts"] or 0) - 1, 0) for i in ids),
                "score": score.get(team) if team else None,
                "ci_blocked": sum(ci_events[i] for i in blocked),
                "ci_pass_rate": round(m / (m + len(blocked)), 3) if m + len(blocked) else None}

    by_team = {}
    by_goal = {}
    for tid, r in tinfo.items():
        by_team.setdefault(r["team_id"], []).append(tid)
        if r["team_id"] is None:
            by_goal.setdefault(r["goal_id"], []).append(tid)
    team_ids = sorted({r["id"] for r in con.execute("SELECT id FROM teams")} | {t for t in by_team if t})
    return {"teams": [row(t, by_team.get(t, [])) for t in team_ids], "overall": row(None, list(tinfo)),
            "unassigned": {g: row(None, ids) for g, ids in by_goal.items()}}


def changelog(con):
    """Merged tasks grouped by goal: [{goal_id, goal_title, items:[{task_id, title, pr_url, merged, sha}]}]."""
    last = {}
    for r in con.execute("SELECT id, ts, entity_id, detail FROM events WHERE entity='task' AND action='merged' ORDER BY id"):
        last[r["entity_id"]] = r
    rows = con.execute("SELECT t.id, t.title, t.pr_url, t.goal_id, g.title gtitle FROM tasks t "
                       "LEFT JOIN goals g ON g.id=t.goal_id ORDER BY t.goal_id, t.id")
    groups = {}
    for t in rows:
        ev = last.get(t["id"])
        if ev is None:
            continue
        gp = groups.setdefault(t["goal_id"], {"goal_id": t["goal_id"], "goal_title": t["gtitle"], "items": []})
        gp["items"].append({"task_id": t["id"], "title": _strip_tags(t["title"]), "pr_url": t["pr_url"],
                            "merged": ev["ts"], "sha": (ev["detail"] or "").lstrip("@").strip() or None})
    for gp in groups.values():
        gp["items"].sort(key=lambda i: (_epoch(i["merged"]) or 0), reverse=True)
    return [groups[k] for k in sorted(groups)]


# ───────────────────────────── config / resources ─────────────────────────────

def config(con):
    return _dicts(con.execute("SELECT key, value FROM config ORDER BY key"))


def resources_db(con, defaults=None):
    """DB-side resource view: live/queued counts, queue reasons, caps (config over `defaults`), recent events."""
    live = con.execute("SELECT COUNT(*) FROM agents WHERE status='live'").fetchone()[0]
    queued = _dicts(con.execute("SELECT name, role, queued_at FROM agents WHERE status='queued' ORDER BY queued_at, name"))
    reasons = {}
    for r in con.execute("SELECT entity_id, detail FROM events WHERE entity='agent' AND action='queued' ORDER BY id DESC LIMIT 500"):
        reasons.setdefault(r["entity_id"], _t(r["detail"], DETAIL_MAX))
    for q in queued:
        q["reason"] = reasons.get(q["name"])
    cfg = {r["key"]: r["value"] for r in con.execute("SELECT key, value FROM config")}
    caps = {}
    for k, v in (defaults or {}).items():
        caps[k] = {"value": cfg[k], "source": "config"} if k in cfg else {"value": v, "source": "default"}

    def recent(entity, action):
        return _dicts(con.execute(
            "SELECT id, ts, actor, entity_id, detail FROM events WHERE entity=? AND action=? ORDER BY id DESC LIMIT 50",
            (entity, action)))
    q_ev = recent("agent", "queued")
    for e in q_ev:
        e["detail"] = _t(e["detail"], DETAIL_MAX)
    return {"live": live, "queued_count": len(queued), "queued": queued, "caps": caps,
            "queued_events": q_ev, "config_events": recent("config", "set")}


# ───────────────────────────── app shell ─────────────────────────────

def _cfg_num(cfg, defaults, key, fallback):
    try:
        return float(cfg.get(key, (defaults or {}).get(key)))
    except (TypeError, ValueError):
        return float(fallback)


def shell_status(con, usage, defaults, mem=None):
    """Data for the global status header and nav badges, the same on every page:
    {needs, agents_attention, health: {tone, text}}. `usage` is the usage_fn() dict (or None), `defaults` the caps.
    needs = what owner_pending lists; agents_attention = queued or blocked agents (running agents never count).
    `mem` = MB available (mem_fn()), None when unknown.
    health, worst first: memory low, usage cap reached, gate closed, usage high, usage unknown, else all normal."""
    pend = owner_pending(con)
    needs = sum(len(v) for v in pend.values())
    n = dict(tuple(r) for r in con.execute("SELECT status, COUNT(*) FROM agents GROUP BY status"))
    queued = n.get("queued", 0)
    cfg = {k: v for k, v in con.execute("SELECT key, value FROM config")}
    cap = int(_cfg_num(cfg, defaults, "max_live_agents", 6))
    cap5 = _cfg_num(cfg, defaults, "max_five_hour_pct", 92)
    cap7 = _cfg_num(cfg, defaults, "max_seven_day_pct", 92)
    fresh = bool(usage) and not usage.get("unreadable") and usage.get("five_hour") is not None
    stale = fresh and (not usage.get("fresh") or (usage.get("age_min") or 0) > 30)
    p5 = usage["five_hour"] if fresh else None
    p7 = usage.get("seven_day") if fresh else None
    ok = fresh and not stale
    if mem is not None and mem < _cfg_num(cfg, defaults, "min_mem_available_mb", 1024):
        health = {"tone": "danger", "text": "Memory low"}
    elif ok and (p5 >= cap5 or (p7 is not None and p7 >= cap7)):
        health = {"tone": "danger", "text": "Usage cap reached"}
    elif n.get("live", 0) >= cap:
        health = {"tone": "warning", "text": f"Gate closed · {queued} queued" if queued else "Gate closed"}
    elif ok and p5 >= 0.8 * cap5:
        health = {"tone": "warning", "text": f"Claude usage {p5:.0f}%"}
    elif ok and p7 is not None and p7 >= 0.8 * cap7:
        health = {"tone": "warning", "text": f"Weekly usage {p7:.0f}%"}
    elif not fresh or stale:
        health = {"tone": "neutral", "text": "Usage unknown"}
    else:
        health = {"tone": "success", "text": "All normal"}
    return {"needs": needs, "agents_attention": queued + n.get("blocked", 0), "health": health}
