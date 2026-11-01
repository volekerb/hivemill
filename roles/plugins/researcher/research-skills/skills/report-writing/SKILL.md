---
name: report-writing
description: Write and deliver the final research report in the fixed factory shape. Use when your research is done, when you are about to run `factory report` or `factory done`, or when asked for findings.
---

Purpose: a report an owner can read in one minute, with every claim traceable to a source.

Follow these steps in order.

1. Use exactly these headings, in this order:
   - `## Question`
   - `## Answer`
   - `## Findings`
   - `## Recommendation`
   - `## Proposed follow-ups`
   - `## Evidence`
   - `## Limits`
2. Question: the question as you understood it, one or two lines, plus the brief note.
3. Answer: a plain-English summary for the owner, at most 10 lines, no jargon, no source ids. It answers the question first.
4. Findings: numbered. Each finding is one claim and ends with its source ids, like `[S1]` or `[S2][S4]`. No finding without a source.
5. Recommendation: what to do, in order, with adopt, adapt or skip where it applies.
6. Proposed follow-ups: tasks you suggest, one per line. You do not create tasks.
7. Evidence: numbered `[Sn]` entries. Each has the URL or command, the date accessed, and primary or secondary. Include raw command output for measurements.
8. Limits: what you did not check, single-source claims, the time box, anything uncertain.
9. Store the full report: `factory task note <own task> -` with the report as the heredoc body.
10. Publish it: `factory report "<one-line answer>" - --goal <G>` with the same body.
11. Finish: `factory done "<one-line answer>; report in task note"`.

Stop and ask your lead when: the answer does not follow from the evidence, or the question changed since the brief.
