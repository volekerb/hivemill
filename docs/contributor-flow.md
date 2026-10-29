# Contributor flow: external pull requests

How a pull request from a fork gets from "opened" to "merged" without running untrusted code on the factory box and without exposing secrets. Contributors only need the first section; the rest is for the director, reviewer, QA and judge.

## What a contributor sees

Fork, branch, open a normal pull request against `main` ([CONTRIBUTING.md](../CONTRIBUTING.md)). A maintainer reads it first. If it passes that read, it goes through the same reviewer, QA and judge gate as every factory PR, and is merged as you wrote it.

## 1. Intake: read pass before anything runs

The local gate ([local-gate.md](local-gate.md)) executes PR code on the factory machine, so nothing from a fork may reach it before a human-grade read.

1. The director sees the external PR.
2. A reviewer does a **read pass** first: it reads the diff and nothing else. No checkout that is run, no tests, no gate, no scripts from the PR.
3. Only after a reviewer PASS on the read pass does the director file a factory task with `--pr <url>`. Filing that task is the **ok-to-test**: it is what lets the local gate and QA run the code.
4. QA runs fork code only inside the worker sandbox ([worker-sandbox.md](worker-sandbox.md)) when the sandbox is on for qa; otherwise only after the read pass above.

A PR without a task is never executed. A reviewer FAIL on the read pass ends the flow: the director answers the contributor (section 4) and no task is filed.

## 2. Gate

Same as for factory PRs:

- a reviewer verdict and a QA verdict, then a judge merge ([review-workflow.md](review-workflow.md));
- the local gate passes on the exact head sha;
- a change of the head sha resets the verdicts, so a contributor push after review is read and tested again ([verdict-gates.md](verdict-gates.md)).

The read pass counts as an early look, not as the reviewer verdict for the final head.

## 3. Fork safety

- Workflows use the `pull_request` trigger. `pull_request_target` and `workflow_run` are forbidden: they run with the base repository's token and secrets against fork-controlled input.
- The `GITHUB_TOKEN` is read-only and workflows reference no secrets, so a fork run has nothing to leak.
- The owner sets GitHub > Settings > Actions > General > fork pull request workflows to "Require approval for all outside collaborators".
- `tests/test_ci_policy.py` enforces the trigger, permission and secret rules on every workflow file; see [ci-cost.md](ci-cost.md).

## 4. Public text

Everything an agent writes on an external PR is public.

- Every reply, comment, review and PR edit passes the public text guard ([public-text-guard.md](public-text-guard.md)) before it is posted.
- Agents never quote private factory data to contributors: no mail, no DB contents, no local paths, hostnames or addresses.
- Factory ids (`T-`, `M-`, `G-`) are fine to mention.

## 5. Credit

An external PR that passes is squash-merged as is, with the contributor as author. The factory never re-authors, rewrites or re-opens it under another identity. If it needs changes, the contributor makes them.
