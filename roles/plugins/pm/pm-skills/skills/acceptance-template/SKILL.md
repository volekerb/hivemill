---
name: acceptance-template
description: Make every acceptance item a runnable command with an expected result, quote wording exactly, and add a RISKS section. Use when writing or amending a task's spec or acceptance.
---

# Acceptance template

Prose acceptance ("works", "is documented") makes QA guess. Each rule below adds a requirement; none loosens plan-quality.

A1 Every acceptance item is `N. <claim> - <runnable command> -> <expected result>`; the result is an exit code, an exact output line, a count, or "empty". A judgement item names the exact file:line or diff hunk to read.
   Good: `3. tests pass - python3 -m unittest discover -s tests -p test_x.py -> exit 0, "OK"`. Bad: `3. tests pass`.

A2 Wording that must survive (rule text, messages, markers) is quoted exactly and checked with `grep -nF '<exact text>' <file>`.
   Good: `grep -nF 'never call the advisor tool' roles/x.md` -> one hit. Bad: "the advisor rule is kept".

A3 "Additions only" or "no other line changes" is a grep: `git diff origin/main -- <file> | grep '^-' | grep -v '^--- a/'` -> empty.
   Bad: "do not change other lines".

A4 Byte and line caps are `wc -c` / `wc -l` commands with the number. A role prompt delta states the headroom (plan-quality P8).
   Good: `wc -c < roles/pm.md` -> at most 4606 (headroom 1362). Bad: "keep the prompt short".

A5 Every spec has a `RISKS:` section: each risk, how it is mitigated, and which acceptance item tests it. A spec without RISKS is not ready.
   Good: `RISKS: (r1) prompt over budget -> item 2 measures it`. Bad: "RISKS: none".

A6 Guards: one blocked and one allowed case through the real entry point (see plan-quality P2; do not restate it), plus a C4 item: remove the fix -> the new test fails.
   Good: `7. remove the fix in a scratch copy, run the test -> FAIL`. Bad: "guard is tested".

## Skeleton (copy, fill, delete what does not apply)

Spec headings:
```
WHY:
BUILD:
CONSUMERS (P1):
GATE NOT WEAKENED:
RISKS:
FILES:
Rules:
```

Acceptance lines:
```
1. <claim> - <command> -> <expected result>
2. <claim> - grep -nF '<exact text>' <file> -> one hit
3. additions only - git diff origin/main -- <file> | grep '^-' | grep -v '^--- a/' -> empty
4. cap - wc -c < <file> -> at most <N>
```

Mandatory coder Rules line, verbatim:

"merge origin/main before opening or updating the PR; test count >= main; renames must not strip test_ prefixes; a denied command stays denied in every form - stop and report; run the coder-skills:pre-pr-check skill before in_review; PR body needs `## Acceptance walk`; never call the 'advisor' tool."
