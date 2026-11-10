#!/usr/bin/env python3
"""Audit Cursor sessions: which ran without policy hooks, and did they try sudo/herdr.

Run as ``python3 -I ci/cursor_session_audit.py [--since YYYY-MM-DD] [--json]``.

Inputs (explicit globs only, never a recursive walk of HOME):
  HOME/.cursor/projects/*/agent-transcripts/ID/ID.jsonl
  HOME/.cursor/chats/*/ID/meta.json      (createdAtMs and cwd only)
  STATE/factory/cursor/*/hook-events.jsonl   (when present; STATE = XDG_STATE_HOME
      else HOME/.local/state). Each line is a JSON object; a line counts for a
      session when one of conversation_id/session_id/sessionId equals the session id,
      it is a beforeShellExecution line when event/hook_event_name/hook says so, and
      a deny when decision/permission/action is "deny".

Per session the output names project key, session id, first-seen time, agent name,
hooks_loaded (yes/NO/unknown) and the sudo/herdr attempts (first 80 chars of the
command). Transcripts hold no tool results, so an attempt reads
"attempted; outcome not recorded" unless hook-events shows a deny ("blocked").
No message text is printed beyond that 80-char excerpt and the agent name.
"""
import argparse
import datetime
import glob
import json
import os
import re
import sys

EXCERPT = 80
AGENT_RE = re.compile(r"You are `([^`]+)`")
WORD_RE = re.compile(r"(?<![\w-])(sudo|herdr)(?![\w-])")
NOT_RECORDED = "attempted; outcome not recorded"


def home_dir():
    return os.environ.get("HOME") or os.path.expanduser("~")


def state_dir(home):
    return os.environ.get("XDG_STATE_HOME") or os.path.join(home, ".local", "state")


def resolve_key(key):
    """Map a project key (path with '/' turned into '-') back to an existing dir.

    Returns None when no existing directory matches; never invents a path.
    """
    parts = key.split("-")

    def go(base, i):
        if i == len(parts):
            return base
        for j in range(i + 1, len(parts) + 1):
            cand = os.path.join(base, "-".join(parts[i:j]))
            if os.path.isdir(cand):
                found = go(cand, j)
                if found:
                    return found
        return None

    return go("/", 0)


def load_hook_events(home):
    """session id -> {"shell": bool, "deny": bool}. Streams each file."""
    out = {}
    pattern = os.path.join(state_dir(home), "factory", "cursor", "*", "hook-events.jsonl")
    for path in sorted(glob.glob(pattern)):
        try:
            fh = open(path, encoding="utf-8", errors="replace")
        except OSError:
            continue
        with fh:
            for line in fh:
                try:
                    ev = json.loads(line)
                except ValueError:
                    continue
                if not isinstance(ev, dict):
                    continue
                sid = next((ev[k] for k in ("conversation_id", "session_id", "sessionId") if ev.get(k)), None)
                if not sid:
                    continue
                name = str(next((ev[k] for k in ("event", "hook_event_name", "hook") if ev.get(k)), ""))
                rec = out.setdefault(str(sid), {"shell": False, "deny": False})
                if name == "beforeShellExecution":
                    rec["shell"] = True
                verdict = str(next((ev[k] for k in ("decision", "permission", "action") if ev.get(k)), ""))
                if verdict.lower() == "deny":
                    rec["deny"] = True
    return out


def scan_transcript(path):
    """Stream one transcript; return (agent name, [command excerpts])."""
    agent = "?"
    attempts = []
    first_user = True
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if not isinstance(msg, dict):
                continue
            content = (msg.get("message") or {}).get("content")
            if not isinstance(content, list):
                continue
            if msg.get("role") == "user" and first_user:
                first_user = False
                for part in content:
                    if isinstance(part, dict) and part.get("type") == "text":
                        m = AGENT_RE.search(part.get("text") or "")
                        if m:
                            agent = m.group(1)
                            break
                continue
            for part in content:
                if not isinstance(part, dict) or part.get("type") != "tool_use" or part.get("name") != "Shell":
                    continue
                cmd = (part.get("input") or {}).get("command")
                if isinstance(cmd, str) and WORD_RE.search(cmd):
                    attempts.append(cmd[:EXCERPT])
    return agent, attempts


def read_meta(path):
    try:
        with open(path, encoding="utf-8") as fh:
            meta = json.load(fh)
    except (OSError, ValueError):
        return None
    return meta if isinstance(meta, dict) else None


