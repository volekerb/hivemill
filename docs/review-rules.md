# Review, QA and judge prompt rules

`roles/reviewer.md`, `roles/qa.md` and `roles/judge.md` carry short rules that make verdicts evidence-based:

- **Reviewer R1-R7:** severity by behaviour (nits never FAIL), RESOLVED/OPEN/DECLINED re-checks on retries, proof for negative claims, unexplained deletions, a security bypass pass, unannounced CI edits, verbatim third-party text.
- **QA Q1-Q5:** zero or low test counts fail, a guard or fix is shown failing without the fix, retry re-checks, severity, every numbered acceptance item reported.
- **Judge J1-J3:** read every body, rule on cited evidence, reject with "insufficient evidence:" naming who owes it.

`tests/test_prompt_rules_review.py` checks that a marker for each rule id stays in its file and that the `factory verdict` / `factory judge` command lines remain. Prompt changes reach new workers at spawn only.

Source: Review/QA/judge rules: ideas from gAmUssA/coding-policy (review-severity, reviewer-feedback-reading, verify-before-done, ADR 0004) @7a25169 (declares MIT, no LICENSE file); our wording.
