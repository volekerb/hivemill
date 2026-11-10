"""Cursor Agent CLI harness for Hivemill (docs/runtimes.md, docs/research/cursor-harness.md).

Materializes per-role Cursor policy into an isolated workspace and builds spawn argv.
factory_cli opts in via config agent_runtimes (e.g. *=cursor or pm=cursor).
"""
from __future__ import annotations

import json
import os
import re
import shlex
import shutil
from pathlib import Path

# Every factory role may use Cursor except the skilleval evaluator (Claude-shaped sandbox).
CURSOR_ROLES = frozenset({
    "boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner",
    "staff-engineer", "scout",
})
# Legacy name used by cursor_pilot_roles / older docs.
PILOT_ROLES = CURSOR_ROLES
HQ_ROLES = frozenset({"boss", "director", "lead"})

ADAPTER_NAME = "cursor-guard-adapter.py"
GUARD_NAME = "worker-guard.py"


def parse_pilot_roles(value: str) -> list[str]:
    """Normalise cursor_pilot_roles config; empty = off. Unknown names raise ValueError."""
    names = [x.strip() for x in (value or "").split(",") if x.strip()]
    bad = [n for n in names if n not in CURSOR_ROLES]
    if bad:
        raise ValueError(
            f"cursor_pilot_roles: {', '.join(repr(b) for b in bad)} not in cursor allow-list "
            f"({', '.join(sorted(CURSOR_ROLES))})"
        )
    return list(dict.fromkeys(names))


def role_uses_cursor(value: str, role: str) -> bool:
    try:
        return role in parse_pilot_roles(value)
    except ValueError:
        return False


def translate_permission_token(token: str) -> str | None:
    """Map one Claude Code permission string to a Cursor CLI token, or None to drop."""
    if not isinstance(token, str) or not token:
        return None
    if token in ("Advisor",) or token.startswith("Workflow("):
        return None
    if token in ("Edit", "Write", "NotebookEdit"):
        return "Write(**)"  # deny all writes (pm posture)
    m = re.match(r"^(Bash|Edit|Write|NotebookEdit|Read)\((.*)\)\s*$", token)
    if not m:
        return None
    kind, inner = m.group(1), m.group(2)
    if kind == "Bash":
        return f"Shell({inner})"
    if kind in ("Edit", "Write", "NotebookEdit"):
        return f"Write({inner})"
    if kind == "Read":
        return f"Read({inner})"
    return None


def translate_permissions(claude_perms: dict) -> dict:
    """Claude settings permissions → Cursor cli.json permissions."""
    out = {"allow": [], "deny": []}
    for side in ("allow", "deny"):
        seen = set()
        for tok in claude_perms.get(side) or []:
            t = translate_permission_token(tok)
            if t and t not in seen:
                seen.add(t)
                out[side].append(t)
    return out


def hooks_dir_for_repo(repo_root: Path) -> Path:
    return Path(repo_root) / "roles" / "hooks"


def adapter_command(hooks_dir: Path, role: str, event: str, hq: bool = False) -> str:
    adapter = hooks_dir / ADAPTER_NAME
    parts = ["python3", str(adapter.resolve())]
    if hq:
        parts.append("--hq")
    # HQ hooks pass --role so worker-guard can enforce lead goal-show scope (T-0822); MCP needs role for boss-only MCP.
    parts.extend(["--role", role])
    parts.extend(["--event", event])
    return " ".join(shlex.quote(p) for p in parts)


def cursor_hooks_json(hooks_dir: Path, role: str, hq: bool = False) -> dict:
    """Cursor-native hooks.json that always fail-closed into the adapter."""
    shell_cmd = adapter_command(hooks_dir, role, "beforeShellExecution", hq=hq)
    tool_cmd = adapter_command(hooks_dir, role, "preToolUse", hq=hq)
    mcp_cmd = adapter_command(hooks_dir, role, "beforeMCPExecution", hq=hq)
    return {
        "version": 1,
        "hooks": {
            "beforeShellExecution": [
                {"command": shell_cmd, "failClosed": True},
            ],
            "beforeMCPExecution": [
                {"command": mcp_cmd, "failClosed": True},
            ],
            "preToolUse": [
                {
                    "command": tool_cmd,
                    "failClosed": True,
                    "matcher": "Shell|Write|Delete|Read|Grep|StrReplace|EditNotebook",
                },
            ],
        },
    }


