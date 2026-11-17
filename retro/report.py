"""Retro packet, filed reports, and the daily digest section (T-1207)."""
import datetime as dt
import json

HEADING = "## Retro"
FIX_TYPES = ("check", "template", "prompt_rule")
WINDOW_HOURS = 24


def ts(s):
    try:
        t = dt.datetime.fromisoformat(s)
    except (TypeError, ValueError):
        return None
    return t.astimezone() if t.tzinfo is None else t


def window_bounds(end, hours=WINDOW_HOURS):
    end = ts(end) if not isinstance(end, dt.datetime) else end
    if end is None:
        raise ValueError("window end is not a valid timestamp")
    return end - dt.timedelta(hours=hours), end


def _inwin(s, start, end):
    d = ts(s)
    return d is not None and start < d <= end


def cause_counts(con, start, end):
    start_s = start.isoformat()
    end_s = end.isoformat()
    return {r["cause"]: r["n"] for r in con.execute(
        "SELECT cause, COUNT(*) n FROM rejection_incidents "
        "WHERE julianday(created) > julianday(?) AND julianday(created) <= julianday(?) "
        "GROUP BY cause ORDER BY n DESC, cause", (start_s, end_s))}


def top_causes(counts, limit=5):
    items = [(c, n) for c, n in counts.items() if n > 0]
    items.sort(key=lambda x: (-x[1], x[0]))
    return [{"cause": c, "count": n} for c, n in items[:limit]]


def incidents_in_window(con, start, end):
    start_s, end_s = start.isoformat(), end.isoformat()
    rows = con.execute(
        "SELECT ri.cause, ri.kind, ri.task_id, coalesce(t.pr_url, '') AS pr_url, ri.verdict_id "
        "FROM rejection_incidents ri LEFT JOIN tasks t ON t.id = ri.task_id "
        "WHERE julianday(ri.created) > julianday(?) AND julianday(ri.created) <= julianday(?) "
        "ORDER BY ri.created, ri.id", (start_s, end_s)).fetchall()
    return [{"cause": r["cause"], "kind": r["kind"], "task_id": r["task_id"],
             "pr_url": r["pr_url"] or "", "verdict_id": r["verdict_id"]} for r in rows]


def build_packet(con, clock, hours=WINDOW_HOURS):
    end = clock if isinstance(clock, dt.datetime) else ts(clock)
    if end is None:
        raise ValueError("clock is not a valid timestamp")
    start, end = window_bounds(end, hours)
    counts = cause_counts(con, start, end)
    return {
        "window_start": start.isoformat(),
        "window_end": end.isoformat(),
        "causes": top_causes(counts),
        "incidents": incidents_in_window(con, start, end),
    }


def _parse_causes(body):
    raw = body.get("causes")
    if not isinstance(raw, list):
        raise ValueError("causes must be a list")
    out = []
    for item in raw:
        if not isinstance(item, dict) or "cause" not in item or "count" not in item:
            raise ValueError("each cause must be {cause, count}")
        out.append({"cause": str(item["cause"]), "count": int(item["count"])})
    return out


def _recomputed(con, window_end, hours=WINDOW_HOURS):
    start, end = window_bounds(window_end, hours)
    return top_causes(cause_counts(con, start, end))


