#!/usr/bin/env python3
"""Translate Cursor Agent hook stdin into worker-guard.py's Claude PreToolUse shape.

Cursor events (https://cursor.com/docs/hooks — beforeMCPExecution verified 2026-11-03):
  beforeShellExecution  — {command, cwd, sandbox}; expects JSON {permission: allow|deny|ask}
  beforeMCPExecution    — {tool_name, tool_input, mcp_server_name, ...}; same permission shape
  preToolUse            — {tool_name, tool_input, cwd, ...}; expects JSON {permission: allow|deny}
                          or exit 2 to deny (Claude-compatible)

worker-guard.py expects Claude Code PreToolUse:
  {tool_name: Bash|Read|Grep|Edit|Write|..., tool_input: {command|file_path|...}, cwd?}
  exit 0 allow, exit 2 deny (fail closed)

This adapter is the pilot bridge. It does not weaken worker-guard rules.
"""
from __future__ import annotations

import argparse
import datetime
import json
import os
import subprocess
import sys

GUARD = os.path.join(os.path.dirname(os.path.abspath(__file__)), "worker-guard.py")

# Cursor tool args may use any of these keys; the model chooses them — check all present values.
PATH_KEYS = ("file_path", "path", "notebook_path", "target_notebook")


def _paths_from_tool_input(ti: dict) -> list[str]:
    if not isinstance(ti, dict):
        return []
    seen: set[str] = set()
    out: list[str] = []
    for k in PATH_KEYS:
        v = ti.get(k)
        if isinstance(v, str) and v and v not in seen:
            seen.add(v)
            out.append(v)
    return out


def _path_from_tool_input(ti: dict) -> str | None:
    paths = _paths_from_tool_input(ti)
    return paths[0] if paths else None


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

    if tool in ("Write", "Edit", "MultiEdit", "NotebookEdit", "Delete", "StrReplace", "EditNotebook"):
        path = _path_from_tool_input(ti)
        # Delete → treat as Write path check (persist / live-home path rules)
        return {"tool_name": "Write", "tool_input": {"file_path": path or ""}, **base}

    if tool in ("Read", "Grep"):
        path = _path_from_tool_input(ti) or ti.get("path")
        if tool == "Read":
            return {"tool_name": "Read", "tool_input": {"file_path": path or ""}, **base}
        return {"tool_name": "Grep", "tool_input": {"path": path or ""}, **base}

    return None  # unknown tool: do not invent a block


def mcp_to_guard_payload(data: dict) -> dict:
    """Map beforeMCPExecution stdin to worker-guard PreToolUse (non-Bash tools are allowed in guard)."""
    cwd = data.get("cwd") if isinstance(data.get("cwd"), str) else None
    base: dict = {}
    if cwd:
        base["cwd"] = cwd
    if isinstance(data.get("session_id"), str):
        base["session_id"] = data["session_id"]
    tool = data.get("tool_name") if isinstance(data.get("tool_name"), str) else "MCP"
    ti = data.get("tool_input")
    if isinstance(ti, str):
        try:
            ti = json.loads(ti)
        except Exception:
            ti = {}
    if not isinstance(ti, dict):
        ti = {}
    return {"tool_name": tool, "tool_input": ti, **base}


_PATH_CHECK_TOOLS = frozenset(
    {"Write", "Edit", "MultiEdit", "NotebookEdit", "Delete", "StrReplace", "EditNotebook", "Read", "Grep"}
)


def _guard_payloads_for_all_paths(payload: dict, data: dict) -> list[dict]:
    """One worker-guard PreToolUse object per path-like key value in tool_input."""
    tool = data.get("tool_name")
    ti = data.get("tool_input") if isinstance(data.get("tool_input"), dict) else {}
    paths = _paths_from_tool_input(ti)
    if not paths:
        return [payload]
    base = {k: v for k, v in payload.items() if k not in ("tool_name", "tool_input")}
    out: list[dict] = []
    for path in paths:
        if tool in ("Read",):
            out.append({"tool_name": "Read", "tool_input": {"file_path": path}, **base})
        elif tool in ("Grep",):
            out.append({"tool_name": "Grep", "tool_input": {"path": path}, **base})
        else:
            out.append({"tool_name": "Write", "tool_input": {"file_path": path}, **base})
    return out


