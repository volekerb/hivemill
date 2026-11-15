# CI cost and fork safety

## Policy

- GitHub Actions runs on **free standard hosted runners only** (`ubuntu-latest`, `ubuntu-24.04`, `ubuntu-22.04`). No self-hosted, larger or paid runners, no paid features. The paid Actions budget is **$0**.
- The **local gate** (`factory gate run`) is the factory's merge gate. GitHub required checks are used only while they are free; they are never the only thing standing between a change and `main`.
- Fork pull requests never get secrets or a write token: workflows run on `pull_request` (not `pull_request_target`), with read-only permissions and no secrets other than the built-in `GITHUB_TOKEN`.

## Owner steps (GitHub UI)

Do these once; the factory cannot change GitHub settings.

1. **Budget.** Billing and plans > Budgets and alerts: set the Actions budget to `$0` and tick "Stop usage when budget limit is reached".
2. **Fork PR approval.** Settings > Actions > General > Fork pull request workflows from outside collaborators: choose "Require approval for all outside collaborators".
3. **Token default.** Settings > Actions > General > Workflow permissions: choose "Read repository contents" (read-only).
4. **Private repo before first push.** While the repo is private, disable Actions (Settings > Actions > General > Actions permissions > "Disable actions") before the first push, so no minutes are spent. Enable them after the repo is public.

## Kill switch

This is the kill switch. If CI costs money or misbehaves: remove the required checks in Settings > Branches (branch protection rule for `main`), then disable Actions (Settings > Actions > General > "Disable actions"). The factory keeps merging on the local gate; nothing else changes.

## What `tests/test_ci_policy.py` enforces

It scans `.github/workflows/*.yml|*.yaml`, `ci/public-overlay/.github/workflows/` if present, and `.github/dependabot.yml`. Each failure names `file:line`.

| Rule | Check |
|---|---|
| a | every `runs-on:` is `ubuntu-latest`, `ubuntu-24.04` or `ubuntu-22.04` (no `self-hosted`, no matrix-expanded value) |
| b | no `pull_request_target`, `workflow_run` or `schedule` trigger |
| c | a top-level `permissions:` block that grants only `read`; a job-level `contents: write` is allowed only when the workflow triggers on `push: tags` and/or `workflow_dispatch` |
| d | no `secrets.` reference except `secrets.GITHUB_TOKEN` (`github.token` is fine); no `secrets: inherit` |
| e | no `github.event.pull_request.*`, `github.event.issue.*` or `github.head_ref` expression inside a `run:` block (use `env:`), in dot or bracket notation (`github['head_ref']`) and inside `format()` |
| f | `dependabot.yml` has only the `github-actions` ecosystem on a `weekly` interval |
| g | every step and job-level (reusable workflow) `uses:` is `OWNER/REPO[/path]@ref`, owner `actions`, `github` or `volekerb`, ref a version tag (`v4`) or a 40-char sha; `./` local paths are fine |

## Preflight: fail in a minute, before the heavy jobs start (T-1593)

Free runners are a shared, limited pool: a full `tests.yml` run starts 21 to 27 jobs (about 150 job-minutes), and a run that is going to fail anyway takes those slots from PRs that could merge. The `preflight` job spends about one job-minute to stop such a run early.

- **What it runs.** The test files listed in [`ci/preflight.txt`](../ci/preflight.txt), one `python -m unittest discover -s tests -p <file>` per file, on `ubuntu-latest` with a 5 minute cap. It runs after `ci-tier` in every tier (docs, affected, full) and on push runs on `main`.
- **What it gates.** `unittest-shard`, `go`, `guard-unittest-shard`, `guard-diff`, `guard-self-compare`, `guard-evaluator-home-loosen`, `guard-equiv` and `guard-equiv-rest` have `needs: [ci-tier, preflight]`. When preflight fails or is cancelled they are skipped, so the run costs a few job-minutes instead of about 150. `docs-check` does not wait for preflight: on a docs-only PR the two run side by side.
- **The required check stays `unittest`.** `preflight` is not a required check. The `unittest` job needs it and fails first when its result is anything but `success` (failed, cancelled, skipped), in every tier including the docs lane, so skipped heavy jobs can never show up as a green PR. After a red preflight the `guard-unittest` and `guard-equiv` fan-in jobs also report failure (their shards were skipped); the cause is the preflight log.
- **Nothing leaves the full suite.** Every preflight file still runs in its unittest shard (the shard matrix and `SHARDS` are unchanged). On a green run the cost is one more job start and its test time before the heavy jobs begin.
- **How the list is chosen.** From the last 100 failed pull request runs: a test file goes in when it failed at least 3 of them and runs in under 20 s; `tests/test_docs_index.py` is always in. Keep the total under 60 s of test time. The numbers behind the current list are in the comment at the top of `ci/preflight.txt`. To re-measure: `gh run list --workflow tests.yml --event pull_request --status failure --limit 100 --json databaseId`, then `gh run view <id> --json jobs` and `gh run view <id> --log-failed` per run.
- **Reproduce a red preflight locally.** The job log ends each failing file with the command: `python3 -m unittest discover -s tests -p <file>`.
