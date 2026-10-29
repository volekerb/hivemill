"""Tick scheduling of the skill scout: one run per slot, idempotent, with a wall and token budget (docs/scout-tick.md).

bin/factory owns spawn, force_stop, the dequeue and the config; they reach this module as the `api` object, so
nothing here imports the CLI script. Every clock read goes through api.clock() ($FACTORY_NOW in tests), except the claimed-stale check in sync(),
which compares the scout-slot event stamp with api.now() on purpose.
"""
import datetime as dt
import sqlite3

from scout import report

TERMINAL = ("done", "stopped", "failed", "skipped")
CLAIM_STALE_S = 600      # a run still 'claimed' this long after its scout-slot event lost its spawn (a crash)
NAME = "scout"


def parse_slots(text):
    """'09,18' -> [9, 18] (sorted, unique). Anything else raises ValueError, which the tick wrapper records."""
    try:
        hours = sorted({int(x) for x in str(text).split(",") if x.strip()})
    except ValueError:
        hours = []
    if not hours or hours[0] < 0 or hours[-1] > 23:
        raise ValueError(f"scout_slots must be a comma list of hours 0-23, got {text!r}; fix it with `factory config scout_slots 09,18`")
    return hours


def current_slot(clock, hours):
    """Latest slot hour <= now as 'YYYY-MM-DDTHH' (local); before today's first slot, yesterday's last one."""
    due = [h for h in hours if h <= clock.hour]
    day, hour = (clock.date(), due[-1]) if due else (clock.date() - dt.timedelta(days=1), hours[-1])
    return f"{day.isoformat()}T{hour:02d}"


def _stamp(clock):
    return clock.isoformat(timespec="seconds")


def _ts(s):
    try:
        t = dt.datetime.fromisoformat(s)
    except (TypeError, ValueError):
        return None
    return t.astimezone() if t.tzinfo is None else t


def _finish(con, api, run_id, status, reason=None, session=None):
    """Move a non-terminal run to a terminal status once; the tokens are read best effort from the session log."""
    tokens = None
    if session:
        try:
            tokens = api.session_tokens(session)
        except (OSError, ValueError):
            tokens = None
    con.execute("UPDATE scout_runs SET status=?, stop_reason=coalesce(stop_reason, ?), ended=coalesce(ended, ?), "
                "tokens=coalesce(?, tokens) WHERE id=? AND status NOT IN ('done','stopped','failed','skipped')",
                (status, reason, _stamp(api.clock()), tokens, run_id))
    if status != "skipped":
        _count(con, run_id)


SUPERSEDED = "superseded by a newer run"
NEVER_SPAWNED = ("spawn failed", "claimed but never spawned", "scout agent row missing")


def _count(con, run_id):
    """Store the counts of candidates first seen and triage rows created in the run's [started, ended] window.
    The bounds are made explicit-UTC here (a naive stamp is local time) and julianday() compares across offsets;
    the window opens at the earlier of the run's and the agent's started (a run queued and dequeued in one tick is
    stamped late). scout_runs has no created/claimed column, so a superseded run that never got its own start ignores
    the agent's started: the agents row is shared by every scout run and may be an earlier run's start. Any other run
    ignores the agent's started when it is earlier than the run's latest scout-slot event (ts <= ended; slots repeat
    daily), which is the same earlier-run signature; with no such event the agent's started is used. A skipped run,
    and a failed run that was never spawned, keep NULL."""
    run = con.execute("SELECT slot, started, ended, stop_reason FROM scout_runs WHERE id=? AND status IN ('done','stopped','failed') "
                      "AND new_candidates IS NULL", (run_id,)).fetchone()
    end = _ts(run["ended"]) if run else None
    if end is None:
        return
    if run["started"] is None and (run["stop_reason"] or "").startswith(NEVER_SPAWNED):
        return
    superseded = run["started"] is None and (run["stop_reason"] or "").startswith(SUPERSEDED)
    agent = None if superseded else con.execute("SELECT started FROM agents WHERE name=?", (NAME,)).fetchone()
    astart = _ts(agent["started"]) if agent else None
    if astart is not None:
        evs = [t for e in con.execute("SELECT ts FROM events WHERE entity='scout' AND entity_id=? AND action='scout-slot' "
                                      "ORDER BY id DESC", (run["slot"],)) if (t := _ts(e["ts"])) is not None and t <= end]
        if evs and astart < max(evs):
            astart = None  # an earlier run's start
    starts = [t for t in (_ts(run["started"]), astart) if t is not None and t <= end]
    start = min(starts, default=end)
    lo, hi = (x.astimezone(dt.timezone.utc).isoformat() for x in (start, end))
    n = con.execute("SELECT COUNT(*) FROM scout_candidates WHERE julianday(first_seen) BETWEEN julianday(?) AND julianday(?)",
                    (lo, hi)).fetchone()[0]
    t = con.execute("SELECT COUNT(*) FROM scout_triage WHERE julianday(created) BETWEEN julianday(?) AND julianday(?)",
                    (lo, hi)).fetchone()[0]
    con.execute("UPDATE scout_runs SET new_candidates=?, triaged=? WHERE id=?", (n, t, run_id))


