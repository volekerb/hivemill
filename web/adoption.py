"""Adoption metrics: how much the factory spends per merged PR and how its agents use Claude Code features,
measured from Claude Code transcripts (~/.claude/projects/**/*.jsonl) and attributed through the factory DB.
Read-only, stdlib only. See docs/adoption-metrics.md.

    python3 -m web.adoption report [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--json]

A task counts when its latest judge verdict is "merge" and that verdict's local day is in the window. Missing data is
the string "unknown", never 0. Only usage numbers, tool names, a few tool inputs (command text is matched, never kept)
and result sizes are read out of a line; message text and tool output are never kept or printed.
"""
import argparse
import json
import re
import sqlite3
import subprocess
import sys
from pathlib import Path

from web.context import _day, attribution, connect_ro, context_of, db_path, is_hq, projects_dir
from web.prices import cost
from web.tokens import TASK_RE, _int, _parse_ts

UNKNOWN = "unknown"
ROLES = ("coder", "reviewer", "qa", "judge", "other")
COST_ROLES = ("cleaner", "scout")
GATE_PATHS = ("roles/hooks/", "roles/settings/", "ci/", ".github/")
GATE_LINES = 500
SEARCH_TOOLS = ("Read", "Grep", "Glob")
BASH_TOOLS = ("Bash", "bash")
BASH_SEARCH = ("grep", "egrep", "fgrep", "rg", "find", "ls", "cat", "head", "tail", "sed")
SESSION_ROLES = ("coder", "reviewer", "qa", "judge", "cleaner", "scout", "pm")
GH_TIMEOUT = 30


