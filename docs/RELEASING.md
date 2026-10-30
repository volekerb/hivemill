# Releasing

How a Hivemill release is made, who does each step, and how a new feature reaches users. The main branch moves fast; a release is a tag on one chosen commit of it.

## Versioning

Tags are `vMAJOR.MINOR.PATCH` only. There is no `-rc` or other suffix before 1.0: a release candidate is a commit SHA, not a tag.

While the version is 0.x:

- MINOR is for new features, behaviour changes, flag promotions and breaking changes. A breaking change gets an Upgrade note in the release notes.
- PATCH is for fixes only.

Every 0.x tag is a GitHub pre-release; `python3 ci/changelog.py --prerelease 0.3.0` prints `true`.

The `VERSION` file at the repo root holds the version of the last release PR (added by T-0792). `factory --version` prints it.

## Cadence

Releases are on demand: there is no fixed release train.

A release happens in one of two cases:

- the owner asks for one;
- the boss proposes one because notable user-visible changes have landed. The director flags release candidates in its reports.

Patch releases for urgent fixes happen at any time.

This is the owner decision of 2026-10-30 ("on demand").

## Release candidate

A release candidate is a full 40-character SHA on origin/main (first-parent history) that:

1. has a green `tests.yml` push run (push runs on main run the full suite);
2. contains the release PR for that version;
3. passes `python3 ci/release_check.py <sha>` with exit 0.

Usage, run by the director from the factory checkout:

```
python3 ci/release_check.py <sha> [--version X.Y.Z] [--repo OWNER/NAME]
```

`<sha>` is any commit-ish; it is resolved to the full 40-character SHA. `--version` defaults to `VERSION` at that SHA, `--repo` to the origin remote. The script prints one `PASS <name>` or `FAIL <name>` line per check and runs all of them even after a failure:

1. `on-main`: the SHA is on the first-parent history of origin/main.
2. `version`: the version is `X.Y.Z` and equals `VERSION` at the SHA.
3. `ci-green`: a completed, successful `tests.yml` push run exists for the SHA (push runs on main run the full suite).
4. `notes`: `changelog.py --check-version` passes and the release notes contain 0 placeholders (the FAIL line names each hit with its line number).
5. `fresh-install-smoke`: from a detached checkout of the SHA, with a temporary `HOME` and `FACTORY_HOME`, `factory --version` prints `hivemill <version>`, `ci/demo_seed.py` succeeds and `factory serve` on the demo home answers HTTP 200. The live factory is never touched.
6. `public-text`: the public-text guard passes on the release notes. Without owner terms the guard exits 2 and the check fails; it is never skipped.
7. `tag-free`: `v<version>` is not on origin yet.

Exit 0: all checks passed, and the script prints the exact owner commands (fetch, `git tag -a`, `git push` of the tag). Exit 1: a check failed, no commands are printed. Exit 2: usage error or the SHA does not resolve. The script only reads; it never tags, pushes or creates a release.

For a MINOR (patch 0) release it also prints a reminder that the owner runs `ci/freshbox_test.py` (needs `sudo`) before tagging. The automated smoke in check 5 runs for every release; the fresh-box test is for MINOR releases only.

Later merges to main do not invalidate the candidate: the tag points at the SHA.

## Release steps

1. The director files the release PR task. The coder bumps `VERSION`, writes `ci/release-notes/<version>.md` from `ci/release-notes/TEMPLATE.md`, updates the CHANGELOG header in `ci/changelog.py` and runs `python3 ci/changelog.py --write`.
2. The release PR goes through the normal review, QA and judge merge.
3. The director runs `python3 ci/release_check.py <merge sha>`.
4. The boss sends the owner the exact commands the check printed.
5. The owner pushes the tag (see the next section).
6. `.github/workflows/release.yml` runs on the `v*` tag push and creates the GitHub release from the notes.
7. The director checks the release page and tells the boss.

## Owner tag step

The owner runs, with the full SHA the check passed on:

```
git -C ~/factory fetch origin
git -C ~/factory tag -a vX.Y.Z -m vX.Y.Z <full sha>
git -C ~/factory push origin vX.Y.Z
```

Agents never create tags or releases.

Do not run `gh release create`: the tag push makes release.yml create the release.

## Hotfix

The fix lands on main through the normal PR flow. A release PR then bumps PATCH, and the same gate and the same owner tag step apply.

Main is releasable at any time because unfinished features sit behind default-off flags. If main holds an unreleased change that is not behind a flag and must not ship, the director escalates to the boss. A branch from the previous tag is an owner decision, never a default.

## Feature flags

A flag moves through the states off -> shadow -> on -> removed.

- New features land default off (or shadow).
- A flag becomes default on only in a release PR whose notes name the config key.
- A flag that has been on for two releases is removed (the code path is made unconditional) in a later release, with a note.
- The registry is `bin/flags.json`, listed with `factory flags` (T-0793). A test fails when a config key has no registry entry.
- Local `factory config` changes on one box are not promotions.

## Upgrade and migration notes

Every `ci/release-notes/<version>.md` from 0.2.0 on has a `### Upgrade` section. It states:

- the upgrade command;
- database changes: the CLI migrates the schema on first run, so say so, or say "no schema change";
- config keys added, renamed or removed;
- flags whose default changed.

Users install and upgrade from the latest tag (`docs/SETUP.md`, T-0795). The G-025 one-prompt installer consumes the same "latest vX.Y.Z tag" rule.
