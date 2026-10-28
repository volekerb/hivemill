---
name: spec-change
description: Change a running worker's spec, acceptance or scope safely through the task DB. Use when a running worker's spec, acceptance or scope must change.
---

# Changing a running worker's spec

A running worker does not re-read its task, so a spec delta sent by message is ignored (T-0016 failed 3 attempts this way; M253/M255). Stop the worker, update the task in the DB, respawn.

1. Stop the worker:
   `factory stop <worker> --reason "spec vN"`
2. Wait until `factory agents` no longer shows it live (or the stop fallback ran). Do not edit the task while it is still running.
3. Rewrite the spec, then acceptance in a SEPARATE call (only one argument per call may be `-`):
   ```
   factory task update T --spec - <<'EOF'
   <full new spec>
   EOF
   factory task update T --acceptance - <<'EOF'
   <full new acceptance>
   EOF
   ```
4. Record what changed:
   `factory task note T "spec vN: <what changed>"`
5. Respawn the worker:
   `factory spawn <role> --task T`

Always write the full spec, not a delta. Check with `factory task show T` that both spec and acceptance read correctly before respawning.
