#!/usr/bin/env python3
"""UserPromptSubmit hook for the boss pane.

The tick never types into the boss pane (the owner does). Instead, each owner message
triggers this hook: it runs `factory inbox` (which marks the mail read) and hands the output
to the model as additionalContext. Always exits 0 and never blocks the owner's prompt.
Empty inbox: prints nothing. factory missing/failing/timing out: prints nothing.
"""
import json
import subprocess
import sys

LIMIT = 8000
HEADER = "Factory inbox (read automatically with this owner message; act on it and report in this same reply):"
TAIL = "\n(truncated; run factory inbox --all)"


def main():
    try:
        sys.stdin.read()  # content ignored; bad or empty stdin must not matter
    except Exception:
        pass
    try:
        p = subprocess.run(["factory", "inbox", "--limit", "1000"], capture_output=True, text=True, timeout=8)
    except Exception:
        return
    out = (p.stdout or "").strip()
    if p.returncode != 0 or not out or out == "inbox empty":
        return
    body = HEADER + "\n" + out
    if len(body) > LIMIT:
        body = body[:LIMIT] + TAIL
    print(json.dumps({"hookSpecificOutput": {"hookEventName": "UserPromptSubmit", "additionalContext": body}}))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        pass
    sys.exit(0)
