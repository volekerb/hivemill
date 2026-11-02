"""Orgs: two (or more) factory organisations side by side in one Hivemill (G-028).

An org is a complete chain of command (boss, director, team leads, workers) that runs on one agent runtime with one
model map. Every goal belongs to exactly one org (goals.org); every agent spawned for that goal inherits it
(agents.org) and carries the org prefix in its name: claude-boss, cursor-director, cursor-lead-g-028-a,
claude-coder-t-0840. The box's capacity (agent cap, memory floor, load) stays shared; P1 goals win across orgs.

Shared by bin/factory_cli.py and web/ so the CLI and the UI agree on names, labels, runtimes and models.
"""
from __future__ import annotations

import re

DEFAULT_ORG = "claude"

# Judgement roles think (plan, review, decide); build roles produce code/tests. Same split as factory MODELS.
JUDGEMENT_ROLES = ("boss", "director", "lead", "pm", "reviewer", "judge", "staff-engineer", "researcher")
BUILD_ROLES = ("coder", "qa", "cleaner", "scout", "evaluator")

ORGS = {
    "claude": {
        "label": "Claude",
        "runtime": "claude",
        # Opus for judgement, Sonnet for building, Haiku for the cleaner. Never Fable (owner rule).
        "models": {
            "boss": "claude-opus-5-5", "director": "claude-opus-5-5", "lead": "claude-opus-5-5",
            "pm": "claude-opus-5-5", "reviewer": "claude-opus-5-5", "judge": "claude-opus-5-5",
            "staff-engineer": "claude-opus-5-5", "researcher": "claude-opus-5-5",
            "coder": "claude-sonnet-5-5", "qa": "claude-sonnet-5-5", "cleaner": "claude-haiku-5-5",
            "scout": "claude-sonnet-5-5", "evaluator": "claude-sonnet-5-5",
        },
    },
    "cursor": {
        "label": "Cursor",
        "runtime": "cursor",
        # Owner 2026-11-01: Grok for judgement, Cursor Composer for building. No Anthropic models in this org.
        "models": {
            "boss": "grok-4.7-high", "director": "grok-4.7-high", "lead": "grok-4.7-high",
            "pm": "grok-4.7-high", "reviewer": "grok-4.7-high", "judge": "grok-4.7-high",
            "staff-engineer": "grok-4.7-high", "researcher": "grok-4.7-high",
            "coder": "composer-2.5", "qa": "composer-2.5", "cleaner": "composer-2.5",
            "scout": "composer-2.5",
            # skilleval evaluator always runs on Claude (agent_runtime: cursor refuses it)
            "evaluator": "claude-sonnet-5-5",
        },
    },
}

NAMES = tuple(ORGS)
PREFIX_RE = re.compile(r"^(%s)-" % "|".join(map(re.escape, NAMES)))
# org-singleton roles: exactly one per org, named <org>-<role>
SINGLETON_ROLES = ("boss", "director", "staff-engineer")


def check(org: str | None) -> str:
    """Validate an org name; None/'' means the default org."""
    org = (org or "").strip() or DEFAULT_ORG
    if org not in ORGS:
        raise ValueError(f"unknown org {org!r}; known: {', '.join(NAMES)}")
    return org


def runtime(org: str | None) -> str:
    return ORGS[check(org)]["runtime"]


def model(org: str | None, role: str) -> str:
    return ORGS[check(org)]["models"][role]


def label(org: str | None) -> str:
    return ORGS[check(org)]["label"]


def prefix_of(name: str | None) -> str | None:
    """'cursor' for 'cursor-coder-t-1', None for a legacy unprefixed name."""
    m = PREFIX_RE.match(name or "")
    return m.group(1) if m else None


def strip_prefix(name: str) -> str:
    return PREFIX_RE.sub("", name or "", count=1)


def name(org: str | None, base: str) -> str:
    """The org-prefixed agent name: name('cursor', 'coder-t-0840') -> 'cursor-coder-t-0840'. Idempotent."""
    org = check(org)
    if prefix_of(base):
        return base
    return f"{org}-{base}"


def of_agent(row) -> str:
    """The org of an agent row (dict or sqlite3.Row): the org column when set, else the name prefix, else the runtime
    recorded in spawn_args (legacy rows), else the default org."""
    get = row.get if hasattr(row, "get") else (lambda k, d=None: row[k] if k in row.keys() else d)
    org = get("org")
    if org in ORGS:
        return org
    p = prefix_of(get("name"))
    if p:
        return p
    rt = get("runtime")
    if not rt:
        import json
        try:
            rt = (json.loads(get("spawn_args") or "{}") or {}).get("runtime")
        except (ValueError, TypeError):
            rt = None
    for o, spec in ORGS.items():
        if spec["runtime"] == rt:
            return o
    return DEFAULT_ORG