def run_guard(payload: dict, role: str | None, hq: bool) -> subprocess.CompletedProcess:
    argv = [sys.executable, GUARD]
    if hq:
        argv.append("--hq")
    if role:
        argv.extend(["--role", role])
    return subprocess.run(
        argv,
        input=json.dumps(payload),
        capture_output=True,
        text=True,
        timeout=60,
    )


_LOG_ROLE: str | None = None


def log_decision(event: str, allow: bool, role: str | None = None) -> None:
    """Append {ts, agent, role, event, decision} to FACTORY_CURSOR_HOOK_LOG. Never raises, never logs command text."""
    try:
        path = os.environ.get("FACTORY_CURSOR_HOOK_LOG")
        if not path:
            return
        rec = {
            "ts": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
            "agent": os.environ.get("FACTORY_AGENT", ""),
            "role": role or os.environ.get("FACTORY_ROLE", ""),
            "event": event,
            "decision": "allow" if allow else "deny",
        }
        with open(path, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(rec) + "\n")
    except Exception:  # logging must never change the decision or exit code
        pass


def emit_cursor_decision(event: str, allow: bool, reason: str) -> int:
    """Print Cursor hook JSON. Always exit 0 after a valid decision body (deny is in JSON).

    Fail-closed callers should set failClosed:true so a crash/timeout still blocks.
    """
    log_decision(event, allow, _LOG_ROLE)
    if allow:
        print(json.dumps({"permission": "allow"}))
        return 0
    msg = (reason or "blocked by worker-guard").strip() or "blocked by worker-guard"
    body = {"permission": "deny", "agent_message": msg, "user_message": msg}
    print(json.dumps(body))
    # Also exit 2 for preToolUse Claude-compat (Cursor accepts both).
    return 2 if event in ("preToolUse", "beforeMCPExecution") else 0


def deny_direct(event: object, msg: str) -> int:
    """Early deny (bad stdin, unsupported event, guard crash): log it, print the deny body, exit 2."""
    log_decision(event if isinstance(event, str) and event else "unknown", False, _LOG_ROLE)
    print(json.dumps({"permission": "deny", "agent_message": msg}))
    return 2


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--role", default=None, help="worker role for worker-guard (default: non-cleaner worker)")
    p.add_argument("--hq", action="store_true", help="HQ mode (PRIV only)")
    p.add_argument(
        "--event",
        default=None,
        help="beforeShellExecution|beforeMCPExecution|preToolUse (else read hook_event_name from stdin JSON)",
    )
    args = p.parse_args(argv)
    global _LOG_ROLE
    _LOG_ROLE = args.role

    try:
        data = json.load(sys.stdin)
    except Exception as e:
        return deny_direct(args.event, f"cursor-guard-adapter: bad JSON on stdin ({e})")

    if not isinstance(data, dict):
        return deny_direct(args.event, "cursor-guard-adapter: stdin JSON must be an object")

    event = args.event or data.get("hook_event_name") or data.get("event")
    if event not in ("beforeShellExecution", "preToolUse", "beforeMCPExecution"):
        # Unknown event: fail closed for this adapter (it is only wired for these).
        return deny_direct(event, f"cursor-guard-adapter: unsupported event {event!r}")

    if event == "beforeMCPExecution":
        role = args.role or "worker"
        if role != "boss":
            msg = f"MCP disabled for factory role {role}"
            return emit_cursor_decision(event, False, msg)
        payload = mcp_to_guard_payload(data)
        try:
            r = run_guard(payload, role, args.hq)
        except Exception as e:
            return deny_direct(event, f"cursor-guard-adapter: guard failed ({e})")
        if r.returncode == 0:
            return emit_cursor_decision(event, True, "")
        reason = (r.stderr or r.stdout or f"worker-guard exit {r.returncode}").strip()
        return emit_cursor_decision(event, False, reason)

    payload = to_claude_payload(data, event)
    if payload is None:
        return emit_cursor_decision(event, True, "")

    tool = data.get("tool_name")
    if tool in _PATH_CHECK_TOOLS:
        payloads = _guard_payloads_for_all_paths(payload, data)
    else:
        payloads = [payload]

    last_reason = ""
    try:
        for check in payloads:
            r = run_guard(check, args.role, args.hq)
            if r.returncode != 0:
                last_reason = (r.stderr or r.stdout or f"worker-guard exit {r.returncode}").strip()
                return emit_cursor_decision(event, False, last_reason)
    except Exception as e:
        return deny_direct(event, f"cursor-guard-adapter: guard failed ({e})")

    return emit_cursor_decision(event, True, "")


if __name__ == "__main__":
    sys.exit(main())