def _set_status(con, run_id, status, clock):
    con.execute("UPDATE scout_runs SET status=?, started=coalesce(started, ?) WHERE id=? "
                "AND status NOT IN ('done','stopped','failed','skipped')", (status, _stamp(clock), run_id))


def _budget(con, api, clock, run, agent):
    """Stop a live scout over its wall or token cap. The stop reason is stored first, so a stop the fallback finishes
    later still ends the run as 'stopped' with it."""
    if agent["stop_requested_at"]:
        return  # a stop is already in progress: the tick's own stop fallback retries it
    why = None
    started = _ts(agent["started"])
    if started and (clock - started).total_seconds() > int(api.cfg(con, "scout_max_minutes")) * 60:
        why = "wall"
    elif agent["session_id"] and api.session_tokens(agent["session_id"]) > int(api.cfg(con, "scout_max_tokens")):
        why = "tokens"
    if not why:
        return
    con.execute("UPDATE scout_runs SET stop_reason=? WHERE id=?", (why, run["id"]))
    if con.execute("UPDATE agents SET stop_requested_at=?, stop_attempt_at=NULL, stop_failed_kind=NULL "
                   "WHERE name=? AND status='live' AND stop_requested_at IS NULL", (api.now(), NAME)).rowcount != 1:
        return  # it finished meanwhile
    api.event(con, "scout", run["slot"], "scout-budget", why)
    res, _note = api.force_stop(con, NAME, "budget")
    if res == "stopped":
        _finish(con, api, run["id"], "stopped", why, agent["session_id"])


def sync(con, api, clock):
    """Bring the latest non-terminal run in line with the scout's agents row and enforce its budgets."""
    runs = con.execute("SELECT * FROM scout_runs WHERE status NOT IN ('done','stopped','failed','skipped') "
                       "ORDER BY id").fetchall()
    for old in runs[:-1]:
        _finish(con, api, old["id"], "failed", SUPERSEDED)
    if not runs:
        return
    run = runs[-1]
    agent = con.execute("SELECT * FROM agents WHERE name=?", (NAME,)).fetchone()
    if run["status"] == "claimed":
        # normally spawned in the same call that claimed it; one older than CLAIM_STALE_S lost its spawn (a crash)
        ev = con.execute("SELECT ts FROM events WHERE entity='scout' AND entity_id=? AND action='scout-slot' "
                         "ORDER BY id DESC LIMIT 1", (run["slot"],)).fetchone()
        at = _ts(ev["ts"]) if ev else None
        if at is None or (_ts(api.now()) - at).total_seconds() > CLAIM_STALE_S:
            _finish(con, api, run["id"], "failed", "claimed but never spawned")
        return
    if agent is None:
        _finish(con, api, run["id"], "failed", "scout agent row missing")
        return
    st, sid = agent["status"], agent["session_id"]
    if st == "queued":
        _set_status(con, run["id"], "queued", clock)
        queued = _ts(agent["queued_at"])
        limit_h = int(api.cfg(con, "scout_queue_max_h"))
        if queued and (clock - queued).total_seconds() > limit_h * 3600:
            if api.cancel_queued(con, NAME, "tick", f"scout queued > {limit_h}h"):
                _finish(con, api, run["id"], "skipped", f"gate closed > {limit_h}h")
    elif st == "starting":
        return  # claimed by the dequeue; live on a later tick
    elif st == "live":
        con.execute("UPDATE scout_runs SET status='running', started=coalesce(started, ?) WHERE id=? AND status IN ('queued','running')",
                    (_stamp(clock), run["id"]))
        _budget(con, api, clock, run, agent)
    elif st in ("done", "exited"):
        _finish(con, api, run["id"], "done", None, sid)
    elif st == "stopped":
        _finish(con, api, run["id"], "stopped", "stopped", sid)
    elif st == "lost":
        _finish(con, api, run["id"], "failed", "agent lost", sid)
    elif st == "cancelled":
        _finish(con, api, run["id"], "skipped", "dequeued")


