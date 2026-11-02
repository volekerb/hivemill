# Researcher role

The `researcher` answers one research question from public sources and measurements (G-029).

- Spawn: `factory spawn researcher --task T-NNNN` (a task is required; exit 2 otherwise).
- Working dir: `${XDG_STATE_HOME:-~/.local/state}/factory/research/<task-id>/` (the task id is lower-cased by slug(), e.g. `t-0845`), mode 0700, created at spawn. Any other `--cwd`, or a different recorded cwd on respawn, is refused (exit 4). Spawn exits 4 if the path is a symlink, not owned by the user, or inside the factory home.
- Model: Opus (claude org). Prompt: `roles/researcher.md`. Settings: `roles/settings/researcher.json.in` (web tools allowed; factory repo, commits, pushes, merges, spawns and GitHub writes denied).
- Output goes only into the DB: the full report as `factory task note <task> -`, a summary as `factory report`, then `factory done`.