def write_atomic(path: Path, text: str, mode: int = 0o644) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        tmp.write_text(text)
        os.chmod(tmp, mode)
        os.replace(tmp, path)
    finally:
        tmp.unlink(missing_ok=True)


def worktrees_root(home: Path | str) -> Path:
    return Path(home).resolve() / "worktrees"


def is_factory_worktree_path(home: Path | str, path: Path | str) -> bool:
    """True when path is FACTORY_HOME/worktrees or any path under it (task worktree or clone)."""
    home_r = Path(home).resolve()
    path_r = Path(path).resolve()
    wt = worktrees_root(home_r)
    if path_r == wt:
        return True
    try:
        path_r.relative_to(wt)
    except ValueError:
        return False
    return True


def state_root() -> Path:
    """XDG state dir (env XDG_STATE_HOME, else ~/.local/state)."""
    xdg = os.environ.get("XDG_STATE_HOME")
    return Path(xdg) if xdg else Path.home() / ".local" / "state"


def nearest_git_holder(path: Path | str) -> Path | None:
    """Nearest directory (path itself or an ancestor) holding a .git file or dir; None when none.

    Pure filesystem walk, no git subprocess."""
    p = Path(path).resolve()
    for d in (p, *p.parents):
        if (d / ".git").exists() or (d / ".git").is_symlink():
            return d
    return None


def inside_git_worktree(path: Path | str) -> bool:
    """True when path or any ancestor has a .git (Cursor would resolve the project to that top level)."""
    return nearest_git_holder(path) is not None


def _is_under(path: Path, root: Path) -> bool:
    return path == root or root in path.parents


def isolated_policy_dir(home: Path | str, name: str) -> Path:
    """STATE/factory/cursor/<agent name>/ (0700): per-agent policy, outside every git work tree.

    Cursor resolves the project to the git top level and loads hooks from there, so the dir must not
    sit inside FACTORY_HOME (a git work tree) or any task worktree."""
    home_r = Path(home).resolve()
    ws = (state_root() / "factory" / "cursor" / name).resolve()
    if _is_under(ws, home_r) or inside_git_worktree(ws):
        raise ValueError(
            f"cursor policy dir {ws} is inside a git work tree or FACTORY_HOME; Cursor would not load its hooks. "
            "Set XDG_STATE_HOME to a directory outside every git work tree (default ~/.local/state)."
        )
    ws.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(ws, 0o700)
    return ws


def policy_workspace(home: Path | str, name: str, cwd: Path | str) -> Path:
    """Where .cursor/ policy is written.

    HQ cwd (FACTORY_HOME) and any task worktree cwd → (XDG_STATE_HOME or ~/.local/state)/factory/cursor/<name> (isolated).
    Other cwds (e.g. cursor-pilot --cwd temp dir) → that directory unchanged.
    """
    home_r = Path(home).resolve()
    cwd_r = Path(cwd).resolve()
    if cwd_r == home_r or is_factory_worktree_path(home_r, cwd_r):
        return isolated_policy_dir(home_r, name)
    return cwd_r


def assert_policy_target_allowed(home: Path | str, target: Path | str) -> None:
    """Refuse to materialize Cursor policy into a shared checkout or task worktree."""
    home_r = Path(home).resolve()
    path_r = Path(target).resolve()
    if is_factory_worktree_path(home_r, path_r):
        raise ValueError(
            f"cursor policy must not be written into a factory worktree ({path_r}); "
            f"use policy_workspace() and --add-dir for the task tree"
        )
    if _is_under(path_r, home_r):
        raise ValueError(
            f"cursor policy must not be written under FACTORY_HOME ({path_r}); "
            f"use STATE/factory/cursor/<agent> via policy_workspace() (XDG_STATE_HOME)"
        )
    holder = nearest_git_holder(path_r)
    if holder is not None:
        raise ValueError(
            f"cursor policy must not be written inside a git work tree ({path_r}, git root {holder}); "
            "Cursor would load hooks from the git top level instead. Use a dir outside it (XDG_STATE_HOME)"
        )


