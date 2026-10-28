"""Skill scout report: the "## Skill scout" markdown section and the once-per-run message to the director (docs/scout-report.md).

Read-only DB queries over scout_candidates / scout_triage / skill_trials / scout_runs, plus send_pending(), which
the tick calls. bin/factory owns messaging and the clock; they reach this module as the `api` object.
"""
import datetime as dt
import json
import statistics

HEADING = "## Skill scout"
FOOTER = "Adoption of any third-party skill needs owner approval through the boss."
MAX_FINDS = 5
RANK = {"adopt": 0, "watch": 1, "skip": 2}
REPORTED = ("done", "stopped", "failed")   # terminal run statuses that get a report; a skipped run sends nothing
DEFAULT_HOURS = 24
TRIAL_ENDED = ("passed", "failed", "timeout", "over-budget", "error")


def ts(s):
    """ISO string or datetime -> aware datetime (naive = local); None when unparsable."""
    if isinstance(s, dt.datetime):
        t = s
    else:
        try:
            t = dt.datetime.fromisoformat(str(s).replace("Z", "+00:00"))
        except ValueError:
            return None
    return t.astimezone() if t.tzinfo is None else t


def _line(text):
    """One line of untrusted text: whitespace collapsed so a candidate cannot inject headings or list items."""
    return " ".join(str(text if text is not None else "-").split()) or "-"


def _link(c):
    if c["url"]:
        return c["url"]
    repo = c["repo"] or ""
    if repo.startswith("http"):
        return repo
    return f"https://github.com/{repo}" if repo else c["key"]


def _inwin(s, since, until):
    t = ts(s) if s else None
    return t is not None and since < t <= until


def _finds(con, since, until):
    """Newest triage per candidate triaged inside the window, best first: adopt > watch > skip, then stars/day."""
    by = {}
    for t in con.execute("SELECT * FROM scout_triage ORDER BY id"):
        if _inwin(t["created"], since, until):
            by[t["candidate_id"]] = t
    rows = []
    for cid, t in by.items():
        c = con.execute("SELECT * FROM scout_candidates WHERE id=?", (cid,)).fetchone()
        if c:
            rows.append((c, t))
    rows.sort(key=lambda r: (RANK.get(r[1]["recommendation"], 9),
                             -(r[0]["stars_per_day"] if r[0]["stars_per_day"] is not None else float("-inf")), r[0]["id"]))
    return rows


def _find_lines(c, t):
    stars = "-" if c["stars"] is None else str(c["stars"])
    spd = "-" if c["stars_per_day"] is None else f"{c['stars_per_day']:+.1f}/day"
    return [f"- **{c['id']}** {_link(c)} — REC **{t['recommendation']}**: {_line(t['rationale'])}",
            f"  - ★ {stars} ({spd}) · licence {_line(c['licence'])} · last commit {_line(c['last_commit'])}",
            f"  - purpose: {_line(t['purpose'])}",
            f"  - target roles: {_line(t['target_roles'])} · overlap: {_line(t['overlap'])}",
            f"  - install risk: {_line(c['install_risk'] or 'unknown')}"]


def _median(vals):
    vals = [v for v in vals if v is not None]
    return None if not vals else statistics.median(vals)


def _num(v):
    return "-" if v is None else str(int(v)) if float(v).is_integer() else f"{v:.1f}"


def _cell(rows):
    """Result of the trials of one task and condition: P / F, or the terminal status when it never got a verdict."""
    if not rows:
        return "-"
    out = []
    for r in rows:
        out.append("P" if r["status"] == "passed" and r["passed"] else "F" if r["status"] in ("passed", "failed") else r["status"])
    return "/".join(out)


def _tokens(r):
    parts = [r["tokens_in"], r["tokens_out"], r["cache_write"]]
    return None if all(p is None for p in parts) else sum(p or 0 for p in parts)


