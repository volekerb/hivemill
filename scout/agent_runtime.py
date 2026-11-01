"""Multi-runtime agent harness for Hivemill.

Default runtime is Claude Code (unchanged spawn path). Cursor is the first
extension; more runtimes (codex, etc.) register the same way.

Config (empty = all Claude):
  agent_runtimes=pm=cursor
  agent_runtimes=*=cursor          # every Cursor-capable role
  agent_runtimes=all=cursor        # same as *=cursor
  cursor_pilot_roles=pm            # legacy alias → pm=cursor

A runtime owns: prepare workspace policy, build start argv, how to launch
(herdr kind vs pane-run vs subprocess), and kickoff delivery.
"""
from __future__ import annotations

import json
import os
import shlex
import shutil
import subprocess
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

from scout import cursor_harness as ch

# claude: every role (default). cursor: every role except evaluator.
ALLOWED = {
    "claude": frozenset(),
    "cursor": ch.CURSOR_ROLES,
}

KNOWN = frozenset(ALLOWED)


@dataclass
class SpawnPlan:
    """What factory_cli needs to start one agent after place()/settings render."""
    runtime: str
    argv: list[str]
    herdr_kind: str | None
    launch: str  # "herdr-kind" | "pane-run" | "subprocess"
    binary: str | None = None
    notes: list[str] = field(default_factory=list)
    prepared: dict = field(default_factory=dict)
    session_id: str | None = None
    # Actual agent workspace (may differ from factory cwd for HQ policy isolation)
    workspace: Path | None = None


@dataclass
class SpawnContext:
    role: str
    name: str
    cwd: Path
    model: str
    prompt_text: str
    kickoff: str
    settings_path: Path
    settings: dict
    home: Path
    hooks_dir: Path
    plugin_dirs: list[str]
    add_dirs: list[str]
    resume: str | None = None
    fresh: bool = False
    effort: list[str] = field(default_factory=list)
    append_prompt_file: Path | None = None
    session_id: str | None = None
    evaluator: bool = False
    sandbox: bool = False  # Cursor --sandbox enabled (not Claude nsbox)


def parse_runtimes(agent_runtimes: str, cursor_pilot_roles: str = "") -> dict[str, str]:
    """Return {role: runtime}. Raises ValueError on bad tokens.

    Special keys: *=cursor / all=cursor expand to every Cursor-capable role.
    """
    out: dict[str, str] = {}
    raw = (agent_runtimes or "").strip()
    if raw:
        for part in raw.split(","):
            part = part.strip()
            if not part:
                continue
            if "=" not in part:
                raise ValueError(
                    f"agent_runtimes entry {part!r} must be role=runtime "
                    f"(known runtimes: {', '.join(sorted(KNOWN))}; "
                    f"use *=cursor or all=cursor for every Cursor-capable role)"
                )
            role, _, runtime = part.partition("=")
            role, runtime = role.strip(), runtime.strip()
            if runtime not in KNOWN:
                raise ValueError(f"unknown runtime {runtime!r}; known: {', '.join(sorted(KNOWN))}")
            if role in ("*", "all"):
                if runtime == "claude":
                    # explicit all-claude: clear map (default is already Claude)
                    continue
                for r in sorted(ALLOWED[runtime]):
                    out[r] = runtime
                continue
            if runtime != "claude" and role not in ALLOWED[runtime]:
                raise ValueError(
                    f"runtime {runtime!r} allow-list is {sorted(ALLOWED[runtime])}; not {role!r}"
                )
            out[role] = runtime
    for role in [x.strip() for x in (cursor_pilot_roles or "").split(",") if x.strip()]:
        if role not in ALLOWED["cursor"]:
            raise ValueError(
                f"cursor_pilot_roles: {role!r} not in cursor allow-list "
                f"({', '.join(sorted(ALLOWED['cursor']))})"
            )
        out.setdefault(role, "cursor")
    return out


def resolve_runtime(role: str, agent_runtimes: str, cursor_pilot_roles: str = "") -> str:
    return parse_runtimes(agent_runtimes, cursor_pilot_roles).get(role, "claude")


def plan_spawn(runtime: str, ctx: SpawnContext) -> SpawnPlan:
    if runtime == "claude":
        return _plan_claude(ctx)
    if runtime == "cursor":
        return _plan_cursor(ctx)
    raise ValueError(f"no planner for runtime {runtime!r}")


