"""Collecting a finished skill trial (G-009 T8b): end state, contained checker, metrics, cleanup, results (docs/scout-eval.md).

SECURITY CODE. The evaluator wrote work/ and the transcripts, so both are untrusted. check.py and the hidden tests run
agent-written code: they run ONLY through nsbox.run_contained (no network, no token, ~/.claude and ~/factory hidden),
never in this process and never outside that wrapper. Without user namespaces the trial is recorded as `error`;
there is no uncontained fallback. Nothing here executes third-party files.

bin/factory owns force_stop and the clock; they reach this module as the `api` object (api.force_stop, api.now,
optionally api.event), like scout/schedule.py.
"""
import datetime as dt
import fcntl
import json
import os
import re
import shutil
import sqlite3
import statistics
import stat
import sys
import unicodedata
from pathlib import Path

try:
    from scout import nsbox, sandbox
    from scout import vendor as scoutvendor
except ImportError:  # run as a script by path
    sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    from scout import nsbox, sandbox
    from scout import vendor as scoutvendor

CHECK_TIMEOUT_S = 60
OUTPUT_MAX = 2048
REASONS = ("timeout", "over-budget", "cancelled")
TERMINAL = ("passed", "failed", "timeout", "over-budget", "error", "cancelled", "voided")
VOIDABLE = ("passed", "failed", "timeout", "over-budget", "error")
DELETE_SANDBOX = ("passed", "failed", "cancelled")  # error / timeout / over-budget are kept for T9 to clean later


class CollectError(Exception):
    """The trial cannot be collected now; the CLI maps it to exit 2."""


def fh_db(fh):
    """The temp DB of a trial's FACTORY_HOME, located by web.dbpath.resolve(fh)."""
    from web import dbpath
    return Path(dbpath.resolve(fh))


def temp_done(fh, agent):
    """(done, ended): does the temp DB show the evaluator agent done? Any read problem means 'not done'."""
    path = fh_db(fh)
    if not path.is_file():
        return False, None
    try:
        con = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=5)
        try:
            r = con.execute("SELECT status, ended FROM agents WHERE name=?", (agent,)).fetchone()
        finally:
            con.close()
    except sqlite3.Error:
        return False, None
    return (True, r[1]) if r and r[0] == "done" else (False, None)


def _ts(s):
    try:
        t = dt.datetime.fromisoformat(s)
    except (TypeError, ValueError):
        return None
    return t.astimezone() if t.tzinfo is None else t


def wall_seconds(started, ended):
    a, b = _ts(started), _ts(ended)
    return None if a is None or b is None else max(0, round((b - a).total_seconds()))


def trial_dir(root, trial):
    """<root>/trials/trNNNN, only when it is a real directory under the root (never a symlink)."""
    d = Path(root).resolve() / "trials" / sandbox.trial_name(trial["id"])
    return d if d.is_dir() and not d.is_symlink() else None


def _clean_projects(projects):
    """The transcripts were written by the evaluator: drop anything that is not a plain file or directory (symlink,
    fifo, socket) before a parser opens it."""
    for dirpath, dirnames, filenames in os.walk(projects, followlinks=False):
        for n in dirnames + filenames:
            p = os.path.join(dirpath, n)
            m = os.lstat(p).st_mode
            if not (stat.S_ISREG(m) or stat.S_ISDIR(m)):  # lstat: a symlink is neither
                os.unlink(p)


def skill_names(root, trial):
    """(names, plugin_dir) of the candidate's vendored skills: the dir name of every SKILL.md and its `name:` line."""
    meta = scoutvendor.vendored(root, trial["candidate_id"], trial["vendor_sha"])
    if not meta:
        return set(), None
    plugin = Path(scoutvendor.vendor_dir(root, trial["candidate_id"], trial["vendor_sha"]), meta["plugin"]).resolve()
    names = set()
    for f in plugin.rglob("SKILL.md"):
        names.add(f.parent.name)
        try:
            for line in f.read_text(errors="replace").splitlines()[:12]:
                if line.startswith("name:"):
                    names.add(line.split(":", 1)[1].strip().strip("\"'"))
        except OSError:
            pass
    return names, plugin