def _eval_lines(con, since, until):
    trials = [t for t in con.execute("SELECT * FROM skill_trials WHERE status IN (%s) ORDER BY id" % ",".join("?" * len(TRIAL_ENDED)),
                                     TRIAL_ENDED) if _inwin(t["ended"], since, until)]
    if not trials:
        return []
    out = ["### Eval results (indicative, n=1 per cell)", ""]
    for cid in dict.fromkeys(t["candidate_id"] for t in trials):
        c = con.execute("SELECT key FROM scout_candidates WHERE id=?", (cid,)).fetchone()
        out += [f"**{cid}** {c['key'] if c else '-'}", "",
                "| task | with | without | tokens with / without | wall s with / without | skill invoked |",
                "|---|---|---|---|---|---|"]
        mine = [t for t in trials if t["candidate_id"] == cid]
        for task in dict.fromkeys(t["task"] for t in mine):
            w = [t for t in mine if t["task"] == task and t["condition"] == "with"]
            wo = [t for t in mine if t["task"] == task and t["condition"] == "without"]
            invoked = [t["skill_invoked"] for t in w if t["skill_invoked"] is not None]
            inv = "-" if not invoked else "yes" if any(invoked) else "no"
            out.append(f"| {_line(task)} | {_cell(w)} | {_cell(wo)} | "
                       f"{_num(_median([_tokens(t) for t in w]))} / {_num(_median([_tokens(t) for t in wo]))} | "
                       f"{_num(_median([t['wall_s'] for t in w]))} / {_num(_median([t['wall_s'] for t in wo]))} | {inv} |")
        out.append("")
    return out


def build_skill_scout_section(con, since, until):
    """Markdown "## Skill scout" for the window (since, until]; since/until are datetimes or ISO strings."""
    since, until = ts(since), ts(until)
    finds = _finds(con, since, until)
    evals = _eval_lines(con, since, until)
    if not finds and not evals:
        return f"{HEADING}\nNo new finds.\n"
    out = [HEADING, ""]
    if finds:
        shown = finds[:MAX_FINDS]
        out += [f"### Finds ({len(shown)} of {len(finds)} triaged)", ""]
        for c, t in shown:
            out += _find_lines(c, t)
        out.append("")
    else:
        out += ["No new finds.", ""]
    out += evals
    exe = [(c, t) for c, t in finds if t["recommendation"] in ("adopt", "watch") and (c["install_risk"] or "unknown") != "prompt-only"]
    if exe:
        out += ["### Needs owner decision: executable parts", ""]
        for c, t in exe:
            try:
                detail = ", ".join(str(d) for d in json.loads(c["risk_detail"] or "[]")[:3])
            except (ValueError, TypeError):
                detail = ""
            out.append(f"- {c['id']} {_link(c)} — {t['recommendation']}, risk flags: {_line(c['install_risk'] or 'unknown')}"
                       + (f" ({_line(detail)})" if detail else ""))
        out.append("")
    out.append(FOOTER)
    return "\n".join(out) + "\n"


def run_window(con, run):
    """(since, until] of a finished run: from the end of the previous reported run (else 24 h before) to its own end."""
    until = ts(run["ended"])
    prev = con.execute("SELECT ended FROM scout_runs WHERE id<? AND reported_msg IS NOT NULL AND ended IS NOT NULL "
                       "ORDER BY id DESC LIMIT 1", (run["id"],)).fetchone()
    since = ts(prev["ended"]) if prev else None
    if since is None or since >= until:
        since = until - dt.timedelta(hours=DEFAULT_HOURS)
    return since, until


def subject(slot):
    """'2026-10-25T09' -> 'skill scout 2026-10-25 09'."""
    return "skill scout " + slot.replace("T", " ", 1)


def send_pending(con, api):
    """Send the section of every terminal, unreported, non-skipped run to the director, once each. The check, the
    message and reported_msg are one IMMEDIATE transaction, so two ticks cannot both send. Returns the message ids."""
    sent = []
    ids = [r["id"] for r in con.execute("SELECT id FROM scout_runs WHERE status IN (?,?,?) AND reported_msg IS NULL "
                                        "AND ended IS NOT NULL ORDER BY id", REPORTED)]
    for rid in ids:
        con.execute("BEGIN IMMEDIATE")
        try:
            run = con.execute("SELECT * FROM scout_runs WHERE id=? AND reported_msg IS NULL", (rid,)).fetchone()
            if run is not None:
                since, until = run_window(con, run)
                body = build_skill_scout_section(con, since, until)
                if run["new_candidates"] is not None:
                    body += f"\nThis run: {run['new_candidates']} new candidates, {run['triaged']} triaged.\n"
                mid, _rcpt = api.send(con, "director", subject(run["slot"]), body, "report", sender="tick")
                con.execute("UPDATE scout_runs SET reported_msg=? WHERE id=?", (mid, rid))
                api.event(con, "scout", run["slot"], "scout-report", f"M{mid}")
                sent.append(mid)
            con.execute("COMMIT")
        except BaseException:
            if con.in_transaction:
                con.execute("ROLLBACK")
            raise
    return sent