def validate_file(con, body):
    """Return (day, normalized body) or raise ValueError with a user-facing message."""
    if not isinstance(body, dict):
        raise ValueError("body must be a JSON object")
    wend = body.get("window_end")
    if not wend:
        raise ValueError("window_end is required")
    end = ts(wend)
    if end is None:
        raise ValueError("window_end must be an ISO timestamp")
    got = _parse_causes(body)
    want = _recomputed(con, end)
    if got != want:
        raise ValueError("causes do not match the incidents in the window")
    evidence = body.get("evidence")
    fixes = body.get("fixes")
    if not isinstance(evidence, list) or not isinstance(fixes, list):
        raise ValueError("evidence and fixes must be arrays")
    if len(evidence) > 8:
        raise ValueError("at most 8 evidence items")
    if not want:
        if evidence or fixes:
            raise ValueError("an empty window requires empty evidence and fixes")
        return end.date().isoformat(), {"causes": [], "evidence": [], "fixes": []}
    if not (1 <= len(fixes) <= 2):
        raise ValueError("fixes must have length 1 or 2 when causes are non-empty")
    allowed = {c["cause"] for c in want}
    start, end_dt = window_bounds(end)
    start_s, end_s = start.isoformat(), end_dt.isoformat()
    for i, ev in enumerate(evidence):
        if not isinstance(ev, dict):
            raise ValueError(f"evidence[{i}] must be an object")
        for k in ("task_id", "pr_url", "cause"):
            if k not in ev:
                raise ValueError(f"evidence[{i}] missing {k}")
        if ev["cause"] not in allowed:
            raise ValueError(f"evidence[{i}] cause not in the recomputed list")
        tid, cause = ev["task_id"], ev["cause"]
        row = con.execute(
            "SELECT 1 FROM rejection_incidents ri WHERE ri.task_id=? AND ri.cause=? "
            "AND julianday(ri.created) > julianday(?) AND julianday(ri.created) <= julianday(?)",
            (tid, cause, start_s, end_s)).fetchone()
        if not row:
            raise ValueError(f"evidence[{i}] task {tid} is not an incident for {cause} in the window")
        pr = con.execute("SELECT pr_url FROM tasks WHERE id=?", (tid,)).fetchone()
        want_pr = (pr["pr_url"] or "") if pr else ""
        if (ev.get("pr_url") or "") != want_pr:
            raise ValueError(f"evidence[{i}] pr_url must match the task")
    norm_fixes = []
    want_set = set(allowed)
    for i, fx in enumerate(fixes):
        if not isinstance(fx, dict):
            raise ValueError(f"fixes[{i}] must be an object")
        for k in ("type", "cause", "text"):
            if k not in fx:
                raise ValueError(f"fixes[{i}] missing {k}")
        if fx["type"] not in FIX_TYPES:
            raise ValueError(f"fixes[{i}] type must be one of {FIX_TYPES}")
        if fx["cause"] not in want_set:
            raise ValueError(f"fixes[{i}] cause not in the recomputed list")
        text = fx["text"]
        if not isinstance(text, str) or not (1 <= len(text) <= 400):
            raise ValueError(f"fixes[{i}] text must be 1-400 characters")
        norm_fixes.append({"type": fx["type"], "cause": fx["cause"], "text": text})
    return end.date().isoformat(), {"causes": want, "evidence": evidence, "fixes": norm_fixes}


def store_report(con, day, body, author, created):
    con.execute(
        "INSERT INTO retro_reports(day, causes, evidence, fixes, author, created) VALUES(?,?,?,?,?,?) "
        "ON CONFLICT(day) DO UPDATE SET causes=excluded.causes, evidence=excluded.evidence, "
        "fixes=excluded.fixes, author=excluded.author, created=excluded.created",
        (day, json.dumps(body["causes"]), json.dumps(body["evidence"]), json.dumps(body["fixes"]), author, created))


def load_report(con, day):
    return con.execute("SELECT * FROM retro_reports WHERE day=?", (day,)).fetchone()


def build_digest_section(con, start, end):
    """Markdown retro section for (start, end]; start/end are datetimes."""
    rows = [r for r in con.execute("SELECT * FROM retro_reports ORDER BY day")
            if _inwin(r["created"], start, end)]
    if not rows:
        return f"{HEADING}\n_nothing_\n"
    row = rows[-1]
    try:
        causes = json.loads(row["causes"] or "[]")
        evidence = json.loads(row["evidence"] or "[]")
        fixes = json.loads(row["fixes"] or "[]")
    except (ValueError, TypeError):
        return f"{HEADING}\n_nothing_\n"
    out = [HEADING, ""]
    for c in causes:
        out.append(f"- {c['cause']}: {c['count']}")
    for ev in evidence:
        tid = ev.get("task_id", "")
        url = ev.get("pr_url") or ""
        cause = ev.get("cause", "")
        out.append(f"- {tid} {url} ({cause})")
    for fx in fixes:
        out.append(f"- {fx.get('type', '')} — {fx.get('text', '')}")
    return "\n".join(out) + "\n"


# ── adopted fixes (T-1695) ──
FIX_STATUSES = ("pending", "adopted", "held", "reopened", "noise")
SIG_ERR = "signature needs 1-10 '|'-separated terms of 3-80 characters"


