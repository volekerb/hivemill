---
name: research-flow
description: Run a research task from researcher to evidence review to the director's report. Use when a researcher task is ready, finishes, or its evidence review returns PASS or FAIL.
---

# Research flow

Research tasks have no PR. Evidence review replaces the PR review.

1. Spawn the researcher: `factory spawn researcher --goal G --task T-x`.
2. When it finishes, read the report note: `factory task show T-x --full`. Check the seven headings are there.
3. If the pm did not add the review, add it: `factory task add --goal G --team <team> --role researcher --after T-x` titled "Evidence review of T-x". A fresh researcher does the review, never the researcher who wrote the report. Spawn it: `factory spawn researcher --goal G --task T-y`.
4. The reviewing researcher's done summary starts with `evidence-review: PASS` or `evidence-review: FAIL`.
5. PASS: send the report up, unchanged:
   `factory msg director "research T-x: <one-line answer>" - --kind report --goal G`
   Body = the report: Answer, Recommendation, Proposed follow-ups, Evidence (every [Sn] entry). Never drop the Evidence list.
6. FAIL: reopen the research task with the review's numbered gaps as the reason: `factory task reopen T-x --reason "<numbered gaps>"` (--reason is required; the value is used raw, not read from stdin). Spawn the researcher again.
7. After the researcher finishes the retry, reopen the review task too: `factory task reopen T-y --reason "re-review after retry <n>"` (T-y = the evidence-review task). Spawn a fresh researcher on it (`factory spawn researcher --goal G --task T-y`), not the report's author, so every retried report gets a fresh evidence review. Maximum 2 retries; after the third FAIL escalate to the director with the gaps.
8. Proposed follow-ups stay proposals. Do not add tasks or goals for them.
