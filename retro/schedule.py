"""Daily Cursor retro job: one run per local day from retro_hour (docs in T-1207 spec).

bin/factory supplies spawn, force_stop, cfg and the clock via the `api` namespace; this module never imports the CLI.
"""
import datetime as dt
import sqlite3

from scout import orgs

TERMINAL = ("done", "stopped", "failed", "skipped")
NAME = "cursor-retro"
ORG = "cursor"
ROLE = "retro"


def parse_hour(text):
    try:
        h = int(str(text).strip())
    except ValueError:
        h = -1
    if h < 0 or h > 23:
        raise ValueError(f"retro_hour must be an integer 0-23, got {text!r}; fix it with `factory config retro_hour 6`")
    return h


def parse_max_minutes(text):
    try:
        m = int(str(text).strip())
    except ValueError:
        m = 0
    if m < 1:
        raise ValueError(f"retro_max_minutes must be an integer >= 1, got {text!r}")
    return m


def _stamp(clock):
    return clock.isoformat(timespec="seconds")


def _ts(s):
    try:
        t = dt.datetime.fromisoformat(s)
    except (TypeError, ValueError):
        return None
    return t.astimezone() if t.tzinfo is None else t


def _record_config_fail(con, api, msg):
    today = api.clock().date().isoformat()
    failed = con.execute("SELECT value FROM config WHERE key='last_retro_config_fail_date'").fetchone()
    if failed and failed["value"] == today:
        return
    api.event(con, "retro", "-", "retro-failed", msg[:300])
    con.execute("INSERT INTO config(key,value) VALUES('last_retro_config_fail_date',?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value", (today,))


def _finish(con, api, run_id, status, reason=None):
    con.execute("UPDATE retro_runs SET status=?, stop_reason=coalesce(stop_reason, ?), ended=coalesce(ended, ?) "
                "WHERE id=? AND status NOT IN ('done','stopped','failed','skipped')",
                (status, reason, _stamp(api.clock()), run_id))


def _budget(con, api, clock, run, agent):
    if agent["stop_requested_at"]:
        return
    try:
        limit_m = parse_max_minutes(api.cfg(con, "retro_max_minutes"))
    except ValueError as e:
        _record_config_fail(con, api, str(e))
        return
    started = _ts(agent["started"])
    if started and (clock - started).total_seconds() > limit_m * 60:
        con.execute("UPDATE retro_runs SET stop_reason=? WHERE id=?", ("wall", run["id"]))
        if con.execute("UPDATE agents SET stop_requested_at=?, stop_attempt_at=NULL, stop_failed_kind=NULL "
                       "WHERE name=? AND status='live' AND stop_requested_at IS NULL", (api.now(), agent["name"])).rowcount != 1:
            return
        api.event(con, "retro", run["day"], "retro-budget", "wall")
        res, _note = api.force_stop(con, agent["name"], "budget")
        if res == "stopped":
            _finish(con, api, run["id"], "stopped", "wall")


def sync(con, api, clock):
    """Align the open retro run with its agent row and enforce the wall budget."""
    runs = con.execute("SELECT * FROM retro_runs WHERE status NOT IN ('done','stopped','failed','skipped') "
                       "ORDER BY id").fetchall()
    if not runs:
        return
    run = runs[-1]
    for old in runs[:-1]:
        _finish(con, api, old["id"], "failed", "superseded")
    agent = con.execute("SELECT * FROM agents WHERE name=?", (NAME,)).fetchone()
    if run["status"] == "claimed":
        return
    if agent is None:
        _finish(con, api, run["id"], "failed", "retro agent row missing")
        return
    st, sid = agent["status"], agent["session_id"]
    if st == "queued":
        con.execute("UPDATE retro_runs SET status='queued' WHERE id=? AND status='claimed'", (run["id"],))
    elif st == "starting":
        return
    elif st == "live":
        con.execute("UPDATE retro_runs SET status='running', started=coalesce(started, ?) WHERE id=? "
                    "AND status IN ('queued','running')", (_stamp(clock), run["id"]))
        _budget(con, api, clock, run, agent)
    elif st in ("done", "exited"):
        _finish(con, api, run["id"], "done", None)
    elif st == "stopped":
        _finish(con, api, run["id"], "stopped", run["stop_reason"] or "stopped")
    elif st == "lost":
        _finish(con, api, run["id"], "failed", "agent lost")
    elif st == "cancelled":
        _finish(con, api, run["id"], "skipped", "dequeued")


def claim(con, api, day):
    con.execute("BEGIN IMMEDIATE")
    try:
        if con.execute("SELECT 1 FROM retro_runs WHERE day=?", (day,)).fetchone():
            con.execute("ROLLBACK")
            return None
        busy = con.execute("SELECT 1 FROM agents WHERE name=? AND status IN ('live','starting','queued')", (NAME,)).fetchone()
        status, reason = ("skipped", "previous run active") if busy else ("claimed", None)
        con.execute("INSERT INTO retro_runs(day, status, agent, stop_reason, ended) VALUES(?,?,?,?,?)",
                    (day, status, None if busy else NAME, reason, _stamp(api.clock()) if busy else None))
        api.event(con, "retro", day, "retro-day", day if status == "claimed" else f"{day} skipped: {reason}")
        con.execute("COMMIT")
        return status
    except sqlite3.IntegrityError:
        if con.in_transaction:
            con.execute("ROLLBACK")
        return None
    except BaseException:
        if con.in_transaction:
            con.execute("ROLLBACK")
        raise


def start(con, api, day):
    model = orgs.model(ORG, ROLE)
    try:
        api.spawn(con, ROLE, name=NAME, org=ORG, model=model,
                  prompt=f"Scheduled daily retro for {day}. Run factory retro packet, investigate, then factory retro file.")
    except (Exception, SystemExit) as e:  # noqa: BLE001
        why = f"exit {e.code}; see the tick's stderr" if isinstance(e, SystemExit) else str(e)
        con.execute("UPDATE retro_runs SET status='failed', stop_reason=?, ended=? WHERE day=? AND status='claimed'",
                    (f"spawn failed: {why}"[:300], _stamp(api.clock()), day))
        api.event(con, "retro", day, "retro-spawn-failed", why[:300])
        return
    agent = con.execute("SELECT status FROM agents WHERE name=?", (NAME,)).fetchone()
    status = "running" if agent and agent["status"] == "live" else "queued"
    con.execute("UPDATE retro_runs SET status=?, started=CASE WHEN ?='running' THEN ? END WHERE day=? AND status='claimed'",
                (status, status, _stamp(api.clock()), day))


def tick(con, api):
    clock = api.clock()
    sync(con, api, clock)
    if str(api.cfg(con, "retro_enabled")) != "1":
        return
    try:
        hour = parse_hour(api.cfg(con, "retro_hour"))
    except ValueError as e:
        _record_config_fail(con, api, str(e))
        return
    if clock.hour < hour:
        return
    # T-1135: do not claim or spawn while other rows are still queued — the tick dequeue owns their starts.
    if con.execute("SELECT 1 FROM agents WHERE status IN ('queued','starting') AND role != ? LIMIT 1", (ROLE,)).fetchone():
        return
    day = clock.date().isoformat()
    if con.execute("SELECT 1 FROM retro_runs WHERE day=?", (day,)).fetchone():
        return
    if claim(con, api, day) == "claimed":
        start(con, api, day)
