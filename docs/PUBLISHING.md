# Publishing the factory repository

This is the one approved path to a public copy of this repository. Read it before anyone creates a GitHub repository or changes a visibility setting.

## Owner decision

Relayed by the boss (M3178) and the director (M3179), in the owner's words: "if there were some commits which contain my pricate informatjon or keys or secrets we need to start from scratch. Once we atart we can make it publix."

So the public repository is a **fresh repository with one commit**, built from a tree that our own scanner reports clean. The current private repository's history is never published.

## Conditions for making the fresh repository public

All four must hold before the flip:

1. Our scrub scanner (`ci/scrub_scan.py`) reports 0 hits on the export repository with `--history`: blobs as committed, path names, author, committer, message, ref names and tags of its single commit. Only the reviewed lines in `ci/scrub_history_allowlist.txt` are exempt.
2. Every G-012 success criterion is met.
3. The director has signed off.
4. Before the flip the director receives the scan report, the planned repository name and URL, and the commit author identity. The boss gives the owner a heads-up. The flip happens only on the director's explicit go.

No team member creates the GitHub repository or changes its visibility. The current private repository stays private and untouched, and its history is never pushed.

## Why a fresh repository

The history of the private repository contains data that must not be public. Counts from `python3 ci/scrub_scan.py --history .` (generic patterns only, no owner terms file) at the time this document was written, on the branches present in a normal clone:

