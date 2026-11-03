---
name: research-plan
description: Plan a research goal or task as researcher tasks with a brief, numbered acceptance and an evidence-review task. Use when a goal asks to research, compare, benchmark or evaluate something, or when you add a research task to a plan.
---

# Research plan

A research task answers questions with cited evidence. It has no PR and no code diff.

1. One research task per question cluster. Questions that need different sources or methods are separate tasks.
2. Add each with `--role researcher`. Do not add the coder Rules line (merge, PR, test-count) to researcher tasks.
3. The spec is the research brief: question, sub-questions (Q1, Q2, ...), scope and non-goals, source types (docs, public repos, papers, sandboxed measurements), method, time box, exit criteria.
4. Acceptance is numbered:
   - one item per sub-question: "Q<n> answered in ## Findings with at least one primary source [Sn]";
   - "report note present with the seven headings" (Question, Answer, Findings, Recommendation, Proposed follow-ups, Evidence, Limits);
   - "evidence review PASS".
5. Add an evidence-review task for a fresh researcher (never the report's author), after the research task: `factory task add --goal G --team <team> --role researcher --after T-x` with the title "Evidence review of T-x" and the research acceptance as its spec.
6. Follow-ups the researcher proposes are not tasks. Only the owner's word, through the boss, turns them into goals.

Check before you hand over: every sub-question has an acceptance line, and the time box is a number.
