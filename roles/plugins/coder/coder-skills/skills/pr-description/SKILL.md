---
name: pr-description
description: Write a concrete, short PR body in the factory template (Summary, Why, Changes, Test evidence, Risk / rollback). Use when opening or updating a PR, running gh pr create or gh pr edit, or writing or rewriting a PR body.
---

Purpose: a reviewer should grasp the change in one minute. Write like a professional engineer: concrete, terse, checkable. Template: `.github/pull_request_template.md`. Owner request M2502.

Follow these steps in order.

1. Start from the template. Keep the template's five sections in this order: `## Summary`, `## Why`, `## Changes`, `## Test evidence`, `## Risk / rollback`. Add no other section before them.
2. Summary: 1-2 lines, what changed and the effect for the user of the code.
3. Why: the task id (T-xxxx) and one line of motivation.
4. Changes: 3-6 bullets that say what the code now does. Do not restate the diff or list files; the diff already shows them.
5. Test evidence: the exact command and its key output line, e.g. `python3 -m unittest discover -s tests` -> `Ran 1790 ... OK`. State the test count on main and on the branch (branch >= main). For a bug fix or new guard, say the new test failed with the fix removed (coder.md C4).
6. Risk / rollback: what could break, "rollback = revert this PR", and "applies after relaunch" when role files, plugins or settings change.
7. Keep the five sections to 150-250 words target, 300 hard limit. After Risk, add the template's `<details>` block holding the line `## Acceptance walk` (pre-pr-check) and one line per acceptance item; other long evidence (an audit table) goes in its own `<details>` block. Text inside `<details>` is outside the word count.
8. No marketing tone ("robust", "seamless", "comprehensive"), no narration of your process, no restating the task spec.
9. End the body with the footer line `🤖 Generated with [Claude Code](https://claude.com/claude-code)`. The `Co-Authored-By` trailer belongs in the commit message, not the body.
10. Do not add a `## Gates` block. Gates belong in QA and reviewer verdicts (`factory verdict`), not in PR bodies.
11. Pass the text with `gh pr create --body-file <file>` or `gh pr edit --body-file <file>` (coder.md C9). Re-check the body after every push that changes the story.

Examples: docs/pr-descriptions.md.
