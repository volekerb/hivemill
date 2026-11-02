"""Cursor plan limits recorded by the tick from in-pane usage-limit errors, and display data for the web UI."""
from __future__ import annotations

import datetime as dt
import json
import re

from scout import orgs as _orgs

CONFIG_KEY = "cursor_model_limits"
PLAN_TIER = "Pro"
_LIMIT_RE = re.compile(r"usage limit for\s+(.+?)(?:\.|$|\n)", re.I)
_RESET_RE = re.compile(
    r"(?:resets?(?:\s+on)?\s+)(?:(\d{4}-\d{2}-\d{2})|"
    r"((?:Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|"
    r"Aug(?:ust)?|Sep(?:tember)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)\s+\d{1,2},?\s+\d{4}))",
    re.I,
)
_SWITCH_RE = re.compile(r"otherModelsLimitSwitchTarget[\"'\s:=]+([^\s\"'\n,]+)", re.I)


def cursor_model_slugs():
    """Per-model slugs for the Cursor org (not the Claude evaluator model)."""
    return sorted({m for m in _orgs.ORGS["cursor"]["models"].values() if not m.startswith("claude-")})


def parse_limit_error(text):
    """Return {model, reset, switch_target} when the pane shows a usage-limit error, else None."""
    if not text or "usage limit" not in text.lower():
        return None
    m = _LIMIT_RE.search(text)
    if not m:
        return None
    model = m.group(1).strip().strip('"').strip("'")
    reset = None
    rm = _RESET_RE.search(text)
    if rm:
        if rm.group(1):
            reset = rm.group(1)
        else:
            try:
                reset = dt.datetime.strptime(rm.group(2).replace(",", ""), "%B %d %Y").date().isoformat()
            except ValueError:
                try:
                    reset = dt.datetime.strptime(rm.group(2).replace(",", ""), "%b %d %Y").date().isoformat()
                except ValueError:
                    reset = rm.group(2).strip()
    sw = _SWITCH_RE.search(text)
    switch_target = sw.group(1).strip().strip('"').strip("'") if sw else None
    return {"model": model, "reset": reset, "switch_target": switch_target}


def load_state(con):
    row = con.execute("SELECT value FROM config WHERE key=?", (CONFIG_KEY,)).fetchone()
    if not row:
        return {}
    try:
        data = json.loads(row["value"])
    except (TypeError, ValueError):
        return {}
    return data if isinstance(data, dict) else {}


def save_state(con, state):
    con.execute(
        "INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
        (CONFIG_KEY, json.dumps(state, sort_keys=True)),
    )


def _today_since(now):
    return now.astimezone().date().isoformat()


def _activity_today(con, now):
    from web import queries
    from web.views import tokens

    ag = queries.agents(con, include_ended=True, now=now)
    rows = tokens.cursor_activity(ag, _today_since(now), now, org="cursor")
    sessions = sum(r["sessions"] for r in rows)
    hours = round(sum(r["hours"] for r in rows), 2)
    return sessions, hours


def _model_rows(state):
    rows = []
    for slug in cursor_model_slugs():
        ent = state.get(slug) or {"status": "ok"}
        if ent.get("status") == "limited":
            reset = ent.get("reset") or ""
            rows.append({"model": slug, "status": "limited", "reset": reset, "reset_display": _fmt_reset(reset),
                         "switch_target": ent.get("switch_target")})
        else:
            rows.append({"model": slug, "status": "ok", "reset": None, "reset_display": None, "switch_target": None})
    return rows


def _fmt_reset(reset):
    if not reset:
        return ""
    if re.fullmatch(r"\d{4}-\d{2}-\d{2}", reset):
        try:
            return dt.date.fromisoformat(reset).strftime("%m-%d")
        except ValueError:
            return reset
    return reset


def display(con, now):
    """Data for the UI: tier, per-model limit state, today's sessions and agent-hours."""
    sessions, hours = _activity_today(con, now)
    return {"tier": PLAN_TIER, "models": _model_rows(load_state(con)), "sessions": sessions, "hours": hours}


def shell_line(data):
    """One-line summary for the global status header."""
    limits = " · ".join(_cursor_limit_text(m) for m in data["models"])
    spent = f"time spent {data['sessions']} sessions · {data['hours']} h"
    return f"Cursor {data['tier']} · {limits} · {spent}"


def _cursor_limit_text(m):
    if m["status"] == "limited":
        until = m["reset_display"] or m["reset"] or "unknown"
        return f"{m['model']} limited until {until}"
    return f"{m['model']} ok"


def tick_record(con, *, herdr_fn, agent_runtime_of, agent_org_fn, send_fn, event_fn, now_fn):
    """Scan live Cursor panes; update config; alert the agent's director on a new limit."""
    limited = {}
    for r in con.execute("SELECT * FROM agents WHERE status='live'").fetchall():
        if agent_runtime_of(r) != "cursor":
            continue
        tail = herdr_fn("agent", "read", r["name"], "--source", "recent-unwrapped", "--lines", "80", check=False) or {}
        text = tail.get("text") or tail.get("raw") or ""
        parsed = parse_limit_error(text)
        if not parsed:
            continue
        slug = r["model"] or parsed["model"]
        if slug not in cursor_model_slugs():
            slug = parsed["model"]
        limited[slug] = {
            "status": "limited",
            "reset": parsed.get("reset"),
            "switch_target": parsed.get("switch_target"),
            "agent": r["name"],
        }
        key = f"cursor-limit:{slug}"
        sig = json.dumps({"reset": parsed.get("reset"), "switch": parsed.get("switch_target")}, sort_keys=True)
        old = con.execute("SELECT ts FROM throttle WHERE key=?", (key,)).fetchone()
        if (old["ts"] if old else "") != sig:
            con.execute(
                "INSERT INTO throttle(key,ts) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET ts=excluded.ts",
                (key, sig),
            )
            org = agent_org_fn(con, r["name"])
            reset = parsed.get("reset") or "unknown date"
            send_fn(
                con,
                "director",
                f"Cursor limit: {slug}",
                f"{r['name']} hit the Cursor usage limit for {parsed.get('model') or slug}; "
                f"limited until {reset}"
                + (f"; switch target {parsed['switch_target']}" if parsed.get("switch_target") else "")
                + ".",
                "alert",
                r["goal_id"],
                r["task_id"],
                sender="tick",
                org=org,
            )
            event_fn(
                con,
                "agent",
                r["name"],
                "cursor-limit",
                json.dumps({"model": slug, "reset": parsed.get("reset"), "switch_target": parsed.get("switch_target")}),
            )

    state = {}
    for slug in cursor_model_slugs():
        if slug in limited:
            state[slug] = limited[slug]
        else:
            state[slug] = {"status": "ok"}
    save_state(con, state)
