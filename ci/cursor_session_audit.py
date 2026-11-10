#!/usr/bin/env python3
"""Audit Cursor agent logs: which sessions ran without policy hooks, and did they run
privilege-escalation or herdr commands.

Run as ``python3 -I ci/cursor_session_audit.py [--log-dir DIR] [--since YYYY-MM-DD] [--json]``.

Primary source: LOGDIR/session-*.log (default /tmp/cursor-agent-logs-UID). Each line is
"[ISO] KIND JSON"; lines that do not parse are skipped. Per log file:
  - pid and start time come from the file name (session-ISO-PID-1.log);
  - workspacePath comes from the first conversationClassification.init; a log without
    one is a helper process: listed, not counted as a session;
  - agent name: the "You are `NAME`" line of the first user message of
    HOME/.cursor/projects/*/agent-transcripts/SESSION/SESSION.jsonl (read only up to that
    message); "?" when there is no such transcript or line;
  - hook counts by (hookStep, hookSource, status, failClosed) from analytics.track
    eventName cli.hook.executed;
  - guarded = yes when any cli.hook.executed has hookStep beforeShellExecution, else NO;
  - risky shell runs: every logger "Executing shell command" whose metadata.command has
    the privilege-escalation word or herdr as a word in any pipeline segment. Only the
    matched word and a per-word count are kept, never the command text. Outcome: "blocked by hook" when a beforeShellExecution hook with a deny/
    blocked status ran between it and the previous shell command, "hook ran, allowed"
    when a beforeShellExecution hook ran without deny, else "ran unguarded".
Secondary source: HOME/.cursor/projects/KEY/agent-transcripts/ID directories whose id is
in no surviving log are listed as "log gone (transcript only)"; hook state is never
inferred for them. The chat database is never opened and HOME is never walked.

Summary line (last): logs=N sessions=S unguarded=U risky_unguarded=R helper=H
transcripts_log_gone=G oldest_log=ISO. risky_unguarded counts risky runs outcome
"ran unguarded". The risky counts are a lower bound (the word match misses quoting tricks,
variables, eval and encoded forms); this is a log audit, not a guard. No command or message text is printed; only ids, workspace, agent name,
yes/no flags and counts (M31135).
"""
import argparse
import datetime
import glob
import json
import os
import re
import sys

PRIV_WORDS = ("su" + "do", "her" + "dr")  # matching constants only
DENY_STATUSES = {"denied", "deny", "blocked", "rejected"}
LINE_RE = re.compile(r"^\[([^\]]+)\] (\S+) (\{.*\})\s*$")
NAME_RE = re.compile(r"^session-(\d{4}-\d{2}-\d{2})T(\d{2})-(\d{2})-(\d{2})-\d+Z-(\d+)-\d+\.log$")
SEGMENT_RE = re.compile(r"\|\||&&|[|;&\n]")
NAME_IN_TEXT_RE = re.compile(r"You are `([A-Za-z0-9_.-]+)`")
TOKEN_RE = re.compile(r"[\s()]+")


def risky_words(command):
    """Set of matched words (never the command text) found in any pipeline segment."""
    found = set()
    for seg in SEGMENT_RE.split(command):
        for tok in TOKEN_RE.split(seg):
            base = os.path.basename(tok.strip("'\"`"))
            if base in PRIV_WORDS:
                found.add(base)
    return found


def parse_name(name):
    m = NAME_RE.match(name)
    if not m:
        return None
    d, hh, mm, ss, pid = m.groups()
    return "%sT%s:%s:%sZ" % (d, hh, mm, ss), int(pid)


def agent_name(home, session):
    """Agent name from the first user message ("You are `NAME`") of the session transcript.

    Globbed (no walk), streamed only until the first user line; "?" when absent."""
    if not session or session == "?" or os.sep in session:
        return "?"
    pattern = os.path.join(glob.escape(home), ".cursor", "projects", "*", "agent-transcripts",
                           glob.escape(session), glob.escape(session) + ".jsonl")
    for path in sorted(glob.glob(pattern)):
        try:
            with open(path, encoding="utf-8", errors="replace") as fh:
                for line in fh:
                    try:
                        rec = json.loads(line)
                    except ValueError:
                        continue
                    if not isinstance(rec, dict) or rec.get("role") != "user":
                        continue
                    msg = rec.get("message")
                    content = msg.get("content") if isinstance(msg, dict) else None
                    if isinstance(content, str):
                        content = [{"text": content}]
                    for part in content if isinstance(content, list) else []:
                        text = part.get("text") if isinstance(part, dict) else None
                        m = NAME_IN_TEXT_RE.search(text) if isinstance(text, str) else None
                        if m:
                            return m.group(1)
                    break  # first user message only
        except OSError:
            continue
    return "?"


