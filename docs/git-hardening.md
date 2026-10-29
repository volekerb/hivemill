# Hardened git in bin/factory

Every worktree shares `~/factory/.git`, so a worker could plant a hook, `core.fsmonitor`, `core.gitProxy`, a merge driver, a filter or an include there and have it run as the tick or the judge. `bin/factory` therefore never calls `git` directly, and it allows only a small, known set of repo config. This is the cheap, repo-scope part of the fix; the rest belongs to G-010 (see "Known limit").

## (a) One helper, forced command-line settings
Every git call goes through `git_run` / `git_argv` (`_git`, `_git_rc` and `_git_out` are thin wrappers). It always adds, on the command line (which beats any config file):

`-c core.hooksPath=/dev/null -c core.fsmonitor=false -c protocol.allow=never -c protocol.https.allow=always -c protocol.file.allow=<never|always> -c commit.gpgSign=false -c tag.gpgSign=false`

and sets `GIT_TERMINAL_PROMPT=0` and `GIT_CONFIG_NOSYSTEM=1`. `gh pr merge`, which runs git itself, gets the same through `GIT_CONFIG_COUNT`. `protocol.allow=never` with only https allowed closes `git://`, `ssh`, `ext::` and remote helpers, so `core.gitProxy` (which only `git://` uses) can never run, including through `url.<x>.insteadOf` rewrites.

`protocol.file.allow` is `never` under the live `FACTORY_HOME` and `always` under any other (temp) home, so tests with local bare remotes keep working. It depends only on `HOME` vs the live path; there is no env var or option to flip it. `tests/test_verdict_carry.py` fails if a literal `"git"` argv appears anywhere else in `bin/factory`.