def cwd_key(cwd):
    return "-".join(p for p in cwd.split("/") if p) if isinstance(cwd, str) else "?"


def fmt_time(ts):
    return datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def collect(home):
    """Return list of session dicts (unfiltered)."""
    transcripts = {}
    for p in glob.glob(os.path.join(home, ".cursor", "projects", "*", "agent-transcripts", "*", "*.jsonl")):
        sid = os.path.basename(os.path.dirname(p))
        if os.path.basename(p) != sid + ".jsonl":
            continue
        key = p.split(os.sep)[-4]
        transcripts[sid] = (key, p)
    metas = {}
    for p in glob.glob(os.path.join(home, ".cursor", "chats", "*", "*", "meta.json")):
        metas[os.path.basename(os.path.dirname(p))] = p
    events = load_hook_events(home)
    key_state = {}

    def hooks_file_state(key):
        if key not in key_state:
            root = resolve_key(key)
            if root is None:
                key_state[key] = "unknown"
            elif os.path.isfile(os.path.join(root, ".cursor", "hooks.json")):
                key_state[key] = "unknown"  # hooks.json exists but loading is not proven
            else:
                key_state[key] = "NO"
        return key_state[key]

    rows = []
    for sid in sorted(set(transcripts) | set(metas)):
        meta = read_meta(metas[sid]) if sid in metas else None
        tr = transcripts.get(sid)
        created = meta.get("createdAtMs") if meta else None
        if isinstance(created, (int, float)) and not isinstance(created, bool):
            first = created / 1000.0
        elif tr:
            try:
                first = os.stat(tr[1]).st_mtime
            except OSError:
                first = None
        else:
            first = None
        key = tr[0] if tr else cwd_key(meta.get("cwd") if meta else None)
        row = {"project": key, "session": sid, "first_seen": fmt_time(first) if first is not None else "?",
               "_ts": first, "agent": "?", "hooks_loaded": "gone", "sudo_or_herdr": []}
        if meta is None or tr is None:
            rows.append(row)
            continue
        try:
            agent, attempts = scan_transcript(tr[1])
        except OSError:
            rows.append(row)
            continue
        ev = events.get(sid, {})
        row["agent"] = agent
        if ev.get("shell"):
            row["hooks_loaded"] = "yes"
        else:
            row["hooks_loaded"] = hooks_file_state(key)
        outcome = "blocked" if ev.get("deny") else NOT_RECORDED
        row["sudo_or_herdr"] = [{"command": c, "outcome": outcome} for c in attempts]
        rows.append(row)
    return rows


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--since", metavar="YYYY-MM-DD", help="only sessions first seen on or after this UTC date")
    ap.add_argument("--json", action="store_true", help="emit JSON (rows plus summary)")
    args = ap.parse_args(argv)
    since = None
    if args.since:
        try:
            since = datetime.datetime.strptime(args.since, "%Y-%m-%d").replace(tzinfo=datetime.timezone.utc).timestamp()
        except ValueError:
            print("error: --since must be YYYY-MM-DD, e.g. --since 2026-11-03", file=sys.stderr)
            return 2
    rows = collect(home_dir())
    if since is not None:
        rows = [r for r in rows if r["_ts"] is not None and r["_ts"] >= since]
    rows.sort(key=lambda r: (r["_ts"] is None, r["_ts"] or 0, r["session"]))
    summary = {
        "sessions": len(rows),
        "hooks_loaded_no": sum(1 for r in rows if r["hooks_loaded"] == "NO"),
        "sudo_or_herdr_attempted": sum(1 for r in rows if r["sudo_or_herdr"]),
        "gone": sum(1 for r in rows if r["hooks_loaded"] == "gone"),
    }
    line = "sessions={sessions} hooks_loaded_no={hooks_loaded_no} sudo_or_herdr_attempted={sudo_or_herdr_attempted} gone={gone}".format(**summary)
    for r in rows:
        r.pop("_ts")
    if args.json:
        print(json.dumps({"sessions": rows, "summary": summary}, indent=2))
        print(line)
        return 0
    for r in rows:
        attempts = "; ".join("%r %s" % (a["command"], a["outcome"]) for a in r["sudo_or_herdr"]) or "-"
        print("%s\t%s\t%s\t%s\thooks_loaded=%s\tsudo_or_herdr=%s" % (
            r["project"], r["session"], r["first_seen"], r["agent"], r["hooks_loaded"], attempts))
    print(line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