def parse_signature(text):
    """'|'-joined stripped terms; ValueError(SIG_ERR) when not 1-10 terms of 3-80 chars without newlines."""
    if not isinstance(text, str) or "\n" in text or "\r" in text:
        raise ValueError(SIG_ERR)
    terms = [t.strip() for t in text.split("|")]
    if not 1 <= len(terms) <= 10 or any(not 3 <= len(t) <= 80 for t in terms):
        raise ValueError(SIG_ERR)
    return "|".join(terms)


def merged_at(con, task_id):
    """ts of the earliest `merged` event of the task, or None."""
    row = con.execute("SELECT ts FROM events WHERE entity='task' AND entity_id=? AND action='merged' "
                      "ORDER BY julianday(ts), id LIMIT 1", (task_id,)).fetchone()
    return row["ts"] if row else None


def _next_fix_id(con):
    n = max([int(r["id"].split("-")[1]) for r in con.execute("SELECT id FROM adopted_fixes")] or [0]) + 1
    return f"F-{n:03d}"


def adopt(con, day, fix_n, task_id, signature, author, created, ev):
    """Record a retro fix as adopted (task merged) or pending; returns the stdout line. ev(entity, id, action, detail)."""
    row = load_report(con, day)
    if not row:
        raise ValueError(f"no retro report for {day}")
    fixes = json.loads(row["fixes"] or "[]")
    if not 1 <= fix_n <= len(fixes):
        raise ValueError(f"report {day} has {len(fixes)} fixes; --fix must be 1..{len(fixes)}")
    task = con.execute("SELECT id, pr_url FROM tasks WHERE id=?", (task_id,)).fetchone()
    if not task:
        raise ValueError(f"unknown task {task_id}")
    dup = con.execute("SELECT id FROM adopted_fixes WHERE report_day=? AND fix_index=?", (day, fix_n)).fetchone()
    if dup:
        raise ValueError(f"fix {day}#{fix_n} is already {dup['id']}")
    sig = parse_signature(signature)
    fx = fixes[fix_n - 1]
    fid = _next_fix_id(con)
    at = merged_at(con, task_id)
    status = "adopted" if at else "pending"
    con.execute("INSERT INTO adopted_fixes(id, report_day, fix_index, cause, type, text, signature, task_id, pr_url, status, "
                "adopted_at, author, created) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
                (fid, day, fix_n, fx["cause"], fx["type"], fx["text"], sig, task_id, task["pr_url"] if at else None,
                 status, at, author, created))
    ev("retro", fid, "fix-adopted" if at else "fix-linked", f"{day}#{fix_n} {task_id}")
    if at:
        return f"adopted {fid} ({fx['type']}, {fx['cause']}) {task_id} adopted_at {at}"
    return f"linked {fid} ({fx['type']}, {fx['cause']}) {task_id}: pending until {task_id} merges"


def sync_adopted(con, ev):
    """pending -> adopted for every row whose task now has a merged event (adopted_at, pr_url set)."""
    pend = con.execute("SELECT id, task_id, report_day, fix_index FROM adopted_fixes WHERE status='pending' ORDER BY id").fetchall()
    for r in pend:
        at = merged_at(con, r["task_id"])
        if not at:
            continue
        t = con.execute("SELECT pr_url FROM tasks WHERE id=?", (r["task_id"],)).fetchone()
        con.execute("UPDATE adopted_fixes SET status='adopted', adopted_at=?, pr_url=? WHERE id=?",
                    (at, t["pr_url"] if t else None, r["id"]))
        ev("retro", r["id"], "fix-adopted", f"{r['report_day']}#{r['fix_index']} {r['task_id']}")


def fixes_lines(con, status=None):
    """One line per row (by id) then the cycles line; cycles counts held/reopened/noise rows over all rows."""
    if status is not None and status not in FIX_STATUSES:
        raise ValueError(f"--status must be one of {', '.join(FIX_STATUSES)}")
    rows = con.execute("SELECT * FROM adopted_fixes ORDER BY id").fetchall()
    cyc = [r for r in rows if r["status"] in ("held", "reopened", "noise")]
    cycles = f"cycles: {len(cyc)} (check/tool: {sum(1 for r in cyc if r['type'] == 'check')})"
    shown = [r for r in rows if status is None or r["status"] == status]
    if not shown:
        return ["no adopted fixes", cycles]
    return [f"{r['id']} {r['status']} {r['type']} {r['cause']} {r['task_id']} adopted_at={r['adopted_at'] or '-'} "
            f"checked_at={r['checked_at'] or '-'} sig={r['signature']}" for r in shown] + [cycles]