def skill_was_invoked(projects, names, plugin):
    """1 if a transcript has a Skill tool_use naming one of `names` or a Read of one of the plugin's SKILL.md files."""
    plugin = str(plugin) if plugin else None
    for f in sorted(Path(projects).rglob("*.jsonl")):
        try:
            with open(f, "rb") as fh:
                for line in fh:
                    if b'"tool_use"' not in line:
                        continue
                    try:
                        d = json.loads(line)
                    except ValueError:
                        continue
                    msg = d.get("message") if isinstance(d, dict) else None
                    content = msg.get("content") if isinstance(msg, dict) else None
                    for b in content if isinstance(content, list) else ():
                        if not isinstance(b, dict) or b.get("type") != "tool_use":
                            continue
                        inp = b.get("input") if isinstance(b.get("input"), dict) else {}
                        if b.get("name") == "Skill":
                            sk = str(inp.get("skill") or inp.get("name") or "")
                            if sk in names or sk.rsplit(":", 1)[-1] in names:
                                return 1
                        elif b.get("name") == "Read":
                            p = str(inp.get("file_path") or "")
                            if p.endswith("SKILL.md") and (
                                    (plugin and p.startswith(plugin + "/")) or Path(p).parent.name in names):
                                return 1
        except OSError:
            continue
    return 0


def _put(work, rel, src):
    """Copy one file into work/ without following anything the evaluator planted: a symlink at the destination or on
    its parent chain is replaced, so the write cannot land outside work/."""
    dest = Path(work, rel)
    cur = Path(work)
    for part in Path(rel).parts[:-1]:
        cur = cur / part
        if cur.is_symlink() or (cur.exists() and not cur.is_dir()):
            cur.unlink()
        cur.mkdir(exist_ok=True)
    if dest.is_symlink() or dest.is_file():
        dest.unlink()
    elif dest.is_dir():
        shutil.rmtree(dest)
    shutil.copyfile(src, dest)


def stage_checker(task_dir, work):
    """hidden/ and check.py go into work/ (data copy only; nothing runs here)."""
    if os.path.islink(work) or not os.path.isdir(work):
        raise CollectError(f"{work} is not a plain directory")
    hidden = Path(task_dir, "hidden")
    if hidden.is_dir():
        for f in sorted(hidden.rglob("*")):
            if f.is_file():
                _put(work, f.relative_to(hidden), f)
    _put(work, "check.py", Path(task_dir, "check.py"))


