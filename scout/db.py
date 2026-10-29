"""Skill scout DB layer: DDL, candidate upsert/dedupe, star trend, cursors, triage.

Stdlib only, no network. Functions take an open sqlite3 connection (row_factory=sqlite3.Row) and
use bound parameters only: candidate text (READMEs, descriptions) is untrusted input.
"""
import datetime as dt
import json
import re

SCOUT_DDL = """
CREATE TABLE IF NOT EXISTS scout_candidates (
  id TEXT PRIMARY KEY,            -- C-0001
  key TEXT NOT NULL UNIQUE,       -- 'gh:<owner>/<repo>[/<path>]' or 'mkt:<marketplace>/<plugin>'
  kind TEXT,                      -- skill | plugin | agent | hook | mcp | collection | unknown
  repo TEXT, path TEXT, url TEXT,
  licence TEXT, last_commit TEXT, default_branch TEXT, head_sha TEXT,
  stars INTEGER, stars_per_day REAL,
  install_risk TEXT,              -- prompt-only | scripts | hooks | mcp | network | shell-in-markdown | symlinks | unknown
  risk_detail TEXT,               -- JSON: offending paths (max 20)
  sources TEXT NOT NULL DEFAULT '[]',
  first_seen TEXT NOT NULL, last_seen TEXT NOT NULL, seen_count INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'new');  -- new | triaged | needs-owner | queued-eval | evaluated | dismissed
CREATE TABLE IF NOT EXISTS scout_triage (
  id INTEGER PRIMARY KEY AUTOINCREMENT, candidate_id TEXT NOT NULL REFERENCES scout_candidates(id),
  purpose TEXT NOT NULL, target_roles TEXT NOT NULL, overlap TEXT NOT NULL,
  recommendation TEXT NOT NULL,   -- adopt | watch | skip
  rationale TEXT NOT NULL, eval_suggested INTEGER NOT NULL DEFAULT 0,
  run_id INTEGER, author TEXT, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scout_stars (
  candidate_id TEXT NOT NULL, ts TEXT NOT NULL, stars INTEGER NOT NULL, PRIMARY KEY(candidate_id, ts));
CREATE TABLE IF NOT EXISTS scout_cursors (
  source TEXT PRIMARY KEY, cursor TEXT, updated TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scout_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, slot TEXT NOT NULL UNIQUE,   -- '2026-10-25T09'; UNIQUE = no double run per slot
  agent TEXT, status TEXT NOT NULL,      -- claimed | queued | running | done | stopped | failed | skipped
  started TEXT, ended TEXT, tokens INTEGER, new_candidates INTEGER, triaged INTEGER,
  stop_reason TEXT, reported_msg INTEGER);
CREATE TABLE IF NOT EXISTS skill_trials (
  id INTEGER PRIMARY KEY AUTOINCREMENT, candidate_id TEXT NOT NULL REFERENCES scout_candidates(id),
  vendor_sha TEXT NOT NULL, task TEXT NOT NULL, condition TEXT NOT NULL,   -- with | without
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | running | passed | failed | timeout | over-budget | error | cancelled | voided
  agent TEXT, sandbox TEXT, queued TEXT NOT NULL, started TEXT, ended TEXT,
  passed INTEGER, wall_s INTEGER, tokens_in INTEGER, tokens_out INTEGER, cache_read INTEGER, cache_write INTEGER,
  skill_invoked INTEGER, check_output TEXT, author TEXT,
  void_reason TEXT, void_why TEXT,   -- set by `skilleval void`; the row keeps every other field
  UNIQUE(candidate_id, vendor_sha, task, condition));
"""

FACTS = ("kind", "repo", "path", "url", "licence", "last_commit", "default_branch", "head_sha",
         "stars", "install_risk", "risk_detail")
RECS = ("adopt", "watch", "skip")
TEXT_MAX = 600
TREND_DAYS = 14


class ScoutError(ValueError):
    """Invalid input; the CLI maps it to exit 2."""


def now():
    return dt.datetime.now().astimezone().isoformat(timespec="seconds")


def normalize_key(key):
    """Lowercase owner/repo, strip a trailing '.git' and '/'. A path after owner/repo keeps its case."""
    k = (key or "").strip()
    if not k or ":" not in k:
        raise ScoutError(f"bad candidate key {key!r}: expected 'gh:<owner>/<repo>' or 'mkt:<marketplace>/<plugin>'")
    scheme, rest = k.split(":", 1)
    scheme = scheme.lower()
    rest = rest.strip().rstrip("/")
    if not rest:
        raise ScoutError(f"bad candidate key {key!r}")
    if scheme == "gh":
        parts = rest.split("/")
        if len(parts) < 2 or not parts[0] or not parts[1]:
            raise ScoutError(f"bad candidate key {key!r}: expected gh:<owner>/<repo>[/<path>]")
        if parts[1].endswith(".git"):
            parts[1] = parts[1][:-4]
        parts[0], parts[1] = parts[0].lower(), parts[1].lower()
        rest = "/".join(parts)
    else:
        rest = rest.lower()
    return f"{scheme}:{rest}"


