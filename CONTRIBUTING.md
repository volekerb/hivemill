# Contributing

## How work flows here

The factory builds itself. Its own changes come from goals and tasks run by agents (pm, coder, qa, reviewer, judge); each task becomes one PR on a `factory/<task-id>` branch. Outside contributors do not need any of that: fork, branch, and open a normal pull request against `main`. A maintainer (GitHub handle `volekerb`) reviews it.

External pull requests get the same reviewer, QA and judge gate as the factory's own, and nothing in them runs before a reviewer has read the diff. The details are in [docs/contributor-flow.md](docs/contributor-flow.md).

Open an issue first for anything larger than a small fix, so the change is agreed before you build it. Security problems do not go in issues: see [SECURITY.md](SECURITY.md).

## Dev setup

Python standard library only, no packages to install. Run the whole suite from the repository root:

```
python3 -m unittest discover -s tests
```

The suite is offline. It runs `bin/factory` against a throwaway `FACTORY_HOME` with `herdr` and `gh` stubbed (`tests/stubs/`) and never touches a real factory. A run that reports 0 tests is a failure. To run one file: `python3 -m unittest discover -s tests -p test_X.py`.

## Rules

Read these before opening a PR. They are linked, not copied, so they stay current:

- [docs/coder-rules.md](docs/coder-rules.md): the coder quality bar (deterministic outcome tests, no live-home paths, fail-closed errors, out-of-scope finds reported rather than fixed).
- [docs/review-rules.md](docs/review-rules.md): what a reviewer checks and what blocks a merge.
- [docs/tests-and-ci.md](docs/tests-and-ci.md): the test suite, the CI workflow and the test-count gate.
- [docs/pr-descriptions.md](docs/pr-descriptions.md): the PR body standard; `.github/pull_request_template.md` carries it.

## One task, one PR

- One change per PR. No drive-by refactors; list unrelated problems you notice in the PR body.
- Document a feature in `docs/<feature>.md` (a new file, or that feature's existing file), not as a new README section.
- Your branch must not run fewer tests than `main`.
- Before opening a PR that touches install or setup, run `sudo python3 ci/freshbox_test.py --repo <your fork> --ref <your branch>` (see [Test a fresh install](docs/SETUP.md#test-a-fresh-install)) and paste its `RESULT` summary line.

## Guard changes

A guard is a hook rule, a settings deny rule, a CLI refusal or a CI check. Every new or changed guard needs a test it must reject and a test it must accept, both run through the real entry point (the hook as invoked, the CLI as a subprocess). See [docs/planning-rules.md](docs/planning-rules.md). Do not loosen a guard without saying so in the PR body.

## Good first issues

The `good first issue` label marks a small, self-contained gap: one file or one doc, acceptance you can check from the issue text, and no design decision left open. Pick one nobody has claimed, comment that you are taking it, and keep to the [One task, one PR](#one-task-one-pr) rule: one issue, one pull request, nothing extra. Run `python3 -m unittest discover -s tests` from the repository root before you push; your branch must not run fewer tests than `main`. Drafts of the first issues are in [docs/STARTER_ISSUES.md](docs/STARTER_ISSUES.md); the repository is `https://github.com/volekerb/hivemill`.

## Licence

There is no CLA. By submitting a contribution you license it under the [MIT licence](LICENSE) of this project.

## Conduct

Participation is covered by the [Code of Conduct](CODE_OF_CONDUCT.md).
