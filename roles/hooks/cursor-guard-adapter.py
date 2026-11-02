#!/usr/bin/env python3
"""Translate Cursor Agent hook stdin into worker-guard.py's Claude PreToolUse shape.

Cursor events (docs.cursor.com/docs/hooks):
  beforeShellExecution  — {command, cwd, sandbox}; expects JSON {permission: allow|deny|ask}
  preToolUse            — {tool_name, tool_input, cwd, ...}; expects JSON {permission: allow|deny}
                          or exit 2 to deny (Claude-compatible)

worker-guard.py expects Claude Code PreToolUse:
  {tool_name: Bash|Read|Grep|Edit|Write|..., tool_input: {command|file_path|...}, cwd?}
  exit 0 allow, exit 2 deny (fail closed)

This adapter is the pilot bridge. It does not weaken worker-guard rules.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys

GUARD = os.path.join(os.path.dirname(os.path.abspath(__file__)), "worker-guard.py")

# Cursor Write/Delete path keys seen in docs/examples; accept any of them.
PATH_KEYS = ("file_path", "path", "notebook_path")


def _path_from_tool_input(ti: dict) -> str | None:
    if not isinstance(ti, dict):
        return None
    for k in PATH_KEYS:
        v = ti.get(k)
        if isinstance(v, str) and v:
            return v
    return None


def to_claude_payload(data: dict, event: str) -> dict | None:
    """Map one Cursor hook payload to a worker-guard PreToolUse object, or None to skip (allow)."""
    cwd = data.get("cwd") if isinstance(data.get("cwd"), str) else None
    base = {}
    if cwd:
        base["cwd"] = cwd
    if isinstance(data.get("session_id"), str):
        base["session_id"] = data["session_id"]

    if event == "beforeShellExecution":
        cmd = data.get("command")
        if not isinstance(cmd, str):
            return {"tool_name": "Bash", "tool_input": {}, **base}  # fail closed in guard
        return {"tool_name": "Bash", "tool_input": {"command": cmd}, **base}

    tool = data.get("tool_name")
    ti = data.get("tool_input") if isinstance(data.get("tool_input"), dict) else {}

    if tool in ("Shell", "Bash"):
        cmd = ti.get("command") if isinstance(ti.get("command"), str) else data.get("command")
        if not isinstance(cmd, str):
            return {"tool_name": "Bash", "tool_input": {}, **base}
        return {"tool_name": "Bash", "tool_input": {"command": cmd}, **base}

    if tool in ("Write", "Edit", "MultiEdit", "NotebookEdit", "Delete"):
        path = _path_from_tool_input(ti)
        # Delete → treat as Write path check (persist / live-home path rules)
        return {"tool_name": "Write", "tool_input": {"file_path": path or ""}, **base}

    if tool in ("Read", "Grep"):
        path = _path_from_tool_input(ti) or ti.get("path")
        if tool == "Read":
            return {"tool_name": "Read", "tool_input": {"file_path": path or ""}, **base}
        return {"tool_name": "Grep", "tool_input": {"path": path or ""}, **base}

    return None  # unknown tool: do not invent a block


def run_guard(payload: dict, role: str | None, hq: bool) -> subprocess.CompletedProcess:
    argv = [sys.executable, GUARD]
    if hq:
        argv.append("--hq")
    elif role:
        argv.extend(["--role", role])
    return subprocess.run(
        argv,
        input=json.dumps(payload),
        capture_output=True,
        text=True,
        timeout=60,
    )


def emit_cursor_decision(event: str, allow: bool, reason: str) -> int:
    """Print Cursor hook JSON. Always exit 0 after a valid decision body (deny is in JSON).

    Fail-closed callers should set failClosed:true so a crash/timeout still blocks.
    """
    if allow:
        print(json.dumps({"permission": "allow"}))
        return 0
    msg = (reason or "blocked by worker-guard").strip() or "blocked by worker-guard"
    body = {"permission": "deny", "agent_message": msg, "user_message": msg}
    print(json.dumps(body))
    # Also exit 2 for preToolUse Claude-compat (Cursor accepts both).
    return 2 if event == "preToolUse" else 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--role", default=None, help="worker role for worker-guard (default: non-cleaner worker)")
    p.add_argument("--hq", action="store_true", help="HQ mode (PRIV only)")
    p.add_argument(
        "--event",
        default=None,
        help="beforeShellExecution|preToolUse (else read hook_event_name from stdin JSON)",
    )
    args = p.parse_args(argv)

    try:
        data = json.load(sys.stdin)
    except Exception as e:
        print(json.dumps({
            "permission": "deny",
            "agent_message": f"cursor-guard-adapter: bad JSON on stdin ({e})",
        }))
        return 2

    if not isinstance(data, dict):
        print(json.dumps({
            "permission": "deny",
            "agent_message": "cursor-guard-adapter: stdin JSON must be an object",
        }))
        return 2

    event = args.event or data.get("hook_event_name") or data.get("event")
    if event not in ("beforeShellExecution", "preToolUse"):
        # Unknown event: fail closed for this adapter (it is only wired for those two).
        print(json.dumps({
            "permission": "deny",
            "agent_message": f"cursor-guard-adapter: unsupported event {event!r}",
        }))
        return 2

    payload = to_claude_payload(data, event)
    if payload is None:
        return emit_cursor_decision(event, True, "")

    try:
        r = run_guard(payload, args.role, args.hq)
    except Exception as e:
        print(json.dumps({
            "permission": "deny",
            "agent_message": f"cursor-guard-adapter: guard failed ({e})",
        }))
        return 2

    if r.returncode == 0:
        return emit_cursor_decision(event, True, "")
    reason = (r.stderr or r.stdout or f"worker-guard exit {r.returncode}").strip()
    return emit_cursor_decision(event, False, reason)


if __name__ == "__main__":
    sys.exit(main())
