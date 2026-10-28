# Public text guard

`ci/public_text_guard.py` refuses private data in text that is about to become public: PR titles and bodies, comments,
commit messages, branch names and pushed diffs. It reuses `ci/scrub_scan.py` (imported by path, never copied) and adds
only what text in public needs. Python 3 stdlib, no network. Exit codes: 0 clean, 1 hits, 2 usage or runtime error.

## Usage

```
public_text_guard.py text [FILE|-]                 # scan a file or stdin
public_text_guard.py ref NAME...                   # scan branch/tag names
public_text_guard.py push [--repo DIR] BASE HEAD   # scan what pushing HEAD publishes beyond BASE
```

Options (before or after the mode): `--terms-file FILE` (repeatable), `--no-terms` (tests/CI only), and for `push`
`--allowlist FILE` (default `ci/scrub_allowlist.txt`).

Output is one line per hit, `<pattern-id> <location> <masked>`, then `N hits`. Locations: `line N` (text),
`ref:<name>`, `commit:<sha8>:message|author|committer|path`, `<path>:<line>` (added diff line). Hits are masked with
`scrub_scan.mask` (at most the first 4 characters). Owner terms are never printed; only their count goes to stderr.

`push` scans every commit in `BASE..HEAD`: message, author and committer name and email, every changed path name and
every added diff line of each commit (a line added in one commit and removed in the next is still published, so it
is a hit). A merge commit is diffed with `--cc`: only lines new against every parent count, so a merge of
`main` does not report content `BASE` already published, while a line added in the merge itself does. Added lines honour `ci/scrub_allowlist.txt` by path and pattern id exactly as `scrub_scan --tree` does;
owner-term, `ipv4` and `private-host` hits are never allowlisted. Messages, identities, paths and ref names have no allowlist.

Importable API (for the worker-guard hook and `bin/factory`): `check_text(text, terms)` and `check_ref(name, terms)`
return `[(pattern_id, masked)]`; `check_push(repo, base, head, terms, allowlist=None)` returns
`[(pattern_id, location, masked)]`; `load_terms_or_fail(env, config_path)` returns the terms or raises `NoTermsError`.

## Fail-closed rule

Owner terms (owner name, handle, host names, private project names, real email) come only from the terms file
(`--terms-file`, `$SCRUB_TERMS_FILE`). With none loaded the tool exits 2 with
`public-text-guard: no owner terms loaded`, because a guard without terms would pass the owner's own name.
`--no-terms` overrides this for tests and CI only.

## Patterns

| id | matches |
|---|---|
| home-path, rfc1918, email, github-token, github-pat, anthropic-key, aws-access-key-id, private-key | `scrub_scan.GENERIC` (see [scrub-scan.md](scrub-scan.md)) |
| owner-term | a line from the terms file, case-insensitive |
| ipv4 | any dotted IPv4 except 127/8, 0.0.0.0, 255.255.255.255 and the documentation ranges 192.0.2/24, 198.51.100/24, 203.0.113/24; covers 10/8, 172.16/12, 100.64/10, 169.254/16 and public addresses; not part of a longer dotted number |
| private-host | a host name ending in `.local`, `.lan`, `.internal`, `.home.arpa` or `.ts.net` |

## Allowed by design

Factory ids (`M8698`, `T-0515`, `G-021`, `L-048`), `PR #309`, three-part versions (`1.2.3`), `example.com` addresses and
GitHub noreply addresses, and the placeholder homes of scrub_scan (`/home/user/`, `/home/runner/`). Files such as
`settings.local.json` do not match `private-host`.

## Residual risk

The guard matches text shapes. It does not see base64 or %-encoded values, a term split across lines or concatenated at
run time, or private data that has no fixed shape. Review is still the last line of defence.

## Tests

`tests/test_public_text_guard.py` runs `tests/fixtures/public_text_corpus.json` through the CLI and the API. The corpus
holds templates (for example `{HOME}`, `{GHP}`) that the test expands, so the repo itself adds no scrub hits.
