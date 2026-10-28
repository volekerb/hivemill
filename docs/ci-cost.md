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

It scans `.github/workflows/*.yml|*.yaml`, `ci/public-overlay/.github/workflows/` if present, and `ci/public-overlay/.github/dependabot.yml`. Each failure names `file:line`.

| Rule | Check |
|---|---|
| a | every `runs-on:` is `ubuntu-latest`, `ubuntu-24.04` or `ubuntu-22.04` (no `self-hosted`, no matrix-expanded value) |
| b | no `pull_request_target`, `workflow_run` or `schedule` trigger |
| c | a top-level `permissions:` block that grants only `read`; a job-level `contents: write` is allowed only when the workflow triggers on `push: tags` and/or `workflow_dispatch` |
| d | no `secrets.` reference except `secrets.GITHUB_TOKEN` (`github.token` is fine); no `secrets: inherit` |
| e | no `github.event.pull_request.*`, `github.event.issue.*` or `github.head_ref` expression inside a `run:` block (use `env:`) |
| f | `dependabot.yml` has only the `github-actions` ecosystem on a `weekly` interval |