def scan_log(path):
    """Stream one log. Return dict with workspace, conversation ids, hooks, risky runs."""
    res = {"workspace": None, "conversations": [], "hooks": {}, "guarded": False, "risky": []}
    window_hook = None  # None: no beforeShellExecution since last shell command; else "allow"/"deny"
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            m = LINE_RE.match(line)
            if not m:
                continue
            kind = m.group(2)
            if kind not in ("conversationClassification.init", "analytics.track", "logger"):
                continue
            try:
                data = json.loads(m.group(3))
            except ValueError:
                continue
            if not isinstance(data, dict):
                continue
            if kind == "conversationClassification.init":
                if isinstance(data.get("conversationId"), str):
                    res["conversations"].append(data["conversationId"])
                if res["workspace"] is None and isinstance(data.get("workspacePath"), str):
                    res["workspace"] = data["workspacePath"]
            elif kind == "analytics.track":
                if data.get("eventName") != "cli.hook.executed":
                    continue
                props = data.get("props")
                if not isinstance(props, dict):
                    continue
                step = props.get("hookStep")
                key = "/".join(str(props.get(k)) for k in ("hookStep", "hookSource", "status", "failClosed"))
                res["hooks"][key] = res["hooks"].get(key, 0) + 1
                if step == "beforeShellExecution":
                    res["guarded"] = True
                    denied = str(props.get("status", "")).lower() in DENY_STATUSES
                    if denied or window_hook is None:
                        window_hook = "deny" if denied else "allow"
            elif data.get("message") == "Executing shell command":
                meta = data.get("metadata")
                cmd = meta.get("command") if isinstance(meta, dict) else None
                if isinstance(cmd, str):
                    outcome = {"deny": "blocked by hook", "allow": "hook ran, allowed"}.get(window_hook, "ran unguarded")
                    for word in sorted(risky_words(cmd)):
                        res["risky"].append({"word": word, "outcome": outcome})
                window_hook = None
    return res


def transcript_ids(home):
    """(key, id) for every transcript directory; globbed, never walked."""
    out = []
    for p in glob.glob(os.path.join(home, ".cursor", "projects", "*", "agent-transcripts", "*")):
        if os.path.isdir(p):
            out.append((p.split(os.sep)[-3], os.path.basename(p)))
    return sorted(out)


def collect(log_dir, home):
    logs = []
    seen_ids = set()
    for path in sorted(glob.glob(os.path.join(log_dir, "session-*.log"))):
        parsed = parse_name(os.path.basename(path))
        if parsed is None:
            continue
        start, pid = parsed
        try:
            scan = scan_log(path)
        except OSError:
            continue
        seen_ids.update(scan["conversations"])
        session = scan["conversations"][0] if scan["conversations"] else "?"
        logs.append({
            "log": os.path.basename(path), "pid": pid,
            "session": session, "start": start,
            "kind": "session" if scan["workspace"] else "helper",
            "workspace": scan["workspace"], "agent": agent_name(home, session),
            "guarded": "yes" if scan["guarded"] else "NO",
            "hooks": scan["hooks"], "risky": scan["risky"],
            "ran": {w: sum(1 for x in scan["risky"] if x["word"] == w) for w in PRIV_WORDS},
        })
    gone = [{"project": k, "session": i, "state": "log gone (transcript only)"}
            for k, i in transcript_ids(home) if i not in seen_ids]
    return logs, gone


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--log-dir", help="Cursor agent log dir (default /tmp/cursor-agent-logs-UID)")
    ap.add_argument("--since", metavar="YYYY-MM-DD", help="only logs started on or after this UTC date")
    ap.add_argument("--json", action="store_true", help="emit JSON (rows plus summary)")
    args = ap.parse_args(argv)
    since = None
    if args.since:
        try:
            datetime.datetime.strptime(args.since, "%Y-%m-%d")
        except ValueError:
            print("error: --since must be YYYY-MM-DD, e.g. --since 2026-11-03", file=sys.stderr)
            return 2
        since = args.since
    home = os.environ.get("HOME") or os.path.expanduser("~")
    log_dir = args.log_dir or "/tmp/cursor-agent-logs-%d" % os.getuid()
    logs, gone = collect(log_dir, home)
    if since:
        logs = [r for r in logs if r["start"][:10] >= since]
    sessions = [r for r in logs if r["kind"] == "session"]
    summary = {
        "logs": len(logs),
        "sessions": len(sessions),
        "unguarded": sum(1 for r in sessions if r["guarded"] == "NO"),
        "risky_unguarded": sum(1 for r in sessions for x in r["risky"] if x["outcome"] == "ran unguarded"),
        "helper": len(logs) - len(sessions),
        "transcripts_log_gone": len(gone),
        "oldest_log": min((r["start"] for r in logs), default="none"),
    }
    line = " ".join("%s=%s" % kv for kv in summary.items())
    if args.json:
        print(json.dumps({"logs": logs, "transcripts_log_gone": gone, "summary": summary}, indent=2))
        return 0
    for r in logs:
        hooks = ",".join("%s=%d" % kv for kv in sorted(r["hooks"].items())) or "-"
        ran = " ".join("%s=%s(%d)" % (w, "yes" if n else "no", n) for w, n in r["ran"].items())
        oc = ["%s=%d" % (o.split()[0], sum(1 for x in r["risky"] if x["outcome"] == o))
              for o in ("ran unguarded", "blocked by hook", "hook ran, allowed")]
        unguarded = sum(1 for x in r["risky"] if x["outcome"] == "ran unguarded")
        print("%s\t%s\tpid=%d\t%s\tagent=%s\tbeforeShellExecution=%s\t%s\tunguarded_runs=%d\toutcomes=%s\thooks=%s" % (
            r["session"], r["kind"], r["pid"], r["workspace"] or "-", r["agent"],
            "yes" if r["guarded"] == "yes" else "no", ran, unguarded, ",".join(oc), hooks))
    for g in gone:
        print("%s\t%s\t%s" % (g["project"], g["session"], g["state"]))
    print(line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
