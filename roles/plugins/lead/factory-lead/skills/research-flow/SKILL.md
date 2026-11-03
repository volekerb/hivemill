---
name: research-flow
description: Run a research task from researcher to evidence review to the director's report. Use when a researcher task is ready, finishes, or its evidence review returns PASS or FAIL.
---

# Research flow

Research tasks have no PR. Evidence review replaces the PR review.

1. Spawn the researcher: `factory spawn researcher --goal G --task T-x`.
2. When it finishes, read the report note: `factory task show T-x --full`. Check the seven headings are there.
3. If the pm did not add the review, add it: `factory task add --goal G --team <team> --role reviewer --after T-x` titled "Evidence review of T-x". Spawn the reviewer.
4. The reviewer's done summary starts with `evidence-review: PASS` or `evidence-review: FAIL`.
5. PASS: send the report up, unchanged:
   `factory msg director "research T-x: <one-line answer>" - --kind report --goal G`
   Body = the report: Answer, Recommendation, Proposed follow-ups, Evidence (every [Sn] entry). Never drop the Evidence list.
6. FAIL: add the reviewer's numbered gaps as a note (`factory task note T-x -`), then `factory task reopen T-x` and spawn the researcher again. Maximum 2 retries; after the third FAIL escalate to the director with the gaps.
7. Proposed follow-ups stay proposals. Do not add tasks or goals for them.