def add_dir_root_has_policy_leak(path: Path | str) -> bool:
    """True when an --add-dir root would expose .cursor/ or AGENTS.md to Cursor multi-root hooks."""
    p = Path(path).resolve()
    return (p / ".cursor").exists() or (p / "AGENTS.md").is_file()


def assert_add_dir_roots_clean(add_dirs: list[str]) -> None:
    """Refuse spawn when any --add-dir root carries pilot policy files (HQ multi-root hook leak)."""
    for raw in add_dirs:
        p = Path(raw).resolve()
        if add_dir_root_has_policy_leak(p):
            raise ValueError(
                f"cursor spawn must not --add-dir a root with .cursor/ or AGENTS.md ({p}); "
                "one-off owner cleanup: remove ~/factory/.cursor and ~/factory/AGENTS.md"
            )


def add_dirs_from_cursor_argv(argv: list[str]) -> list[str]:
    out = []
    for i, x in enumerate(argv):
        if x == "--add-dir" and i + 1 < len(argv):
            out.append(argv[i + 1])
    return out


def materialize_cursor_policy(
    cwd: Path | str,
    role: str,
    *,
    hooks_dir: Path,
    claude_settings: dict,
    system_prompt: str,
    hq: bool = False,
    factory_home: Path | str | None = None,
) -> dict:
    """Write .cursor/{hooks.json,cli.json,rules/...} and AGENTS.md under cwd.

    Returns a status dict: paths written and translation notes.
    """
    cwd = Path(cwd)
    home = factory_home if factory_home is not None else os.environ.get("FACTORY_HOME")
    if home:
        assert_policy_target_allowed(home, cwd)
    cwd.mkdir(parents=True, exist_ok=True)
    notes = []
    perms = translate_permissions(claude_settings.get("permissions") or {})
    if not perms["deny"] and (claude_settings.get("permissions") or {}).get("deny"):
        notes.append("all Claude deny rules dropped in translation (check Advisor/Workflow-only templates)")

    cursor_dir = cwd / ".cursor"
    hooks_path = cursor_dir / "hooks.json"
    cli_path = cursor_dir / "cli.json"
    rule_path = cursor_dir / "rules" / "factory-role.mdc"
    agents_path = cwd / "AGENTS.md"

    write_atomic(hooks_path, json.dumps(cursor_hooks_json(hooks_dir, role, hq=hq), indent=2) + "\n")
    cli_body = {
        "permissions": perms,
        "attribution": {
            "attributeCommitsToAgent": False,
            "attributePRsToAgent": False,
        },
    }
    write_atomic(cli_path, json.dumps(cli_body, indent=2) + "\n")

    rule_body = (
        "---\n"
        "description: Hivemill factory role prompt (cursor runtime)\n"
        "alwaysApply: true\n"
        "---\n\n"
        f"{system_prompt.rstrip()}\n"
    )
    write_atomic(rule_path, rule_body)
    write_atomic(
        agents_path,
        f"# Factory role: {role}\n\n"
        f"Follow `.cursor/rules/factory-role.mdc`. Guard hooks are in `.cursor/hooks.json` "
        f"(worker-guard via {ADAPTER_NAME}).\n",
    )

    mcp_path = cursor_dir / "mcp.json"
    write_atomic(mcp_path, json.dumps({"mcpServers": {}}, indent=2) + "\n")
    notes.append("wrote empty .cursor/mcp.json; user ~/.cursor/mcp.json may still load")

    adapter = hooks_dir / ADAPTER_NAME
    guard = hooks_dir / GUARD_NAME
    if not adapter.is_file():
        notes.append(f"MISSING adapter at {adapter}")
    if not guard.is_file():
        notes.append(f"MISSING worker-guard at {guard}")

    return {
        "cwd": str(cwd.resolve()),
        "role": role,
        "hq": hq,
        "hooks": str(hooks_path),
        "cli": str(cli_path),
        "rule": str(rule_path),
        "agents": str(agents_path),
        "permissions": perms,
        "notes": notes,
    }