def _plan_claude(ctx: SpawnContext) -> SpawnPlan:
    """Mirror today's `_spawn_start` claude_args (Claude Code via herdr --kind claude)."""
    settings = ctx.settings_path
    argv = [
        "--model", ctx.model,
        "--permission-mode", "auto",
        "--settings", str(settings),
    ]
    if ctx.append_prompt_file:
        argv += ["--append-system-prompt-file", str(ctx.append_prompt_file)]
    argv += list(ctx.effort)
    if not ctx.evaluator:
        for d in ctx.add_dirs:
            argv += ["--add-dir", d]
    argv += ["-n", ctx.name]
    if ctx.role != "boss":
        argv += ["--strict-mcp-config", "--mcp-config", '{"mcpServers":{}}']
    for plugin in ctx.plugin_dirs:
        argv += ["--plugin-dir", plugin]
    sid = ctx.session_id
    if ctx.resume:
        argv += ["--resume", ctx.resume, "--system-prompt-snapshot", "off"]
        sid = ctx.resume
    else:
        sid = sid or str(uuid.uuid4())
        argv += ["--session-id", sid]
        if ctx.fresh:
            argv += ["--system-prompt-snapshot", "off"]
    return SpawnPlan(
        runtime="claude",
        argv=argv,
        herdr_kind="claude",
        launch="herdr-kind",
        session_id=sid,
        workspace=Path(ctx.cwd),
    )


def _plan_cursor(ctx: SpawnContext) -> SpawnPlan:
    """Cursor via herdr --kind cursor (same pane lifecycle as Claude)."""
    if ctx.evaluator or ctx.role == "evaluator":
        raise ValueError("runtime cursor: evaluator/skilleval stays on Claude")

    hq = ctx.role in ch.HQ_ROLES
    ws = ch.policy_workspace(ctx.home, ctx.name, ctx.cwd)
    prepared = ch.materialize_cursor_policy(
        ws,
        ctx.role,
        hooks_dir=ctx.hooks_dir,
        claude_settings=ctx.settings,
        system_prompt=ctx.prompt_text,
        hq=hq,
    )
    binary = ch.find_agent_binary()
    if not binary:
        raise FileNotFoundError(
            "cursor runtime: agent/cursor-agent not on PATH; "
            "install from https://cursor.com/docs/cli/installation"
        )

    add_dirs = list(ctx.add_dirs)
    cwd_r, ws_r = Path(ctx.cwd).resolve(), ws.resolve()
    if cwd_r != ws_r and str(cwd_r) not in add_dirs:
        add_dirs.insert(0, str(cwd_r))
    home_r = Path(ctx.home).resolve()
    if str(home_r) not in add_dirs and home_r != ws_r:
        add_dirs.append(str(home_r))

    argv = [
        "--workspace", str(ws_r),
        "--model", ch.map_model(ctx.model),
        "--trust",
        "--force",
    ]
    if ctx.sandbox:
        argv += ["--sandbox", "enabled"]
    if ctx.resume:
        argv += ["--resume", ctx.resume]
    for d in ctx.plugin_dirs:
        argv += ["--plugin-dir", d]
    for d in add_dirs:
        argv += ["--add-dir", d]
    sid = ctx.resume or ctx.session_id or str(uuid.uuid4())
    notes = [
        "cursor uses herdr --kind cursor (same place/prompt/tick path as Claude)",
        f"policy materialised into {ws_r}",
    ]
    if ctx.sandbox:
        notes.append("Cursor --sandbox enabled (Claude nsbox not used)")
    if hq:
        notes.append("HQ role: adapter --hq")
    return SpawnPlan(
        runtime="cursor",
        argv=argv,
        herdr_kind="cursor",
        launch="herdr-kind",
        binary=binary,
        notes=notes,
        prepared=prepared,
        session_id=sid,
        workspace=ws_r,
    )


def launch_subprocess(plan: SpawnPlan, *, cwd: Path, env: dict, log_path: Path) -> subprocess.Popen:
    """Start a non-herdr runtime; stdout/stderr → log_path. Caller records pid."""
    log_path.parent.mkdir(parents=True, exist_ok=True)
    log = open(log_path, "ab", buffering=0)
    return subprocess.Popen(
        plan.argv,
        cwd=str(cwd),
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=log,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )


def works_matrix() -> list[dict]:
    rows = ch.works_needs_work()
    rows.insert(0, {
        "item": "Multi-runtime registry (claude default, cursor extension)",
        "status": "WORKS",
        "detail": "scout.agent_runtime.resolve_runtime / plan_spawn; *=cursor for whole factory",
    })
    return rows