## (b) Repo-scope allow-list guard
Before every privileged git operation (tick sync and export, judge carry check and merge, `prepare_worktree`, `clean`'s fetch) `git_config_refusal(repo)` reads, **without includes**, `git config --file` on `<common-dir>/config`, every `<common-dir>/worktrees/*/config.worktree` and the target worktree's `config.worktree`. It refuses when:

- any `include.*` or `includeIf.*` key is present;
- any key is outside this list (names are case-insensitive): `core.repositoryformatversion`, `core.filemode`, `core.bare`, `core.logallrefupdates`, `remote.origin.url`, `remote.origin.fetch`, `branch.<name>.remote`, `branch.<name>.merge`, `extensions.worktreeConfig`, `user.name`, `user.email`;
- `user.email` does not end in `@users.noreply.github.com` (a real address in repo config would be committed under, so only the noreply form is allowed; `user.name` has no such condition);
- `remote.origin.url` is not the factory URL (config key `factory_remote_url`, see below) or, for a clone whose realpath main worktree is a registered project's `repo_path`, exactly that project's `https://github.com/<gh_repo>.git` (or without `.git`); under a temp home a plain absolute path, the tests' bare remote, is also accepted;
- `extensions.worktreeConfig` is anything but false;
- `<common-dir>/info/attributes` or `~/.config/git/attributes` is non-empty.

The list is exactly what the live `~/factory/.git/config` has (core.repositoryformatversion, filemode, bare, logallrefupdates; remote.origin.url and fetch; branch.<name>.remote and merge). If the config cannot be read the guard refuses too; a directory that is not a git repository is clean.

On refusal there is no git action: the tick skips sync, state commit and push; the judge exits 4 without merging; spawn exits 4 before the fetch; `clean` skips the repo. A `git-config-refused` event `{"keys": [...]}` is recorded and a director message goes out at most once an hour (the judge always records the event). To recover, remove the listed keys (`git config --unset <key>`) or empty `info/attributes` after finding who planted them.

`factory_remote_url` is the one origin URL the factory's own checkout may have, in the form `https://github.com/<owner>/<repo>.git`. It defaults to empty. While it is empty, the first time the guard needs it (trust on first use) it reads `remote.origin.url` from the live checkout's `.git/config`, keeps it only if it has exactly that form, stores it in the key and records a `config factory_remote_url tofu` event. If the live origin is missing or malformed (for example `ext::sh -c x`), nothing is stored and no URL matches, so the guard refuses (fail closed). Plain-path remotes stay allowed only under a temp `FACTORY_HOME`. Because linked worktrees share that config, set the key explicitly (`factory config factory_remote_url <url>`, director) on an existing install instead of relying on trust on first use. To change the key afterwards, the director runs `factory config factory_remote_url <url>`.

## (c) Worktree creation and the retry switch
`prepare_worktree` runs the guard on the project repo, then `git worktree add --no-checkout`, then the guard again on the **new worktree** (an `includeIf gitdir:.../worktrees/**` only applies once the worktree exists), and only then `git checkout`. If the second guard refuses, the half-made worktree is removed. The retry path (`git switch` in an existing worktree, which also checks out) is guarded first.

## Carry check
`verdict_carries` additionally runs with `core.commitGraph=false`, `core.attributesFile=/dev/null`, `GIT_ATTR_NOSYSTEM=1` and `GIT_ATTR_SOURCE` set to the empty tree (git >= 2.40), so not even the worktree's own `.gitattributes` can pick a driver. A `gh api` timeout (`GH_API_TIMEOUT`, 60 s) is a refusal ("cannot verify, gh api for main timed out"), not a traceback.

## Known limit (owned by G-010)
Workers run as the owner uid, unsandboxed, so they can already write `bin/factory`. Since T-0334 the worker-guard hook refuses worker writes (Bash and Edit/Write/MultiEdit/NotebookEdit) to `~/.gitconfig`, `~/.config/git/**`, `/etc/gitconfig` and the shell init files, and `git config --global|--system|-f <those>` writes; reads stay allowed. This guard does not examine global or XDG git config (`~/.gitconfig`, `~/.config/git/config`), `core.askPass`, or the global `credential.helper`, and checks `~/.config/git/attributes` only for being non-empty. Per-task clones with credential isolation (G-010) close that.

This guard rule is accident prevention, not a security boundary: a text check cannot see every way a worker can write a file (scripts run later, other writers such as curl or tar, other spellings). The real controls are the worker sandbox (`scout/workerbox.py`, `sandbox_roles`) and the owner's `chattr +i` on those files (pending director/owner decision).

Since T-0226 the hook also refuses a worker's repo-config writes (`git config <key> <value>`, `git remote add|set-url|...`, Edit/Write/redirects to `<gitdir>/config*` and `info/attributes`) except `--file` inside the scratchpad or a clone inside it; see "Repo git config writes" in `docs/worker-guard.md`. Known gaps (tracked in T-0337, not chased in T-0226): path globs and shell-built paths for redirects (`echo x > ~/factory/.git/conf*`), non-listed writers (`python -c`, `git init --template`, `git worktree add` with `-c` keys, `git update-index`, `curl -o`), an alias defined in config rather than `-c`, `GIT_CONFIG_COUNT` env injection beyond the `GIT_CONFIG*=` text check, and a `git clone` inside the scratchpad whose later `cd` is built at run time (refused, not allowed).

Tests: `tests/test_git_hardening.py` (tick, spawn, protocol/proxy, worktree add, retry switch) and `tests/test_verdict_carry.py` (judge).

## Public and frozen repos (G-021)

Three config keys, all empty by default (nothing changes while they are empty). Set them with `factory config <key> <value>` (director or owner).

| key | meaning |
|---|---|
| `public_text_repos` | comma list of `OWNER/NAME` whose text is public. `factory judge --merge` on a PR in such a repo (the PR URL's repo or the project's `gh_repo`) first runs the public text guard on the PR title, body, branch, comments, reviews and the commits since the verified `origin/main` (messages, identities, paths, added lines). Any hit, or no owner terms, refuses the merge with `judge: refused (public text: N hits: <pattern ids>)` and a `judge-blocked` event. The state branch is never pushed to such an `origin` (docs/state-branch.md). |
| `frozen_repos` | comma list of `OWNER/NAME` that bin/factory never pushes to or merges into: the state push, the judge's refresh push, `factory clean --remote-branch` and `gh pr merge` refuse with `repo <slug> is frozen (G-021)`. |
| `scrub_terms_file` | path of the owner terms file, local only. The environment variable `SCRUB_TERMS_FILE` wins. Without terms the judge check fails closed. |

The guard module is loaded from the main checkout (`$FACTORY_HOME/ci/public_text_guard.py`), never from the PR tree, so a PR cannot switch it off. See docs/public-text-guard.md.
