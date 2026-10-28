# Opus reasoning effort

Every agent spawned on an Opus model (boss, director, lead, pm, reviewer, judge, staff-engineer) starts with
`claude --effort <level>`. Sonnet agents get no `--effort` flag. Owner decision: M2741.

- Config key `opus_effort` (default `medium`): `factory config opus_effort high` (director only). Valid levels: low, medium, high, xhigh, max.
  An empty value disables the flag. Any other value makes `factory spawn` refuse.
- One place: `effort_args()` in `bin/factory`, used by `_spawn_start`. `factory relaunch` and queued spawns go through the same path,
  so persistent roles pick up a changed level at their next relaunch.
- Check a live agent: `ps -o args= -p <pid>` shows `--effort medium`.

The level is validated before a spawn creates anything (worktree, workspace, tab, pane), and `factory config opus_effort <bad>` is refused. Only model ids starting with `claude-opus-` get the flag.
