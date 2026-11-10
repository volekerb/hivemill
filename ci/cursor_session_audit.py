#!/usr/bin/env python3
"""Audit Cursor agent logs: which sessions ran without policy hooks, and did they run
privilege-escalation or herdr commands.

Run as ``python3 -I ci/cursor_session_audit.py [--log-dir DIR] [--since YYYY-MM-DD] [--json]``.

Primary source: LOGDIR/session-*.log (default /tmp/cursor-agent-logs-UID). Each line is
"[ISO] KIND JSON"; lines that do not parse are skipped. Per log file:
  - pid and start time come from the file name (session-ISO-PID-1.log);
  - workspacePath comes from the first conversationClassification.init; a log without
    one is a helper process: listed, not counted as a session;
  - agent name: pid matched against ``factory agents --json`` (or --agents-json FILE);
    "?" when that read-only command is unavailable or does not list the pid;
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
"ran unguarded". No command or message text is printed; only ids, workspace, agent name,
yes/no flags and counts (M31135).
"""
import argparse
import datetime
import glob
import json
import os
import re
import subprocess
import sys

PRIV_WORDS = ("su" + "do", "her" + "dr")  # matching constants only
DENY_STATUSES = {"denied", "deny", "blocked", "rejected"}
LINE_RE = re.compile(r"^\[([^\]]+)\] (\S+) (\{.*\})\s*$")
NAME_RE = re.compile(r"^session-(\d{4}-\d{2}-\d{2})T(\d{2})-(\d{2})-(\d{2})-\d+Z-(\d+)-\d+\.log$")
SEGMENT_RE = re.compile(r"\|\||&&|[|;&\n]")
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


def load_agents(agents_json):
    """pid -> agent name. Empty when the source is unavailable."""
    try:
        if agents_json:
            with open(agents_json, encoding="utf-8") as fh:
                data = json.load(fh)
        else:
            proc = subprocess.run(["factory", "agents", "--json"], capture_output=True, text=True, timeout=20)
            if proc.returncode != 0:
                return {}
            data = json.loads(proc.stdout)
    except (OSError, ValueError, subprocess.SubprocessError):
        return {}
    if isinstance(data, dict):
        data = data.get("agents", [])
    out = {}
    if isinstance(data, list):
        for a in data:
            if isinstance(a, dict) and isinstance(a.get("pid"), int):
                name = a.get("name") or a.get("agent")
                if isinstance(name, str) and name:
                    out[a["pid"]] = name
    return out


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


def collect(log_dir, home, agents):
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
        logs.append({
            "log": os.path.basename(path), "pid": pid,
            "session": scan["conversations"][0] if scan["conversations"] else "?", "start": start,
            "kind": "session" if scan["workspace"] else "helper",
            "workspace": scan["workspace"], "agent": agents.get(pid, "?"),
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
    ap.add_argument("--agents-json", metavar="FILE", help="read the pid->agent list from FILE instead of factory agents --json")
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
    logs, gone = collect(log_dir, home, load_agents(args.agents_json))
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
        unguarded = sum(1 for x in r["risky"] if x["outcome"] == "ran unguarded")
        print("%s\t%s\tpid=%d\t%s\tagent=%s\tbeforeShellExecution=%s\t%s\tunguarded_runs=%d\thooks=%s" % (
            r["session"], r["kind"], r["pid"], r["workspace"] or "-", r["agent"],
            "yes" if r["guarded"] == "yes" else "no", ran, unguarded, hooks))
    for g in gone:
        print("%s\t%s\t%s" % (g["project"], g["session"], g["state"]))
    print(line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