def _next_candidate_id(con):
    rows = con.execute("SELECT id FROM scout_candidates").fetchall()
    n = max([int(r["id"].split("-")[1]) for r in rows] or [0]) + 1
    return f"C-{n:04d}"


def upsert_candidate(con, key, source, ts=None, **facts):
    """New key -> a C-nnnn row, status new. Known key -> facts updated (None values keep the old value),
    last_seen set, seen_count+1, source merged into the sources JSON. Returns (id, created)."""
    bad = set(facts) - set(FACTS)
    if bad:
        raise ScoutError(f"unknown candidate fact(s): {', '.join(sorted(bad))}")
    if not (source or "").strip():
        raise ScoutError("source is required")
    key, ts = normalize_key(key), ts or now()
    facts = {k: v for k, v in facts.items() if v is not None}
    if isinstance(facts.get("risk_detail"), (list, dict)):
        facts["risk_detail"] = json.dumps(facts["risk_detail"])
    row = con.execute("SELECT id, sources FROM scout_candidates WHERE key=?", (key,)).fetchone()
    if row:
        sources = json.loads(row["sources"] or "[]")
        if source not in sources:
            sources.append(source)
        sets = ["sources=?", "last_seen=?", "seen_count=seen_count+1"] + [f"{k}=?" for k in facts]
        con.execute(f"UPDATE scout_candidates SET {', '.join(sets)} WHERE id=?",
                    (json.dumps(sources), ts, *facts.values(), row["id"]))
        return row["id"], False
    cid = _next_candidate_id(con)
    cols = ["id", "key", "sources", "first_seen", "last_seen"] + list(facts)
    con.execute(f"INSERT INTO scout_candidates({','.join(cols)}) VALUES({','.join('?' * len(cols))})",
                (cid, key, json.dumps([source]), ts, ts, *facts.values()))
    return cid, True


def _parse_ts(ts):
    """ISO text -> an aware datetime. A naive stamp is local time, so aware and naive rows compare."""
    t = dt.datetime.fromisoformat(ts.replace("Z", "+00:00"))
    return t.astimezone() if t.tzinfo is None else t


def snapshot_stars(con, cid, stars, ts=None):
    """Record a star count and recompute stars_per_day from the first and latest snapshot within TREND_DAYS
    of the latest one (None while there is only one snapshot or no time between them). Returns the rate."""
    try:
        ts = _parse_ts(ts).isoformat(timespec="seconds") if ts else now()  # always stored aware
    except ValueError:
        raise ScoutError(f"bad star snapshot timestamp {ts!r}: expected ISO 8601") from None
    if not con.execute("SELECT 1 FROM scout_candidates WHERE id=?", (cid,)).fetchone():
        raise ScoutError(f"no candidate {cid}")
    con.execute("INSERT OR REPLACE INTO scout_stars(candidate_id, ts, stars) VALUES(?,?,?)", (cid, ts, int(stars)))
    # julianday, not the text: after a DST switch the offsets differ and string order is not time order
    snaps = [(_parse_ts(r["ts"]), r["stars"]) for r in
             con.execute("SELECT ts, stars FROM scout_stars WHERE candidate_id=? ORDER BY julianday(ts), ts", (cid,))]
    last_t, last_s = snaps[-1]
    window = [s for s in snaps if last_t - s[0] <= dt.timedelta(days=TREND_DAYS)]
    first_t, first_s = window[0]
    days = (last_t - first_t).total_seconds() / 86400
    rate = round((last_s - first_s) / days, 4) if days > 0 else None
    con.execute("UPDATE scout_candidates SET stars=?, stars_per_day=? WHERE id=?", (last_s, rate, cid))
    return rate


def get_cursor(con, source):
    r = con.execute("SELECT cursor FROM scout_cursors WHERE source=?", (source,)).fetchone()
    return r["cursor"] if r else None


def set_cursor(con, source, cursor, ts=None):
    con.execute("INSERT INTO scout_cursors(source, cursor, updated) VALUES(?,?,?) "
                "ON CONFLICT(source) DO UPDATE SET cursor=excluded.cursor, updated=excluded.updated",
                (source, cursor, ts or now()))


def _text(name, val):
    if not (val or "").strip():
        raise ScoutError(f"--{name} is required and may not be empty")
    if len(val) > TEXT_MAX:
        raise ScoutError(f"--{name} is {len(val)} chars, the limit is {TEXT_MAX}")
    return val


def parse_roles(val, valid_roles):
    parts = [p.strip() for p in (val or "").split(",") if p.strip()]
    if not parts:
        raise ScoutError("--roles is required: a comma list of roles, or 'none'")
    if "none" in parts:
        if parts != ["none"]:
            raise ScoutError("--roles: 'none' cannot be combined with other roles")
        return ["none"]
    bad = [p for p in parts if p not in valid_roles]
    if bad:
        raise ScoutError(f"--roles: unknown role(s) {', '.join(bad)}; valid: {', '.join(valid_roles)}, none")
    return list(dict.fromkeys(parts))


