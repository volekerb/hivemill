---
name: research-brief
description: Turn a research question into a written brief before any searching. Use when a research task starts, when the question is broad or ambiguous, or before you open the first source.
---

Purpose: fix scope, method and exit criteria first, so the research stays short and answers the question that was asked.

Follow these steps in order.

1. Read the task: `factory task show <own task> --full`. Restate the question in your own words in one line.
2. If the question is ambiguous (two plausible readings, no decision it feeds, no stated audience), ask before researching: `factory msg lead:<team> "research question unclear" "<the two readings and your default>" --kind request`. Wait for the answer.
3. Split the question into numbered sub-questions (Q1, Q2, ...). Each must be answerable with evidence.
4. Write scope and non-goals: what you will cover and what you will not touch.
5. Write the source plan: which kinds of sources per sub-question (official docs, source repos, papers, our own repo), and in what order.
6. Pick one method: desk research, comparison (skill tool-comparison), benchmark (skill benchmark-protocol) or plugin evaluation (skill plugin-evaluation).
7. Write exit criteria: the evidence that lets you answer each sub-question, and what counts as "not found".
8. Write a time box (for example 45 minutes, at most 12 sources). When it is spent, stop searching and say so in Limits.
9. Record the brief first: `factory task note <own task> -` with the full brief as the heredoc body.
10. Research against the brief. Rate every source with skill source-evaluation; write the result with skill report-writing.

Stop and ask your lead when: the question changes while you work, the method needs a tool or network access you do not have, or the time box is spent with sub-questions still open.