def find_agent_binary() -> str | None:
    return shutil.which("agent") or shutil.which("cursor-agent")


def map_model(claude_model: str) -> str:
    """Map factory MODELS ids to Cursor --model slugs.

    Pass through Anthropic-family ids Cursor accepts; fall back to auto when empty.
    """
    return (claude_model or "").strip() or "auto"


def build_cursor_argv(
    *,
    cwd: Path | str,
    model: str,
    prompt: str | None = None,
    resume: str | None = None,
    plugin_dirs: list[str] | None = None,
    add_dirs: list[str] | None = None,
    print_mode: bool = True,
    force: bool = True,
    sandbox: bool = False,
) -> list[str]:
    """Argv after the agent binary for a pilot/headless run."""
    bin_name = find_agent_binary() or "agent"
    argv = [bin_name, "--workspace", str(Path(cwd).resolve()), "--model", map_model(model), "--trust"]
    if print_mode:
        argv.append("--print")
        argv.extend(["--output-format", "text"])
    if force:
        argv.append("--force")
    if sandbox:
        argv.extend(["--sandbox", "enabled"])
    if resume:
        argv.extend(["--resume", resume])
    for d in plugin_dirs or []:
        argv.extend(["--plugin-dir", d])
    for d in add_dirs or []:
        argv.extend(["--add-dir", d])
    if prompt:
        argv.append(prompt)
    return argv


def works_needs_work() -> list[dict]:
    """Static matrix for `factory cursor-pilot status`."""
    agent = find_agent_binary()
    return [
        {"item": "Headless agent CLI installed", "status": "WORKS" if agent else "NEEDS_WORK",
         "detail": agent or "install: curl https://cursor.com/install | bash"},
        {"item": "Materialize .cursor/hooks.json + cli.json + role rule", "status": "WORKS",
         "detail": "scout.cursor_harness.materialize_cursor_policy; HQ and worktrees → STATE/factory/cursor/<name>"},
        {"item": "Shell gate via beforeShellExecution → worker-guard", "status": "WORKS",
         "detail": "cursor-guard-adapter.py; require failClosed:true"},
        {"item": "Write gate via preToolUse → worker-guard", "status": "WORKS",
         "detail": "adapter remaps Write/Delete path keys to file_path"},
        {"item": "Permission deny translation Bash→Shell Edit→Write", "status": "WORKS",
         "detail": "prefix rules remain weaker than the parser (same as Claude)"},
        {"item": "All roles via agent_runtimes=*=cursor", "status": "WORKS",
         "detail": "evaluator stays Claude (skilleval); others allow-listed"},
        {"item": "factory spawn via herdr --kind cursor", "status": "WORKS",
         "detail": "same place/prompt path as Claude; kind=cursor"},
        {"item": "worker ghcred / Cursor --sandbox (no Claude nsbox)", "status": "WORKS",
         "detail": "pane env gets ghcred; --sandbox enabled when sandbox_roles match"},
        {"item": "MCP strict empty (user MCP blind)", "status": "WORKS",
         "detail": "beforeMCPExecution → cursor-guard-adapter (non-boss deny; boss → worker-guard)"},
        {"item": "Process audits expecting claude argv", "status": "NEEDS_WORK",
         "detail": "claude_procs / flagless checks ignore cursor-agent (OK for pure-Cursor)"},
        {"item": "Plugin dir .claude-plugin packs via --plugin-dir", "status": "UNKNOWN",
         "detail": "Cursor has --plugin-dir; Hivemill plugin layout may need packaging tweaks"},
        {"item": "skilleval evaluator on Cursor", "status": "NEEDS_WORK",
         "detail": "evaluator stays Claude-only"},
    ]