| pattern | hits |
|---|---|
| `home-path` (a home directory path of a real user) | 25064 |
| `email` (any address that is not an example or noreply address; this includes the owner's real name/email in author and committer lines) | 6932 |
| `rfc1918` (a 192.168.x.x LAN address) | 264 |
| total | 32260 |

Context for those numbers:

- `git rev-list --all --count`: 3654 commits.
- Commits on `main` whose subject starts with `state:`: 680. They are automated exports of the factory database.
- The `state` branch exists (`origin/state`, 2014 commits). It holds `exports/` and `state/factory.sql`, a full dump of the factory database, in every commit.
- The first scan with an owner terms file will find more (names, handle, hostname). The terms file stays outside the repository; T-0224 refreshes these counts with it at the end of G-012.

Rewriting this history (filter-repo, BFG) is not done: it would leave old objects in clones and forks, and it is hard to prove complete. A new repository has no such objects.

## Procedure

1. **Export.** From a clean checkout of the source repository, with the owner terms file outside the repo:

   ```
   export SCRUB_TERMS_FILE=<path to the owner terms file>
   python3 ci/fresh_export.py --out <new empty dir outside ~/factory> \
     --author "volekerb <11260070+volekerb@users.noreply.github.com>" --ref origin/main \
     --public-repo volekerb/hivemill
   ```

   `--public-repo OWNER/NAME` replaces the `volekerb/hivemill` and `volekerb` tokens in the tree with the real slug and applies the public-only overlay (`ci/public-overlay`); it refuses the private repository's own slug and a non-GitHub origin (T-0411).

   The tool extracts `git archive <ref>` (tracked files only, so `exports/`, `state/`, the database, `run/` and `worktrees/` are absent), runs `git init -b main`, makes one commit "Initial public release" with the given author and committer and a fixed date, and then checks the result:

   - structure (exit 2 and `--out` left empty if broken): one commit, only `refs/heads/main`, commit headers exactly tree, author and committer, and only file modes 100644, 100755 and 120000 (a gitlink or submodule is refused, in the source and in the export). A path with a control character or invalid UTF-8 is refused too;
   - the gate, and the only pass/fail: `python3 ci/scrub_scan.py --history <out> --history-allowlist ci/scrub_history_allowlist.txt`. It scans what is published: blobs as committed (symlink targets and binary files included, so `.gitattributes` encodings and `export-subst` are covered), every path name, author, committer, message, ref names and tags. See `docs/scrub-scan.md`. The allowlist holds only exact reviewed fixture lines (path, pattern and the hash of the line); owner terms, path names, author, committer and message are never allowlisted.

   git runs with your global and system config ignored, so `core.autocrlf`, `.mailmap`, hooks or signing settings in your own setup cannot change the export. The tool adds no remote and pushes nothing. It refuses an `--out` that is non-empty or inside the live factory home, and `--author` is required (no default). Exit 0 means 0 hits; exit 1 means hits (the output lists them masked, never the value); exit 2 means a refusal or an error, and `--out` is left empty. The summary also prints the number of binary blobs, for visibility.
2. **Scan report.** Keep the full tool output. It is the evidence for condition (i).
3. **Director sign-off.** The director checks the four conditions and receives the scan report, the planned repository name and URL, and the author identity.
4. **Owner heads-up.** The boss tells the owner.
5. **Create and push, on the director's explicit go only.** The new GitHub repository is created, and `<out dir>` is pushed to it as `main`, from the export directory only. Never push from the private repository's checkout. Publish only with `git push <new-remote> main` from the export directory; never tar or copy the export directory: `.git/config`, hooks, reflogs and unreachable objects are not scanned because they are not published by a push.

## Public repository checklist

For the owner, in order, once the director has given the go. Replace `volekerb/hivemill` if the name changes. Never run ci/publish_repo.py against the private repository; its main must stay unprotected.

1. **Export** with the public slug (procedure step 1): `python3 ci/fresh_export.py --out <new empty dir> --author "<Name <email>>" --ref origin/main --public-repo volekerb/hivemill`. Keep the output; it must report 0 hits.
2. **Create the empty repository and push** from the export directory only (procedure step 5): create `volekerb/hivemill` empty on github.com (no README, no license), then `git -C <export> remote add origin https://github.com/volekerb/hivemill.git && git -C <export> push origin main`.
   Disable Actions on the repository before the first push (docs/ci-cost.md).
3. **Flip to public**, on the director's explicit go: github.com/volekerb/hivemill > Settings > General > Danger Zone > Change visibility > Make public.
4. **Dry run** the metadata and read every line: `python3 ci/publish_repo.py --repo volekerb/hivemill`. It prints the six `gh api` calls and calls nothing.
5. **Apply**: `python3 ci/publish_repo.py --repo volekerb/hivemill --apply`. It refuses a private repository, a fork and any repo in the factory config `frozen_repos` (or `--private-repo`); the live public origin is allowed, and stops at the first failing call. It sets description, homepage, topics, Issues and Discussions on, wiki and Projects off, vulnerability alerts, private vulnerability reporting, and branch protection on `main` (required status checks from `ci/publish_metadata.json`, no required reviews).
6. **Create the issue labels** the issue templates use, by hand:
   ```
   gh label create setup --repo volekerb/hivemill
   gh label create question --repo volekerb/hivemill
   gh label create "good first issue" --repo volekerb/hivemill
   ```
   (`good first issue` may already exist as a GitHub default label; then skip it.)
7. **Social preview**, by hand (GitHub has no REST endpoint for it): Settings > General > Social preview > upload `docs/images/social-preview.png`.
8. **Tag and release v0.1.0** (pre-release; the release workflow, T-0419, creates the pre-release from the tag): `git -C <export> tag -a v0.1.0 -m v0.1.0 && git -C <export> push origin v0.1.0`.
   The release workflow (`.github/workflows/release.yml`) marks 0.x and `-`suffixed tags as pre-releases and takes the notes from `ci/release-notes/<version>.md`.
9. **Check the README on github.com** in light and dark mode: badges resolve, the screenshots and both mermaid diagrams render, and the herdr and phone-app links open.
10. **File the starter issues** from `docs/STARTER_ISSUES.md`, with the `good first issue` label where it says so.
11. **Confirm** Settings > General > Features: Discussions on, Wikis off, Projects off.

Not covered by the scanner (residual risk, accepted): leaks encoded as base64, `%40` or look-alike characters, and an owner term split over several lines.

## Still open (owner decisions)

- **Repository name** (and with it the URL).
- **Commit date and timezone (OPEN OWNER DECISION).** The author and committer date and timezone of the commit are published; a timezone reveals the owner's offset and the time reveals when they work. The tool therefore writes `--date "<YYYY-MM-DD>T00:00:00+0000"` (default: today, UTC, midnight). Unless the owner decides otherwise (a different fixed date or timezone), this default applies.
- **Where work happens after publishing: decided.** Public-first workflow, owner 2026-10-28 (G-021 intent, quoted: "I need to make sure we open source the factroy and then we continue all work for that in the public repo"). After the first publish, factory development happens in `volekerb/hivemill`; the private repository is frozen and then archived by the owner, and its open pull requests are ported or closed. The runbook is [CUTOVER.md](CUTOVER.md). Every commit, message and CI log is public the moment it is pushed, so the public text guard ([public-text-guard.md](public-text-guard.md)) gates every agent write.

The author identity is decided (owner decision Q1, M3357/M3360): the GitHub handle with its noreply email, never the real name or email, as in the command above.

## Not done by this goal

- No publish: no GitHub repository is created and nothing is pushed anywhere.
- No visibility change on any repository.
- No history rewrite of this repository (no filter-repo, BFG, force-push).
- No third-party scanner (gitleaks, trufflehog and the like); only `ci/scrub_scan.py`.

## Rerunning

- Tree: `python3 ci/scrub_scan.py --tree <dir>`; the export (with the reviewed lines): `python3 ci/scrub_scan.py --history <out> --history-allowlist ci/scrub_history_allowlist.txt`; history of the private repo: `python3 ci/scrub_scan.py --history <dir>`; single files: `python3 ci/scrub_scan.py --path <file>...`. Set `SCRUB_TERMS_FILE` for owner terms. See `docs/scrub-scan.md`.
- The history counts above: `python3 ci/scrub_scan.py --history .`, `git rev-list --all --count`, and `git log origin/main --format=%s | grep -c '^state:'`.
- The export: the command in the procedure, into a new `--out` each time. Tests: `python3 -m unittest tests.test_fresh_export`.
