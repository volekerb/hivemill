# Releasing

How a Hivemill release is made, who does each step, and how a new feature reaches users. The main branch moves fast; a release is a tag on one chosen commit of it.

## Versioning

Tags are `vMAJOR.MINOR.PATCH` only. There is no `-rc` or other suffix before 1.0: a release candidate is a commit SHA, not a tag.

While the version is 0.x:

- MINOR is for new features, behaviour changes, flag promotions and breaking changes. A breaking change gets an Upgrade note in the release notes.
- PATCH is for fixes only.

Every 0.x tag is a GitHub pre-release; `GOTOOLCHAIN=local go run ./cmd/ci-changelog --prerelease 0.3.0`, run from `go/`, prints `true`.

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

Or, from G-048 P5c: `go run ./cmd/ci-release-check <sha> [--version X.Y.Z] [--repo OWNER/NAME]` from `go/` (same checks, same output, same exit codes). It also prints one `release-check: python under <dir>: <n>` line for each of `bin/`, `roles/hooks/`, `web/`, `scout/`, `retro/` and `plugins/hivemill/scripts/` (the v0.6.0 rule: no Python there). The count is a warning until stage P7; `--require-no-python` turns a non-zero count into a failure. `go run ./cmd/ci-release-check --repo <checkout dir>` runs only that count.

1. `on-main`: the SHA is on the first-parent history of origin/main.
2. `version`: the version is `X.Y.Z` and equals `VERSION` at the SHA.
3. `ci-green`: a completed, successful `tests.yml` push run exists for the SHA (push runs on main run the full suite).
4. `notes`: `GOTOOLCHAIN=local go run ./cmd/ci-changelog --check-version VERSION` (from `go/`) passes and the release notes contain 0 placeholders (the FAIL line names each hit with its line number).
5. `fresh-install-smoke`: from a detached checkout of the SHA, with a temporary `HOME` and `FACTORY_HOME`, `factory --version` prints `hivemill <version>`, `ci/demo_seed.py` succeeds and `factory serve` on the demo home answers HTTP 200. The live factory is never touched.
6. `public-text`: the public-text guard passes on the release notes. Without owner terms the guard exits 2 and the check fails; it is never skipped.
7. `tag-free`: `v<version>` is not on origin yet.
8. `cursor-runtime`: `scout/cursor_harness.py`, `roles/hooks/cursor-guard-adapter.py` and `scout/agent_runtime.py` exist at the SHA, and runtime resolution matches the contract (empty `agent_runtimes` → pm is Claude; `*=cursor` → coder is Cursor; evaluator stays Claude).

Exit 0: all checks passed, and the script prints the exact owner commands (fetch, `git tag -a`, `git push` of the tag). Exit 1: a check failed, no commands are printed. Exit 2: usage error or the SHA does not resolve. The script only reads; it never tags, pushes or creates a release.

For a MINOR (patch 0) release it also prints a reminder that the owner runs `ci/freshbox_test.py` (needs `sudo`) before tagging. The automated smoke in check 5 runs for every release; the fresh-box test is for MINOR releases only. The fresh-box run includes `factory cursor-pilot probe --role pm` (hook policy only; it does not launch the Cursor agent binary).

## Cursor runtime owner step

After a fresh install, validating a real Cursor agent session (`cursor-pilot run` or a live pm spawn) may require `agent login` or `CURSOR_API_KEY`. That is normal: the owner performs it when they want Cursor on the box. It is not part of `ci/freshbox_test.py` and does not block release when the probe and release gate pass.

Later merges to main do not invalidate the candidate: the tag points at the SHA.

## Release steps