def run_checker(d, task, *, home=None, unshare="unshare"):
    """(status, passed, output). The only place check.py runs, and only through nsbox.run_contained."""
    ok, why = nsbox.userns_available(unshare)
    if not ok:
        return "error", None, f"checker not run: user namespaces unavailable, no uncontained fallback: {why}"
    work = d / "work"
    try:
        stage_checker(sandbox.TASKS_DIR / task, work)
        res = nsbox.run_contained(str(d), [sys.executable, "check.py"], cwd=str(work),
                                  env={"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(d / "home")},
                                  timeout=CHECK_TIMEOUT_S, home=home, trials_root=str(d.parent), unshare=unshare)
    except (OSError, ValueError, CollectError) as e:
        return "error", None, f"checker not run: {e}"
    if res.timed_out:
        return "failed", 0, f"check.py exceeded {CHECK_TIMEOUT_S} s and was killed (timeout)\n{res.output}"
    return ("passed", 1, res.output) if res.returncode == 0 else ("failed", 0, res.output)


def tail(text, n=OUTPUT_MAX):
    return text.encode("utf-8", "replace")[-n:].decode("utf-8", "ignore")


def collect_trial(con, trial, root, api, reason=None, *, home=None, unshare="unshare"):
    """Collect one running trial under a per-trial lock (see _collect); a concurrent collect of the same trial is
    refused so nobody removes the sandbox under a running checker."""
    tid = int(trial["id"] if hasattr(trial, "keys") else trial)
    lockdir = Path(root).resolve() / "locks"
    lockdir.mkdir(parents=True, exist_ok=True)
    with open(lockdir / f"{sandbox.trial_name(tid)}.lock", "w") as lk:
        try:
            fcntl.flock(lk, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            raise CollectError(f"trial {tid} is being collected by another process; retry shortly") from None
        return _collect(con, tid, root, api, reason, home=home, unshare=unshare)


def _collect(con, trial, root, api, reason=None, *, home=None, unshare="unshare"):
    """Collect one running trial; returns the updated skill_trials row. A trial in a terminal status is returned
    unchanged (idempotent). Raises CollectError when it cannot be collected yet (still running, no reason)."""
    if reason is not None and reason not in REASONS:
        raise CollectError(f"reason must be one of {', '.join(REASONS)}")
    row = con.execute("SELECT * FROM skill_trials WHERE id=?", (int(trial["id"] if hasattr(trial, "keys") else trial),)).fetchone()
    if row is None:
        raise CollectError(f"no trial {trial}")
    if row["status"] in TERMINAL:
        return row
    if row["status"] != "running":
        raise CollectError(f"trial {row['id']} is {row['status']}, not running: nothing to collect")
    d = trial_dir(root, row)
    agent = row["agent"]
    live = con.execute("SELECT * FROM agents WHERE name=?", (agent,)).fetchone() if agent else None
    done, done_at = temp_done(d / "fh", agent) if d and agent else (False, None)
    lost = live is None or live["status"] != "live"
    if reason is None and not done and not lost:
        raise CollectError(f"trial {row['id']} is still running (agent {agent} live, not done in its temp DB); "
                           "pass --reason timeout|over-budget|cancelled to end it")
    if live is not None and live["status"] == "live":
        # EVERY path stops the evaluator first: a live one could rewrite work/check.py or race the staging and the
        # transcript parse. Only a confirmed stop (or an agent that finished by itself meanwhile) lets us go on.
        con.execute("UPDATE agents SET stop_requested_at=? WHERE name=? AND status='live' AND stop_requested_at IS NULL",
                    (api.now(), agent))
        seen = con.execute("SELECT stop_attempt_at FROM agents WHERE name=?", (agent,)).fetchone()[0]
        res, note = api.force_stop(con, agent, "now", attempt_seen=seen)  # the row's own mark, so a retry re-claims a failed stop
        cur = con.execute("SELECT status FROM agents WHERE name=?", (agent,)).fetchone()
        if not (res == "stopped" or (res == "skip" and cur is not None and cur["status"] != "live")):
            msg = (f"force_stop of {agent} {res}: {note or 'the evaluator may still be running'}; checker not run, "
                   "trial left running: retry collect once the agent is stopped")
            con.execute("UPDATE skill_trials SET check_output=? WHERE id=? AND status='running'", (tail(msg), row["id"]))
            con.commit()
            raise CollectError(msg)
    if d:
        nsbox.stop_proxy(str(d))
    end = api.now()
    con.execute("UPDATE agents SET status='done', ended=?, stop_requested_at=NULL, stop_attempt_at=NULL, "
                "stop_failed_kind=NULL WHERE name=? AND status='live'", (end, agent))
    status, passed, output = None, None, ""
    if d is None:
        status, output = "error", f"sandbox of trial {row['id']} is gone; nothing to check"
    elif not reason and not done:
        status, output = "error", f"agent {agent} exited without finishing (no done in its temp DB); checker not run"
    elif reason == "cancelled":
        status, passed = "cancelled", 0
    else:
        status, passed, output = run_checker(d, row["task"], home=home, unshare=unshare)
        if reason:  # timeout / over-budget: the status says why it ended; passed stays 0
            output = f"[{reason}] checker status {status}\n{output}"
            status, passed = reason, 0
    tok = {"input": None, "output": None, "cache_read": None, "cache_write": None}
    invoked = None
    projects = d / "cfg" / "projects" if d else None
    if projects and projects.is_dir() and not projects.is_symlink():
        _clean_projects(projects)
        from web import tokens  # here, not at top: the evaluator's factory snapshot ships scout/ without web/
        tok = tokens.usage_totals(projects)
        if row["condition"] == "with":
            names, plugin = skill_names(root, row)
            invoked = skill_was_invoked(projects, names, plugin)
    elif d:
        tok = {"input": 0, "output": 0, "cache_read": 0, "cache_write": 0}
    started = live["started"] if live is not None else row["started"]
    wall = wall_seconds(started, done_at if done and done_at else end)
    cur = con.execute(
        "UPDATE skill_trials SET status=?, passed=?, ended=?, wall_s=?, tokens_in=?, tokens_out=?, cache_read=?, "
        "cache_write=?, skill_invoked=?, check_output=? WHERE id=? AND status='running'",
        (status, passed, end, wall, tok["input"], tok["output"], tok["cache_read"], tok["cache_write"], invoked,
         tail(output), row["id"]))
    if cur.rowcount == 1 and d and status in DELETE_SANDBOX:
        shutil.rmtree(d, ignore_errors=True)
    if cur.rowcount == 1 and getattr(api, "event", None):
        api.event(con, "skilleval", row["candidate_id"], "collect", f"trial {row['id']} {status}")
    return con.execute("SELECT * FROM skill_trials WHERE id=?", (row["id"],)).fetchone()


# ── results ──

def _cell(v):
    return "-" if v is None else str(v)


def trial_line(r):
    tok = None if r["tokens_in"] is None and r["tokens_out"] is None else (r["tokens_in"] or 0) + (r["tokens_out"] or 0)
    return (f"{r['candidate_id']} {r['task']} {r['condition']}: trial {r['id']} status={r['status']} "
            f"passed={_cell(r['passed'])} tokens={_cell(tok)} wall_s={_cell(r['wall_s'])} "
            f"skill_invoked={_cell(r['skill_invoked'])}"
            + (f" void_reason={r['void_reason']} void_why={r['void_why']!r}" if r["status"] == "voided" else ""))


def _median(vals):
    vals = [v for v in vals if v is not None]
    return None if not vals else round(statistics.median(vals))


def summary_line(cid, rows):
    parts = []
    for cond in ("with", "without"):
        rs = [r for r in rows if r["condition"] == cond and r["status"] in TERMINAL and r["status"] != "voided"]
        passed = sum(1 for r in rs if r["status"] == "passed")
        tok = _median([None if r["tokens_in"] is None and r["tokens_out"] is None
                       else (r["tokens_in"] or 0) + (r["tokens_out"] or 0) for r in rs])
        parts.append(f"{cond}: passed {passed}/{len(rs)} tasks, median tokens {_cell(tok)}, "
                     f"median wall_s {_cell(_median([r['wall_s'] for r in rs]))}")
    parts += [f"{st} {n}" for st in ("pending", "running", "voided") if (n := sum(1 for r in rows if r["status"] == st))]
    return f"{cid} summary: " + "; ".join(parts)


START_TAIL = 20
_RC = re.compile(r"\brc=(-?\d+)")


def needs_start_log(r):
    """error / lost trials, and running ones that have burned no tokens yet (the stuck-start signature)."""
    no_tokens = r["tokens_in"] is None and r["tokens_out"] is None
    return r["status"] in ("error", "lost") or (r["status"] == "running" and no_tokens)


_CTRL = re.compile("[\x00-\x08\x0a-\x1f\x7f-\x9f\ufffd]")
# ESC, C0 except \t, DEL, C1 and U+FFFD (invalid UTF-8 decodes to it), plus every Unicode format char (category Cf:
# U+200B-U+200F, U+2060, U+FEFF, ...): the sandbox writes this log and any of them inside a token defeats the shape match


def _clean(line):
    return "".join(c for c in _CTRL.sub("", line) if unicodedata.category(c) != "Cf")


def start_log_lines(r, tail=START_TAIL):
    """Indented last rc and <= `tail` lines of <trial>/log/start.log, token-shaped strings redacted. [] when the trial
    needs none or has no regular-file log (symlinks are not followed)."""
    if not needs_start_log(r) or not r["sandbox"]:
        return []
    dfd = fd = None
    try:
        dfd = nsbox.open_log_dir(r["sandbox"])
        if dfd is None:
            return []
        fd = os.open("start.log", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=dfd)
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            return []
        size = os.fstat(fd).st_size
        raw = os.pread(fd, 64 * 1024, max(0, size - 64 * 1024))
    except OSError:
        return []
    finally:
        for f in (fd, dfd):
            if f is not None:
                os.close(f)
    # split on \n only (str.splitlines also breaks on \x0b \x0c \x1c-\x1e \x85), strip controls/format chars, and only then redact
    lines = [c for l in raw.decode("utf-8", "replace").split("\n") if (c := _clean(l)).strip()]
    if not lines:
        return []
    shown = [nsbox._TOKEN_SHAPE.sub("[redacted-token]", l)[:300] for l in lines[-tail:]]
    out = ["  start.log:"] + ["    " + l for l in shown]
    if not any(_RC.search(l) for l in shown):
        rcs = [m.group(1) for l in lines if (m := _RC.search(l))]
        if rcs:
            out.insert(1, f"    last rc={rcs[-1]}")
    return out


def list_lines(con, candidate=None):
    return result_lines(con, candidate)


def result_lines(con, candidate=None):
    q, args = "SELECT * FROM skill_trials", ()
    if candidate:
        q, args = q + " WHERE candidate_id=?", (candidate,)
    rows = con.execute(q + " ORDER BY candidate_id, task, condition", args).fetchall()
    out = []
    for cid in sorted({r["candidate_id"] for r in rows}):
        mine = [r for r in rows if r["candidate_id"] == cid]
        for r in mine:
            out += [trial_line(r)] + start_log_lines(r)
        out.append(summary_line(cid, mine))
    return out


# ── queue, cancel, tick (G-009 T9) ──

_TRIAL_DIR = re.compile(r"tr\d+")


def task_names():
    return sorted(p.name for p in sandbox.TASKS_DIR.iterdir() if (p / "meta.json").is_file())


def queue_trials(con, cid, sha, tasks, author, stamp):
    """Insert pending with/without trials for each task (a task's two conditions back to back). Returns (new, all):
    the UNIQUE(candidate, sha, task, condition) key makes a repeat a no-op, so `new` is empty then."""
    new = []
    for task in tasks:
        for cond in ("with", "without"):
            cur = con.execute("INSERT OR IGNORE INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,author) "
                              "VALUES(?,?,?,?,'pending',?,?)", (cid, sha, task, cond, stamp, author))
            if cur.rowcount == 1:
                new.append(cur.lastrowid)
    if new:
        con.execute("UPDATE scout_candidates SET status='queued-eval' WHERE id=?", (cid,))
    rows = con.execute("SELECT * FROM skill_trials WHERE candidate_id=? AND vendor_sha=? ORDER BY id", (cid, sha)).fetchall()
    return new, rows


def transition(con, tid, to, frm, **cols):
    """Atomically move trial `tid` from status `frm` (a status or tuple) to `to`: BEGIN IMMEDIATE, then an UPDATE
    guarded by the old status. True only for the one caller whose UPDATE changed the row; the loser must back off."""
    frms = (frm,) if isinstance(frm, str) else tuple(frm)
    sets = ", ".join(["status=?"] + [f"{k}=?" for k in cols])
    marks = ",".join("?" * len(frms))
    if con.in_transaction:  # a legacy-mode connection (tests) may hold an implicit one; the factory DB is autocommit
        con.commit()
    con.execute("BEGIN IMMEDIATE")
    try:
        cur = con.execute(f"UPDATE skill_trials SET {sets} WHERE id=? AND status IN ({marks})",
                          (to, *cols.values(), tid, *frms))
        won = cur.rowcount == 1
    except BaseException:
        con.execute("ROLLBACK")
        raise
    con.execute("COMMIT")
    return won


def claim(con, tid, stamp):
    """pending -> starting, atomically: the only way a trial begins to start (tick and `skilleval try` alike)."""
    return transition(con, tid, "starting", "pending", started=stamp)


def cancel(con, target, root, api, *, home=None, unshare="unshare"):
    """Cancel one trial (id or trNNNN) or every open trial of a candidate (C-xxxx): pending ones become cancelled,
    running ones are stopped through collect_trial(reason cancelled). Returns (lines, errors)."""
    m = re.fullmatch(r"(?:tr0*)?(\d+)", target or "")
    if m:
        rows = con.execute("SELECT * FROM skill_trials WHERE id=?", (int(m.group(1)),)).fetchall()
    elif re.fullmatch(r"C-\d+", target or ""):
        rows = con.execute("SELECT * FROM skill_trials WHERE candidate_id=? ORDER BY id", (target,)).fetchall()
    else:
        raise CollectError("usage: factory skilleval cancel <trial id|trNNNN|C-xxxx>")
    if not rows:
        raise CollectError(f"nothing to cancel: no trial or candidate {target}")
    lines, errors = [], []
    for r in rows:
        if r["status"] == "pending":
            if not transition(con, r["id"], "cancelled", "pending", passed=0, ended=api.now()):
                errors.append(f"trial {r['id']}: was claimed for start meanwhile; retry the cancel")
                continue
            lines.append(trial_line(con.execute("SELECT * FROM skill_trials WHERE id=?", (r["id"],)).fetchone()))
        elif r["status"] == "running":
            try:
                lines.append(trial_line(collect_trial(con, r["id"], root, api, "cancelled", home=home, unshare=unshare)))
            except CollectError as e:
                errors.append(f"trial {r['id']}: {e}")
        elif r["status"] == "starting":
            errors.append(f"trial {r['id']}: is starting right now; retry the cancel in a minute")
        # a terminal trial is left as it is
    return lines, errors


def void(con, target, reason, why):
    """Void one finished trial (id or trNNNN) whose result says nothing about the skill (reason infra): status becomes
    voided, every recorded field is kept, summaries ignore it and `try` may re-run it. Returns the row plus 'prev'."""
    m = re.fullmatch(r"(?:tr0*)?(\d+)", target or "")
    if not m:
        raise CollectError("usage: factory skilleval void <trial id|trNNNN> --reason infra --why <text>")
    if reason != "infra":
        raise CollectError("void needs --reason infra")
    r = con.execute("SELECT * FROM skill_trials WHERE id=?", (int(m.group(1)),)).fetchone()
    if r is None:
        raise CollectError(f"no trial {target}")
    if r["status"] not in VOIDABLE:
        hint = "use `factory skilleval cancel`" if r["status"] in ("pending", "running", "starting") else "nothing to do"
        raise CollectError(f"trial {r['id']} is {r['status']}: only a finished trial ({', '.join(VOIDABLE)}) can be voided; {hint}")
    if not transition(con, r["id"], "voided", r["status"], void_reason=reason, void_why=why):
        raise CollectError(f"trial {r['id']} changed meanwhile; look at `factory skilleval list` and retry")
    out = dict(con.execute("SELECT * FROM skill_trials WHERE id=?", (r["id"],)).fetchone())
    out["prev"] = r["status"]
    return out


def _tokens(d):
    """input + output + cache creation of a trial's transcripts (the evaluator wrote them: plain files only)."""
    projects = d / "cfg" / "projects" if d else None
    if not projects or not projects.is_dir() or projects.is_symlink():
        return 0
    _clean_projects(projects)
    from web import tokens  # here, not at top: the evaluator's factory snapshot ships scout/ without web/
    t = tokens.usage_totals(projects)
    return (t["input"] or 0) + (t["output"] or 0) + (t["cache_write"] or 0)


def _collect_running(con, api, root, clock):
    minutes = int(api.cfg(con, "skilleval_trial_minutes"))
    cap = int(api.cfg(con, "skilleval_trial_max_tokens"))
    for r in con.execute("SELECT * FROM skill_trials WHERE status='running' ORDER BY id").fetchall():
        d = trial_dir(root, r)
        agent = r["agent"]
        live = con.execute("SELECT status, started FROM agents WHERE name=?", (agent,)).fetchone() if agent else None
        done = temp_done(d / "fh", agent)[0] if d and agent else False
        reason = None
        if not done and live is not None and live["status"] in ("live", "queued", "starting"):
            if live["status"] != "live":
                continue  # waiting at the gate: no clock, no tokens yet
            started = _ts(live["started"]) or _ts(r["started"])  # the wall runs from the agent going live, not from the gate queue
            if started and (clock - started).total_seconds() > minutes * 60:
                reason = "timeout"
            elif _tokens(d) > cap:
                reason = "over-budget"
            else:
                continue
        try:
            collect_trial(con, r["id"], root, api, reason, home=api.home, unshare=api.unshare)
        except CollectError as e:  # e.g. the stop failed: the trial stays running and is retried next tick
            api.event(con, "skilleval", r["candidate_id"], "collect-retry", f"trial {r['id']}: {str(e)[:300]}")


STARTING_MAX_MIN = 15  # build_sandbox + preflight take seconds; a longer 'starting' means its process died


def _reap_starting(con, api, clock):
    for r in con.execute("SELECT * FROM skill_trials WHERE status='starting'").fetchall():
        t = _ts(r["started"])
        if t is not None and (clock - t).total_seconds() > STARTING_MAX_MIN * 60:
            _fail_start(con, api, r, "start-failed", f"stuck in starting for over {STARTING_MAX_MIN} min; its process died")


def _started_today(con, clock):
    day = clock.date()
    n = 0
    for (s,) in con.execute("SELECT started FROM skill_trials WHERE started IS NOT NULL"):
        t = _ts(s)
        if t is not None and t.astimezone(clock.tzinfo).date() == day:
            n += 1
    return n


def _start_next(con, api, root, clock):
    if con.execute("SELECT count(*) FROM skill_trials WHERE status IN ('running','starting')").fetchone()[0] \
            >= int(api.cfg(con, "skilleval_max_concurrent")):
        return
    if _started_today(con, clock) >= int(api.cfg(con, "skilleval_max_trials_per_day")):
        return
    row = con.execute("SELECT * FROM skill_trials WHERE status='pending' ORDER BY id LIMIT 1").fetchone()
    if row is None:
        return
    try:  # a global blocker (no token, no namespaces) leaves the queue untouched instead of erroring every trial
        sandbox.check_token(f"{api.home}/.config/factory/eval-oauth-token")
        sandbox.check_userns(api.unshare)
    except sandbox.SandboxRefused as e:
        last = con.execute("SELECT action, detail FROM events WHERE action IN ('skilleval-blocked','try') AND entity='skilleval' "
                           "ORDER BY id DESC LIMIT 1").fetchone()
        if last is None or last[0] != "skilleval-blocked" or last[1] != str(e)[:300]:  # the tick runs every minute: say it once per distinct reason
            api.event(con, "skilleval", "-", "skilleval-blocked", str(e)[:300])
        return
    stamp = clock.isoformat(timespec="seconds")  # the tick clock owns the day count
    if not claim(con, row["id"], stamp):
        return  # `skilleval try` or another tick took it, or it was cancelled
    try:
        api.launch(con, row, root, api.home, api.unshare)
    except sandbox.SandboxRefused as e:
        _fail_start(con, api, row, f"start-refused", str(e))
        return
    except SystemExit:  # spawn refused: launch already set the row to error
        api.event(con, "skilleval", row["candidate_id"], "start-refused", f"trial {row['id']}: spawn refused")
        return
    except Exception as e:  # e.g. OSError in build_sandbox: error the row so the queue is not blocked behind it
        _fail_start(con, api, row, "start-failed", f"{type(e).__name__}: {e}")
        return
    con.execute("UPDATE skill_trials SET started=? WHERE id=? AND status='running'", (stamp, row["id"]))


def _fail_start(con, api, row, action, msg):
    con.execute("UPDATE skill_trials SET status='error', check_output=? WHERE id=? AND status='starting'",
                (msg[:400], row["id"]))
    api.event(con, "skilleval", row["candidate_id"], action, f"trial {row['id']}: {msg[:300]}")


def _cleanup(con, root, clock, keep_h):
    trials = Path(root).resolve() / "trials"
    if not trials.is_dir():
        return
    for r in con.execute("SELECT * FROM skill_trials WHERE status IN ('error','timeout','over-budget','voided')").fetchall():
        ended = _ts(r["ended"])
        d = trial_dir(root, r)
        if d and ended and (clock - ended).total_seconds() > keep_h * 3600:
            shutil.rmtree(d, ignore_errors=True)
    known = {sandbox.trial_name(r[0]) for r in con.execute("SELECT id FROM skill_trials")}
    for d in trials.iterdir():
        if _TRIAL_DIR.fullmatch(d.name) and d.name not in known and d.is_dir() and not d.is_symlink():
            shutil.rmtree(d, ignore_errors=True)


def _evaluated(con):
    marks = ",".join("?" * len(TERMINAL))
    for (cid,) in con.execute("SELECT id FROM scout_candidates WHERE status='queued-eval'").fetchall():
        n_open = con.execute(f"SELECT count(*) FROM skill_trials WHERE candidate_id=? AND status NOT IN ({marks})",
                             (cid, *TERMINAL)).fetchone()[0]
        if not n_open and con.execute("SELECT 1 FROM skill_trials WHERE candidate_id=?", (cid,)).fetchone():
            con.execute("UPDATE scout_candidates SET status='evaluated' WHERE id=? AND status='queued-eval'", (cid,))


def tick(con, api):
    """One scheduling pass (the cmd_tick step, docs/scout-eval.md): collect finished or over-budget trials, start at
    most one pending trial under the caps, clean old sandboxes, mark fully terminal candidates evaluated."""
    clock = api.clock()
    root = scoutvendor.resolve_root(api.cfg(con, "skilleval_root"), api.home_dir)
    _collect_running(con, api, root, clock)
    _reap_starting(con, api, clock)
    _start_next(con, api, root, clock)
    _cleanup(con, root, clock, int(api.cfg(con, "skilleval_keep_failed_h")))
    _evaluated(con)
