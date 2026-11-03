---
name: tool-comparison
description: Compare tools, libraries or approaches against criteria fixed in advance and give an adopt, adapt or skip verdict for each. Use when a task asks which tool to use, or to compare two or more options.
---

Purpose: a fair comparison where the criteria come before the candidates, and the verdict says what adoption would cost.

Follow these steps in order.

1. Write the criteria first, before reading about any tool, and record them: `factory task note <own task> -` with the criteria list as the heredoc body. Weight each criterion high, medium or low.
2. List the candidates and why each is in the list. Add no candidate after the criteria are fixed without noting it.
3. Build a feature matrix as a markdown table: rows are criteria, columns are tools, cells hold a fact and its `[S<n>]` id, not an adjective.
4. Record the licence of each tool (SPDX name) and whether it allows our use.
5. Record maintenance signals: last release date, last commit, open issues and PRs, number of maintainers.
6. Check fit with our stack: languages allowed by `docs/adr/0001-language-policy.md`, and the box limits (8 vCPU, 5.7 GB RAM; see `factory res`). Note network or install needs.
7. Rate each source with skill source-evaluation. Do not rate on a README claim alone.
8. Give each tool a verdict: adopt (use as is), adapt (use with changes or only part), or skip. For each add value, effort and risk as high, medium or low, with one line of reasoning.
9. Name the winner against the criteria and say what would change your mind.
10. Put the table and verdicts into the report with skill report-writing.

Stop and ask your lead when: the criteria depend on a decision you cannot make, or no candidate fits the language policy.
