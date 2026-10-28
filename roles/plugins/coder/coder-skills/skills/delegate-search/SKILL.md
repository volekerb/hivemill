---
name: delegate-search
description: Delegate a broad code search to one Explore subagent so file dumps stay out of your context. Use when a question needs more than about 3 searches or files you have not identified yet ("where is X handled", "what calls Y", "which tests cover Z").
---

Purpose: every Grep/Glob/Read dump stays in your context and is re-read on every later turn. One Explore subagent does the sweep in its own context and returns only file:line answers.

Follow these steps in order.

1. Check the trigger. Delegate only when the answer needs more than about 3 searches or files you have not identified yet. If you already know the file or symbol, read it directly; do not delegate.
2. Launch exactly one call with the Agent tool:
   `Agent(subagent_type: "Explore", model: "sonnet", description: "<3-5 words>", prompt: "<question> Start from <paths>. Answer with file:line entries only, one line each, no code dumps. Medium thoroughness.")`
3. Wait for the answer. Do not start another Explore, and do not repeat the same search yourself meanwhile.
4. Treat the answer as a lead, not proof: read the cited lines before relying on them.

Rules:
- At most 1 Explore at a time (the box has 5.7 GB RAM; `_common.md` allows 3 subagents, this skill uses 1).
- Never delegate edits, tests, git or factory commands. Explore is read-only search; its Bash calls still pass through the worker guard.
- One narrow question per call, with the paths to start from. A vague prompt returns a vague dump.

Worked example (this repo). Question: "Who produces and who checks the `Drift:` digest line?" Prompt: "Find where the `Drift:` line of the goal digest is built and which tests assert it. Start from bin/factory and tests/. Answer with file:line only." Expected kind of answer:
- bin/factory:3245: builds the `Drift:` line from `drift_items`
- bin/factory:3604: `drift_items` (the drift rules)
- tests/test_drift.py:283: asserts `Drift: none`
- tests/test_drift.py:293: asserts a D2 + D4 line
Then open bin/factory:3245 and read it before editing anything.