1. The director files the release PR task. The coder bumps `VERSION`, writes `ci/release-notes/<version>.md` from `ci/release-notes/TEMPLATE.md`, updates the CHANGELOG header constants in `go/internal/citools/changelog/changelog.go` and, while `ci/changelog.py` still exists, in `ci/changelog.py` too (update both), and runs `GOTOOLCHAIN=local go run ./cmd/ci-changelog --write --file ../CHANGELOG.md` from `go/`.
   The same PR pins the installer build (`hivemill-install`, see [Installer binaries](#installer-binaries)): the coder builds the three binaries, writes their sha256 into the `SHA_*` lines of `plugins/hivemill/scripts/install` and sets `VERSION` there to the release version. The release notes `### Upgrade` section names the last ref that still carries `plugins/hivemill/scripts/install.py`, the rollback target.
2. The release PR goes through the normal review, QA and judge merge.
3. The director runs `python3 ci/release_check.py <merge sha>`.
   Or, from G-048 P5c: `go run ./cmd/ci-release-check <merge sha>` from `go/`.
4. The boss sends the owner the exact commands the check printed.
5. The owner pushes the tag (see the next section).
6. `.github/workflows/release.yml` runs on the `v*` tag push, creates the GitHub release from the notes and attaches the `hivemill-install` binaries.
7. The director checks the release page and tells the boss.
8. The owner closes the released milestone and creates the next one, with the commands the director sends (see [ISSUES.md](ISSUES.md)).

## Installer binaries

`go/cmd/hivemill-install` is the installer that `plugins/hivemill/scripts/install` downloads and runs. The shim refuses any binary whose sha256 is not the one written into it, so the release process must produce byte-identical binaries twice: once locally for the release PR and once in `release.yml`. The build is reproducible with the toolchain pinned in `go/go.mod`:

```
cd go
for p in linux/amd64 linux/arm64 darwin/arm64; do
  CGO_ENABLED=0 GOOS="${p%/*}" GOARCH="${p#*/}" go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' \
    -o "../dist/hivemill-install-${p%/*}-${p#*/}" ./cmd/hivemill-install
done
sha256sum ../dist/hivemill-install-*
```

1. In the release PR, run the loop above and write the three hashes into `SHA_LINUX_AMD64`, `SHA_LINUX_ARM64` and `SHA_DARWIN_ARM64` of `plugins/hivemill/scripts/install`, and the version into its `VERSION` line. Until then the lines hold 64 zeros and the shim fails closed.
2. `release.yml` builds the same three binaries with the same flags, prints their sha256, fails if one differs from the value in the shim, and attaches them to the release as `hivemill-install-<os>-<arch>`.
3. After the tag push, the director checks that the three assets are on the release page and that `sha256sum` of a downloaded asset equals the shim's value.

Nothing in the shim trusts the download location: a different hash is a stop, whoever served the file.

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

## Publishing tools (Go ports, G-048 P5c)

The other release-side scripts have Go ports; the Python scripts stay until stage P5e. Run them from `go/`; the flags, output and exit codes are the same.

- Apply the public-repository metadata: `python3 ci/publish_repo.py --repo OWNER/NAME [--apply]`, or, from G-048 P5c: `go run ./cmd/ci-publish-repo --repo OWNER/NAME [--apply]`. The default is a dry run that prints the `gh api` calls.
- Export a ref as one commit and scan it: `python3 ci/fresh_export.py --out DIR --author "Name <email>"`, or, from G-048 P5c: `go run ./cmd/ci-fresh-export --out DIR --author "Name <email>"`.
- Load a `state/` directory into a temp DB: `FACTORY_HOME=<tmp> python3 ci/state_load.py <state-dir>`, or, from G-048 P5c: `FACTORY_HOME=<tmp> go run ./cmd/ci-state-load <state-dir>`.

Until the Go scrub scanner exists (stage P5b), the Go tools that scan (`ci-changelog`, `ci-publish-repo`, `ci-fresh-export`) call `ci/scrub_scan.py` through `python3`; `ci-release-check` still runs `bin/factory`, `ci/demo_seed.py` and `ci/public_text_guard.py` of the checked-out tree the same way. Run them from a factory checkout, or pass `--ci-dir` (`--root` for `ci-changelog`).