def median(vals):
    vals = sorted(v for v in vals if isinstance(v, (int, float)) and not isinstance(v, bool))
    n = len(vals)
    if not n:
        return UNKNOWN
    return vals[n // 2] if n % 2 else (vals[n // 2 - 1] + vals[n // 2]) // 2


def _add(a, b):
    """Sum where "unknown" is absorbed by any known number: unknown only when nothing is known."""
    if a == UNKNOWN:
        return b
    if b == UNKNOWN:
        return a
    return a + b


class _Sess:
    __slots__ = ("lines", "ctx", "sub_by_tool", "wf_ids", "turns", "create_turn", "done_turn", "ci", "ci_bg",
                 "explore", "workflow", "search_n", "search_chars", "models", "n_cost", "first_ts", "done_any")

    def __init__(self):
        self.lines = 0            # deduped main-transcript requests with usage
        self.ctx = 0
        self.sub_by_tool = {}     # meta.json toolUseId (None: no meta) -> tokens in that <session>/subagents file
        self.wf_ids = set()       # tool_use ids of the Workflow calls in the main transcript
        self.turns = 0
        self.create_turn = None
        self.done_turn = None
        self.ci = self.ci_bg = self.explore = self.workflow = 0
        self.search_n = self.search_chars = 0
        self.models = {}          # model -> {price field: tokens}; only kept for cleaner/scout sessions
        self.n_cost = 0
        self.first_ts = None      # earliest request time, orders a task's sessions
        self.done_any = None      # turn of the first `factory done`, whether or not a PR was created before it


def _result_chars(c):
    if isinstance(c, str):
        return len(c)
    if isinstance(c, list):
        return sum(len(b.get("text", "")) for b in c if isinstance(b, dict) and isinstance(b.get("text"), str))
    return 0


def _buckets(u):
    cc = u.get("cache_creation") if isinstance(u.get("cache_creation"), dict) else None
    if cc is not None:
        w5, w1 = _int(cc.get("ephemeral_5m_input_tokens")), _int(cc.get("ephemeral_1h_input_tokens"))
    else:
        w5, w1 = _int(u.get("cache_creation_input_tokens")), 0
    return {"input": _int(u.get("input_tokens")), "output": _int(u.get("output_tokens")),
            "cache_read": _int(u.get("cache_read_input_tokens")), "cache_write_5m": w5, "cache_write_1h": w1}


def bash_search(cmd):
    """True when a Bash command reads files to look something up: a grep/find/ls/cat/head/tail/sed command that starts
    a command list (`&&`, `||`, `;`, newline) or `cd` prefix. Pipe filters (`... | tail`), heredoc/redirect writes
    and `sed -i` are not searches. The command text is only matched, never kept."""
    for seg in re.split(r"&&|\|\||;|\n", cmd):
        words = seg.split("|")[0].split()
        while words and (words[0] == "cd" or re.match(r"[A-Za-z_]\w*=", words[0])):
            words = words[2:] if words[0] == "cd" else words[1:]
        if not words:
            continue
        w0 = words[0].rsplit("/", 1)[-1]
        if w0 == "git" and len(words) > 1 and words[1] in ("grep", "ls-files"):
            return True
        if w0 not in BASH_SEARCH:
            continue
        if w0 == "cat" and (">" in seg or "<<" in seg):
            continue
        if w0 == "sed" and any(a.startswith("-") and not a.startswith("--") and "i" in a for a in words[1:]):
            continue
        return True
    return False


def session_role(name, names):
    """Role of an agent from its name: the agents table first, then a role word inside the name."""
    if name in names:
        return names[name]
    for part in (name or "").split("-"):
        if part in SESSION_ROLES:
            return part
    return "other"


def _sub_tool_id(path):
    """toolUseId from the sibling <agent>.meta.json of a sub-agent transcript, None when absent or unreadable."""
    try:
        meta = json.loads(path.with_suffix(".meta.json").read_text())
    except (OSError, ValueError):
        return None
    tid = meta.get("toolUseId") if isinstance(meta, dict) else None
    return tid if isinstance(tid, str) else None


def scan(root, attr, since=None, until=None):
    """Stream every transcript once. Returns ({session: _Sess}, counters).

    A session the agents table and the tick events do not know (a retry/respawn whose row was replaced) is attributed
    as web.context per-pr does: its cwd names the task worktree. Its role comes from the transcript's own
    `agent-name` record. Those sessions are added to `attr` (mutated). Sessions with neither are skipped and counted.
    """
    names = {a["agent"]: a["role"] for a in attr.values()}
    sess, seen_usage, seen_tool = {}, set(), set()
    fb_task, fb_name = {}, {}
    c = {"files": 0, "malformed_lines": 0, "duplicate_requests": 0, "unattributed_sessions": 0}
    unattributed = set()
    files = sorted(Path(root).rglob("*.jsonl")) if Path(root).is_dir() else []
    for path in files:
        c["files"] += 1
        sub = "subagents" in path.parts
        psid = path.parent.parent.name if sub else path.stem
        read_ids = {}             # tool_use id -> tool name, for Read/Grep/Glob results in this file
        sub_tool = _sub_tool_id(path) if sub else None
        try:
            f = open(path, "rb")
        except OSError:
            c["malformed_lines"] += 1
            continue
        with f:
            for raw in f:
                raw = raw.strip()
                if not raw:
                    continue
                if not (raw.startswith(b"{") and raw.endswith(b"}")):
                    c["malformed_lines"] += 1
                    continue
                if (b'"usage"' not in raw and b'"tool_use"' not in raw and b'"tool_result"' not in raw
                        and b'"agent-name"' not in raw):
                    continue
                try:
                    d = json.loads(raw)
                except ValueError:
                    c["malformed_lines"] += 1
                    continue
                if not isinstance(d, dict):
                    continue
                sid = psid if sub else (d["sessionId"] if isinstance(d.get("sessionId"), str) else psid)
                if d.get("type") == "agent-name":
                    if isinstance(d.get("agentName"), str):
                        fb_name.setdefault(sid, d["agentName"])
                    continue
                a = attr.get(sid)
                if a is None and sid not in fb_task and isinstance(d.get("cwd"), str):
                    m = TASK_RE.search(d["cwd"])
                    fb_task[sid] = m.group(2) if m else None
                if a is None and fb_task.get(sid):
                    name = fb_name.get(sid)
                    a = attr[sid] = {"agent": name, "role": session_role(name, names), "task": fb_task[sid]}
                if a is None:
                    unattributed.add(sid)
                    continue
                s = sess.get(sid)
                if s is None:
                    s = sess[sid] = _Sess()
                side = sub or d.get("isSidechain") is True
                msg = d.get("message") if isinstance(d.get("message"), dict) else {}
                content = msg.get("content") if isinstance(msg.get("content"), list) else []
                if d.get("type") == "assistant":
                    _assistant(d, msg, content, a, s, side, sub_tool, seen_usage, seen_tool, read_ids, since, until, c)
                elif d.get("type") == "user" and not side:
                    for b in content:
                        if isinstance(b, dict) and b.get("type") == "tool_result" and b.get("tool_use_id") in read_ids:
                            s.search_n += 1
                            s.search_chars += _result_chars(b.get("content"))
    c["unattributed_sessions"] = len(unattributed)
    return sess, c


def _assistant(d, msg, content, a, s, side, sub_tool, seen_usage, seen_tool, read_ids, since, until, c):
    mid, rid = msg.get("id"), d.get("requestId")
    key = (mid, rid) if mid and rid else d.get("uuid")
    first_sight = key is not None and key not in seen_usage
    u = msg.get("usage")
    if isinstance(u, dict) and key is not None:
        if not first_sight:
            c["duplicate_requests"] += 1
        else:
            seen_usage.add(key)
            ctx = context_of(u)
            t0 = _parse_ts(d.get("timestamp"))
            if t0 and not side and (s.first_ts is None or t0 < s.first_ts):
                s.first_ts = t0
            if side:
                if sub_tool is not None:      # sidechain lines and files without a toolUseId link to no call
                    s.sub_by_tool[sub_tool] = s.sub_by_tool.get(sub_tool, 0) + ctx
            else:
                s.lines += 1
                s.ctx += ctx
                s.turns += 1
            if a["role"] in COST_ROLES:
                t = _parse_ts(d.get("timestamp"))
                day = t.astimezone().date().isoformat() if t else None
                if day and (not since or day >= since) and (not until or day <= until):
                    m = s.models.setdefault(msg.get("model") if isinstance(msg.get("model"), str) else None,
                                            dict.fromkeys(("input", "output", "cache_read", "cache_write_5m",
                                                           "cache_write_1h"), 0))
                    for k, v in _buckets(u).items():
                        m[k] += v
                    s.n_cost += 1
    if side:
        return
    for b in content:
        if not (isinstance(b, dict) and b.get("type") == "tool_use"):
            continue
        tid = b.get("id")
        if tid is not None:
            if tid in seen_tool:
                continue
            seen_tool.add(tid)
        name, inp = b.get("name"), b.get("input") if isinstance(b.get("input"), dict) else {}
        if name in SEARCH_TOOLS and tid is not None:
            read_ids[tid] = name
        elif name == "Workflow":
            s.workflow += 1
            if tid is not None:
                s.wf_ids.add(tid)
        elif name in ("Agent", "Task") and inp.get("subagent_type") == "Explore":
            s.explore += 1
        elif name in BASH_TOOLS:
            cmd = inp.get("command") if isinstance(inp.get("command"), str) else ""
            if tid is not None and a["role"] == "coder" and bash_search(cmd):
                read_ids[tid] = name
            if "gh pr checks" in cmd:
                s.ci += 1
                s.ci_bg += inp.get("run_in_background") is True
            if "gh pr create" in cmd and s.create_turn is None:
                s.create_turn = s.turns
            if "factory done" in cmd:
                if s.done_any is None:
                    s.done_any = s.turns
                if s.create_turn is not None and s.done_turn is None:
                    s.done_turn = s.turns


def scope_gate(files, additions, deletions):
    """(gated, reasons) from a PR's changed files and line counts."""
    reasons = []
    for p in files:
        for g in GATE_PATHS:
            if p.startswith(g):
                reasons.append(p)
                break
    if additions + deletions > GATE_LINES:
        reasons.append(f"{additions + deletions} changed lines")
    return bool(reasons), reasons


def pr_gate(url, cache):
    """Scope gate for a PR via `gh pr view`; any failure is "unknown" (cached per PR, never raises)."""
    if url in cache:
        return cache[url]
    out = {"gated": UNKNOWN, "reasons": [], "changed_lines": UNKNOWN}
    try:
        p = subprocess.run(["gh", "pr", "view", url, "--json", "files,additions,deletions"],
                           capture_output=True, text=True, timeout=GH_TIMEOUT)
        if p.returncode == 0:
            j = json.loads(p.stdout)
            paths = [f["path"] for f in j["files"]]
            add, dele = j["additions"], j["deletions"]
            if not isinstance(add, int) or not isinstance(dele, int):
                raise ValueError("additions/deletions are not numbers")
            gated, reasons = scope_gate(paths, add, dele)
            out = {"gated": gated, "reasons": reasons, "changed_lines": add + dele}
    except (OSError, subprocess.SubprocessError, ValueError, KeyError, TypeError):
        pass
    cache[url] = out
    return out


def merged_tasks(con, since, until):
    """[(task row, merge day)] for tasks whose latest judge verdict is merge, merge day (local) inside the window."""
    out = []
    rows = con.execute("SELECT v.task_id, v.verdict, v.created, t.pr_url, t.goal_id FROM verdicts v "
                       "JOIN tasks t ON t.id=v.task_id WHERE v.role='judge' ORDER BY v.id").fetchall()
    last = {}
    for r in rows:
        last[r["task_id"]] = r
    for tid, r in sorted(last.items()):
        t = _parse_ts(r["created"])
        if r["verdict"] != "merge" or t is None:
            continue
        day = t.astimezone().date().isoformat()
        if (not since or day >= since) and (not until or day <= until):
            out.append((r, day))
    return out


def _role_bucket(role):
    return role if role in ROLES[:4] else "other"


def _turns_create_to_done(coders):
    """Assistant turns strictly between the first `gh pr create` and the first later `factory done` over a task's
    coder sessions in time order (the PR may be created in one session and the coder resumed or respawned in the
    next). "unknown" when no session created a PR, or no `factory done` follows it in any session with a
    transcript: never 0."""
    for i, s in enumerate(coders):
        if s.create_turn is None:
            continue
        if s.done_turn is not None:
            return max(0, s.done_turn - s.create_turn - 1)
        n = s.turns - s.create_turn
        for later in coders[i + 1:]:
            if later.done_any is not None:
                return max(0, n + later.done_any - 1)
            n += later.turns
        return UNKNOWN
    return UNKNOWN


def _task_row(r, day, agents_by_task, sess, gh_cache):
    tokens = dict.fromkeys(ROLES, UNKNOWN)
    sub_tokens = {}
    coder = {k: UNKNOWN for k in ("ci_checks_calls", "ci_checks_background", "turns_pr_create_to_done",
                                  "explore_calls", "search_results", "search_result_chars")}
    rev_wf, rev_sub, rev_files = UNKNOWN, UNKNOWN, 0
    coders = []
    for a in agents_by_task.get(r["task_id"], []):
        s = sess.get(a["session"])
        if s is None or not s.lines:
            continue
        b = _role_bucket(a["role"])
        tokens[b] = _add(tokens[b], s.ctx)
        if a["role"] == "coder":
            coder["ci_checks_calls"] = _add(coder["ci_checks_calls"], s.ci)
            coder["ci_checks_background"] = _add(coder["ci_checks_background"], s.ci_bg)
            coder["explore_calls"] = _add(coder["explore_calls"], s.explore)
            coder["search_results"] = _add(coder["search_results"], s.search_n)
            coder["search_result_chars"] = _add(coder["search_result_chars"], s.search_chars)
            coders.append((s.first_ts is None, s.first_ts, a["session"], s))
        elif a["role"] == "reviewer":
            rev_wf = _add(rev_wf, s.workflow)
            matched = [v for k, v in s.sub_by_tool.items() if k in s.wf_ids]
            rev_files += len(matched)
            rev_sub = _add(rev_sub, sum(matched)) if matched else rev_sub
    coder["turns_pr_create_to_done"] = _turns_create_to_done([x[3] for x in sorted(coders, key=lambda x: x[:3])])
    known = [v for v in tokens.values() if v != UNKNOWN]
    tokens["total"] = sum(known) if known else UNKNOWN
    tokens["complete"] = all(tokens[k] != UNKNOWN for k in ROLES[:4])
    if rev_wf == UNKNOWN:
        wf_sub = UNKNOWN
    elif rev_wf == 0:
        wf_sub = 0
    else:
        wf_sub = rev_sub if rev_files else "not logged"
    gate = pr_gate(r["pr_url"], gh_cache) if r["pr_url"] else {"gated": UNKNOWN, "reasons": [], "changed_lines": UNKNOWN}
    return {"task": r["task_id"], "goal": r["goal_id"], "pr_url": r["pr_url"] or UNKNOWN, "merged_day": day,
            "tokens": tokens,
            "reviewer": {"tokens": tokens["reviewer"], "workflow_calls": rev_wf,
                         "workflow_subagent_tokens": wf_sub, "scope_gate": gate},
            "coder": coder}


def session_costs(sess, attr, since, until):
    out = []
    for sid, s in sorted(sess.items()):
        a = attr[sid]
        if a["role"] not in COST_ROLES or not s.n_cost:
            continue
        total, unknown_models = 0.0, []
        for model, buckets in s.models.items():
            c = cost(model, buckets) if model else None
            if c is None:
                unknown_models.append(model or UNKNOWN)
            else:
                total += c
        out.append({"agent": a["agent"], "role": a["role"], "session": sid, "requests": s.n_cost,
                    "models": sorted(m or UNKNOWN for m in s.models),
                    "usd": UNKNOWN if unknown_models or not s.models else round(total, 4),
                    "unpriced_models": sorted(set(unknown_models))})
    return out


def report(root, con, since=None, until=None, gh_cache=None):
    attr = attribution(con)
    sess, c = scan(root, attr, since, until)
    agents_by_task = {}
    for sid, a in attr.items():
        if a["task"] and not is_hq(a["role"], a["agent"]):
            agents_by_task.setdefault(a["task"], []).append({**a, "session": sid})
    cache = {} if gh_cache is None else gh_cache
    rows = [_task_row(r, day, agents_by_task, sess, cache) for r, day in merged_tasks(con, since, until)]
    med = {
        "tokens_per_merged_pr": {k: median([x["tokens"][k] for x in rows]) for k in (*ROLES, "total")},
        "reviewer_workflow_calls": median([x["reviewer"]["workflow_calls"] for x in rows]),
        "coder": {k: median([x["coder"][k] for x in rows]) for k in rows[0]["coder"]} if rows else {},
    }
    gated = [x["reviewer"]["scope_gate"]["gated"] for x in rows]
    return {"window": {"since": since, "until": until, "merged_tasks": len(rows), **c},
            "scope_gate_summary": {"gated": gated.count(True), "not_gated": gated.count(False),
                                   "unknown": gated.count(UNKNOWN)},
            "tasks": rows, "medians": med, "cost_sessions": session_costs(sess, attr, since, until)}


def _fmt(v):
    return f"{v:,}" if isinstance(v, int) and not isinstance(v, bool) else str(v)


def render(rep):
    w = rep["window"]
    out = [f"window: {w['since'] or 'start'} .. {w['until'] or 'now'}",
           f"  merged tasks: {w['merged_tasks']}, transcript files read: {w['files']}, "
           f"unattributed sessions skipped: {w['unattributed_sessions']}",
           f"  duplicate requests: {w['duplicate_requests']}, malformed lines: {w['malformed_lines']}",
           f"  scope gate: {rep['scope_gate_summary']}", "",
           "1. tokens per merged PR (input + cache_read + cache_write; main transcripts only)"]
    out.append(f"  {'task':<8}{'total':>14}{'coder':>14}{'reviewer':>14}{'qa':>14}{'judge':>14}{'other':>14}")
    for t in rep["tasks"]:
        k = t["tokens"]
        out.append(f"  {t['task']:<8}" + "".join(f"{_fmt(k[r]):>14}" for r in ("total", *ROLES)))
    m = rep["medians"]["tokens_per_merged_pr"]
    out.append(f"  {'median':<8}" + "".join(f"{_fmt(m[r]):>14}" for r in ("total", *ROLES)))
    out += ["", "2. reviewer", f"  {'task':<8}{'tokens':>14}{'workflow':>10}{'wf sub-agent tokens':>22}  scope gate"]
    for t in rep["tasks"]:
        r = t["reviewer"]
        g = r["scope_gate"]
        out.append(f"  {t['task']:<8}{_fmt(r['tokens']):>14}{_fmt(r['workflow_calls']):>10}"
                   f"{_fmt(r['workflow_subagent_tokens']):>22}  {g['gated']} {', '.join(g['reasons'])}")
    out += ["", "3./4. coder: CI wait and search"]
    keys = list(rep["tasks"][0]["coder"]) if rep["tasks"] else []
    out.append(f"  {'task':<8}" + "".join(f"{k[:14]:>16}" for k in keys))
    for t in rep["tasks"]:
        out.append(f"  {t['task']:<8}" + "".join(f"{_fmt(t['coder'][k]):>16}" for k in keys))
    if keys:
        out.append(f"  {'median':<8}" + "".join(f"{_fmt(rep['medians']['coder'][k]):>16}" for k in keys))
    out += ["", "5. cleaner / scout session cost (USD)"]
    for s in rep["cost_sessions"]:
        out.append(f"  {s['agent']} ({s['role']}) {s['session']}: requests={s['requests']} "
                   f"models={','.join(s['models'])} usd={_fmt(s['usd'])}")
    return "\n".join(out) + "\n"


def main(argv=None):
    ap = argparse.ArgumentParser(prog="python3 -m web.adoption", description=__doc__.split("\n\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("report", help="tokens per merged PR, reviewer/coder behaviour, cleaner/scout cost")
    r.add_argument("--since", type=_day, metavar="YYYY-MM-DD", help="first local day to include")
    r.add_argument("--until", type=_day, metavar="YYYY-MM-DD", help="last local day to include")
    r.add_argument("--json", action="store_true", help="one JSON object instead of text")
    args = ap.parse_args(argv)
    try:
        con = connect_ro(db_path())
    except (FileNotFoundError, sqlite3.Error) as e:
        print(f"web.adoption: {e}", file=sys.stderr)
        return 2
    try:
        rep = report(projects_dir(), con, args.since, args.until)
    finally:
        con.close()
    sys.stdout.write(json.dumps(rep, indent=1) + "\n" if args.json else render(rep))
    return 0


if __name__ == "__main__":
    sys.exit(main())