def claim(con, api, slot):
    """One IMMEDIATE transaction decides the slot: 'claimed' (caller spawns), 'skipped', or None (already recorded)."""
    con.execute("BEGIN IMMEDIATE")
    try:
        if con.execute("SELECT 1 FROM scout_runs WHERE slot=?", (slot,)).fetchone():
            con.execute("ROLLBACK")
            return None
        busy = con.execute("SELECT 1 FROM agents WHERE name=? AND status IN ('live','starting','queued')", (NAME,)).fetchone()
        status, reason = ("skipped", "previous run active") if busy else ("claimed", None)
        con.execute("INSERT INTO scout_runs(slot, agent, status, stop_reason, ended) VALUES(?,?,?,?,?)",
                    (slot, None if busy else NAME, status, reason, _stamp(api.clock()) if busy else None))
        api.event(con, "scout", slot, "scout-slot", slot if status == "claimed" else f"{slot} skipped: {reason}")
        con.execute("COMMIT")
        return status
    except sqlite3.IntegrityError:  # UNIQUE(slot): another connection won
        if con.in_transaction:
            con.execute("ROLLBACK")
        return None
    except BaseException:
        if con.in_transaction:
            con.execute("ROLLBACK")
        raise


def start(con, api, slot):
    """Spawn the claimed run through spawn() (and so the gate): queued if the gate is closed, else running."""
    try:
        api.spawn(con, NAME, name=NAME, prompt=f"Scheduled scout run, slot {slot}.")
    except (Exception, SystemExit) as e:  # noqa: BLE001 — spawn() reports refusals with die(); no retry for the slot
        why = f"exit {e.code}; see the tick's stderr" if isinstance(e, SystemExit) else str(e)
        con.execute("UPDATE scout_runs SET status='failed', stop_reason=?, ended=? WHERE slot=? AND status='claimed'",
                    (f"spawn failed: {why}"[:300], _stamp(api.clock()), slot))
        api.event(con, "scout", slot, "scout-spawn-failed", why[:300])
        return
    agent = con.execute("SELECT status FROM agents WHERE name=?", (NAME,)).fetchone()
    status = "running" if agent and agent["status"] == "live" else "queued"
    con.execute("UPDATE scout_runs SET status=?, started=CASE WHEN ?='running' THEN ? END WHERE slot=? AND status='claimed'",
                (status, status, _stamp(api.clock()), slot))


def tick(con, api):
    """One scheduling pass (the cmd_tick step): sync the open run, then start the latest due slot if it has no run."""
    clock = api.clock()
    sync(con, api, clock)
    try:
        report.send_pending(con, api)  # once per finished run, to the director (docs/scout-report.md)
    except (Exception, SystemExit) as e:  # noqa: BLE001 — a failed report must not stop the next slot; it is retried next tick
        api.event(con, "scout", "-", "scout-report-failed", (str(e) or repr(e))[:300])
    if str(api.cfg(con, "scout_enabled")) != "1":
        return
    slot = current_slot(clock, parse_slots(api.cfg(con, "scout_slots")))
    if con.execute("SELECT 1 FROM scout_runs WHERE slot=?", (slot,)).fetchone():
        return
    if claim(con, api, slot) == "claimed":
        start(con, api, slot)