def add_triage(con, cid, purpose, roles, overlap, rec, why, valid_roles, suggest_eval=False,
               run_id=None, author=None, ts=None):
    """Validate everything first; write nothing on error. Sets a 'new' candidate to 'triaged'."""
    if rec not in RECS:
        raise ScoutError(f"--rec must be one of {', '.join(RECS)}")
    role_list = parse_roles(roles, valid_roles)
    purpose, overlap, why = _text("purpose", purpose), _text("overlap", overlap), _text("why", why)
    if not con.execute("SELECT 1 FROM scout_candidates WHERE id=?", (cid,)).fetchone():
        raise ScoutError(f"no candidate {cid}")
    cur = con.execute(
        "INSERT INTO scout_triage(candidate_id, purpose, target_roles, overlap, recommendation, rationale, "
        "eval_suggested, run_id, author, created) VALUES(?,?,?,?,?,?,?,?,?,?)",
        (cid, purpose, ",".join(role_list), overlap, rec, why, 1 if suggest_eval else 0, run_id, author, ts or now()))
    con.execute("UPDATE scout_candidates SET status='triaged' WHERE id=? AND status='new'", (cid,))
    return cur.lastrowid


# Triage order (docs/scout-pending.md): skills before vendor plugins, prompt-only before flagged, seen outside the
# marketplace source first, then trending, then oldest. Nothing is filtered; this only decides what fits the cap.
PENDING_ORDER = (
    "CASE WHEN c.kind IN ('skill','collection','agent') THEN 0 ELSE 1 END, "
    "CASE WHEN c.install_risk = 'prompt-only' THEN 0 ELSE 1 END, "
    "CASE WHEN EXISTS (SELECT 1 FROM json_each(c.sources) j WHERE j.value <> 'marketplace') THEN 0 ELSE 1 END, "
    "c.stars_per_day IS NULL, c.stars_per_day DESC, julianday(c.first_seen), c.id")


def pending(con, limit=8):
    if limit < 1:
        raise ScoutError(f"--limit must be at least 1, got {limit}")
    return con.execute(
        "SELECT * FROM scout_candidates c WHERE NOT EXISTS (SELECT 1 FROM scout_triage t WHERE t.candidate_id=c.id) "
        "AND c.status='new' ORDER BY " + PENDING_ORDER + " LIMIT ?", (limit,)).fetchall()


def inventory(plugins_dir):
    """Skill and plugin names found under roles/plugins/**: [(kind, name, relative dir)]."""
    out = []
    if not plugins_dir.is_dir():
        return out
    for f in sorted(plugins_dir.rglob("SKILL.md")):
        out.append(("skill", f.parent.name, str(f.parent.relative_to(plugins_dir))))
    for f in sorted(plugins_dir.rglob("plugin.json")):
        if f.parent.name == ".claude-plugin":
            out.append(("plugin", f.parent.parent.name, str(f.parent.parent.relative_to(plugins_dir))))
    return out


def list_candidates(con, new=False, rec=None, risk=None, since=None):
    q, args = ("SELECT c.*, (SELECT recommendation FROM scout_triage t WHERE t.candidate_id=c.id "
               "ORDER BY t.id DESC LIMIT 1) AS rec FROM scout_candidates c WHERE 1=1"), []
    if new:
        q += " AND c.status='new'"
    if since:
        try:
            since = _parse_ts(since).isoformat(timespec="seconds")
        except ValueError:
            raise ScoutError(f"bad --since {since!r}: expected an ISO 8601 date or timestamp") from None
        q += " AND julianday(c.first_seen)>=julianday(?)"  # offsets differ across a DST switch: compare instants
        args.append(since)
    rows = con.execute(q + " ORDER BY c.id", args).fetchall()
    if rec:
        rows = [r for r in rows if r["rec"] == rec]
    if risk:
        rows = [r for r in rows if risk in (r["install_risk"] or "").split(",")]
    return rows


def set_risk(con, cid, risk, detail):
    """Store a fresh install-risk classification (detail: list of at most 20 strings)."""
    con.execute("UPDATE scout_candidates SET install_risk=?, risk_detail=? WHERE id=?",
                (risk, json.dumps(list(detail)[:20]), cid))


def set_needs_owner(con, cid, hit_lines):
    """Pre-screen hits: hold the candidate for the owner and record the hits (at most 20) in risk_detail."""
    shown = list(hit_lines)[:20]
    con.execute("UPDATE scout_candidates SET status='needs-owner', risk_detail=? WHERE id=?",
                (json.dumps(shown + ([f"... and {len(hit_lines) - 20} more"] if len(hit_lines) > 20 else [])), cid))
