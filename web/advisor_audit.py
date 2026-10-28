"""Audit past use of the Claude Code `advisor` tool and of Fable models. Read-only, stdlib only.

    python3 -m web.advisor_audit [--projects DIR] [--json]

Scans ~/.claude/projects/**/*.jsonl (or $FACTORY_CLAUDE_PROJECTS / --projects) for
  (a) real advisor calls: tool_use / server_tool_use blocks named "advisor" (tool definitions are not counted),
  (b) assistant messages whose model id contains "fable",
  (c) the advisor model named in the session (advisor tool definitions and advisorModel fields); any model
      outside ALLOWED_ADVISOR_MODELS is flagged.
Per session it prints the agent (from the project directory name), the session id, counts and timestamps. Message
content, prompts and tool output are never kept or printed. Nothing is written. Why: the advisor forwards the whole
transcript on every call (cost); roles/settings/*.json disable it, see docs/spawn-context.md.
"""
import argparse
import json
import os
import re
import sys
from pathlib import Path

ALLOWED_ADVISOR_MODELS = ("claude-opus-5-5", "claude-sonnet-5-5")
MAX_TS = 20  # timestamps listed per session; the count is always exact


def projects_dir():
    return Path(os.environ.get("FACTORY_CLAUDE_PROJECTS") or Path.home() / ".claude" / "projects")


def agent_of(path, root):
    """Agent label from the transcript path: the project dir name (a factory worktree dir ends in G-nnn-T-nnnn)."""
    rel = Path(path).relative_to(root)
    proj = rel.parts[0] if len(rel.parts) > 1 else ""
    m = re.search(r"(G-\d+-T-\d+)$", proj)
    return m.group(1) if m else (proj.lstrip("-") or "?")


def _advisor_models(node, out):
    """Collect model ids named by advisor tool definitions ({"type": "advisor_*", "model": ..}) and advisorModel."""
    if isinstance(node, dict):
        t = node.get("type")
        if isinstance(t, str) and t.startswith("advisor_") and isinstance(node.get("model"), str):
            out.add(node["model"])
        if isinstance(node.get("advisorModel"), str) and node["advisorModel"]:
            out.add(node["advisorModel"])
        for v in node.values():
            _advisor_models(v, out)
    elif isinstance(node, list):
        for v in node:
            _advisor_models(v, out)


def scan_file(path):
    """-> {advisor_calls: [ts], fable: [ts], advisor_models: set}. Only names, ids and timestamps are read out."""
    res = {"advisor_calls": [], "fable": [], "advisor_models": set()}
    with open(path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            low = line.lower()
            if "advisor" not in low and "fable" not in low:
                continue  # cheap prefilter: most lines never reach json.loads
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(obj, dict):
                continue
            ts = obj.get("timestamp") or ""
            _advisor_models(obj, res["advisor_models"])
            msg = obj.get("message")
            if obj.get("type") != "assistant" or not isinstance(msg, dict):
                continue
            if "fable" in str(msg.get("model") or "").lower():
                res["fable"].append(ts)
            content = msg.get("content")
            for b in content if isinstance(content, list) else []:
                if isinstance(b, dict) and b.get("type") in ("tool_use", "server_tool_use") and b.get("name") == "advisor":
                    res["advisor_calls"].append(ts)
    return res


def audit(root):
    """-> list of per-session rows that have an advisor call, a fable model or a flagged advisor model."""
    rows = []
    for p in sorted(Path(root).rglob("*.jsonl")):
        r = scan_file(p)
        bad = sorted(m for m in r["advisor_models"] if m not in ALLOWED_ADVISOR_MODELS)
        if not (r["advisor_calls"] or r["fable"] or bad):
            continue
        rows.append({"agent": agent_of(p, root), "session": p.stem,
                     "advisor_calls": len(r["advisor_calls"]), "advisor_call_ts": sorted(r["advisor_calls"])[:MAX_TS],
                     "fable_messages": len(r["fable"]), "fable_ts": sorted(r["fable"])[:MAX_TS],
                     "advisor_models": sorted(r["advisor_models"]), "flagged_advisor_models": bad})
    return rows


def render(rows):
    if not rows:
        return "no advisor calls, no fable model ids, no flagged advisor models"
    out = []
    for r in rows:
        out.append(f"{r['agent']} session={r['session']} advisor_calls={r['advisor_calls']} "
                   f"fable_messages={r['fable_messages']} advisor_models={','.join(r['advisor_models']) or '-'}"
                   + (f" FLAGGED={','.join(r['flagged_advisor_models'])}" if r["flagged_advisor_models"] else ""))
        for label, key in (("advisor", "advisor_call_ts"), ("fable", "fable_ts")):
            if r[key]:
                out.append(f"    {label} at: {' '.join(r[key])}")
    out.append(f"total: {len(rows)} sessions, advisor_calls={sum(r['advisor_calls'] for r in rows)}, "
               f"fable_messages={sum(r['fable_messages'] for r in rows)}")
    return "\n".join(out)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--projects", help="transcript root (default ~/.claude/projects or $FACTORY_CLAUDE_PROJECTS)")
    ap.add_argument("--json", action="store_true")
    a = ap.parse_args(argv)
    root = Path(a.projects) if a.projects else projects_dir()
    if not root.is_dir():
        print(f"transcript directory not found: {root} (pass --projects DIR)", file=sys.stderr)
        return 2
    rows = audit(root)
    print(json.dumps(rows, indent=2) if a.json else render(rows))
    return 0


if __name__ == "__main__":
    sys.exit(main())
