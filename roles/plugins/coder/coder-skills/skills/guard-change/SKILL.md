---
name: guard-change
description: Procedure for changing a guard so the PR fixes the whole bypass class and loosens nothing unplanned. Use when editing roles/hooks/*, a deny rule in roles/settings/*.json, a CLI refusal, or when retrying a PR after a reviewer or QA bypass finding.
---

Purpose: a guard PR must close the bypass class and must not loosen a block that main has (G-013, T-0147, L-026).

Follow these steps in order.

1. Name the bypass CLASS of each finding before coding. One sentence per finding, for example "variable-writing builtin", not "export FOO=herdr".
2. Walk the variant list for each class: quoting and escapes; metacharacters with no spaces (`;x`, `&&x`, `|x`, `$(x)`, backticks); wrappers with operands (env, nohup, timeout, xargs, command, exec, time); path-qualified and glob command words (`/usr/bin/x`, `/usr/b*/x`); variable-writing builtins (export, declare, read, printf -v, for, mapfile, getopts); aliases; functions and script files; write-then-run heredocs; git and gh options that run code.
3. Add every variant you listed to `ci/guard_corpus.json`: blocked forms to `must_block`, realistic look-alikes that must keep working to `must_allow` (commit and PR heredoc bodies, grep/rg, git log with PAGER, factory msg/verdict).
4. Prefer a fail-closed rule for the whole class over enumerating each writer. A check that cannot decide blocks, never allows.
5. Add tests through the real entry point (the hook as invoked, the CLI as a subprocess): one it must reject and one it must accept per rule. Run each new test once with the fix removed and see it fail.
6. After `git fetch origin`, run `python3 ci/guard_diff.py --base origin/main --mode both` and then `python3 ci/guard_diff.py --base origin/main --mode worker --role evaluator`. The first run also covers the role-tagged corpus entries (evaluator FACTORY_HOME class); add `{"cmd": ..., "role": "evaluator"}` entries for a rule that needs a temp FACTORY_HOME.
7. Paste both summary lines and every LOOSENED and TIGHTENED line into the PR body.
8. Expect zero LOOSENED lines. A planned loosening needs the spec to name it. Otherwise it ships as its own task (L-026): do not list it in `--allow-loosen` yourself.
9. On a retry, list the variants you tested per class in the PR body, and say which of them the previous round missed.
10. Merge origin/main and rerun the guard-diff and the tests right before updating the PR.

Stop and tell your lead if a probe command is blocked by your own guard (do not rephrase around it), or if the spec asks for a loosening without naming it.

Sources: T-0147 retro notes 1-4, L-026.
