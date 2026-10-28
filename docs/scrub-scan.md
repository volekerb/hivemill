# Scrub scanner

`ci/scrub_scan.py` is the factory's own scanner for owner-specific and sensitive data. It is Python 3 standard library only, makes no network calls and writes nothing except stdout and stderr. Third-party scanners are not used and must not be installed.

## Usage

```
python3 ci/scrub_scan.py --tree [DIR]                       # tracked files (git ls-files in DIR, default .)
python3 ci/scrub_scan.py --history [DIR] [--exclude-ref REF ...] [--history-allowlist FILE] [--show-line-hash]
python3 ci/scrub_scan.py --path FILE...                     # arbitrary files, git not needed
```

Common options: `--terms-file FILE` (repeatable) and `--allowlist FILE` (default `ci/scrub_allowlist.txt`).

- `--tree` scans the files `git ls-files` lists in DIR. Untracked files, symlinks and binary files are not scanned (binary files are counted on stderr).
- `--history` scans the whole of `git log -p --all --root`: every added or removed line of every blob as committed (binary blobs and symlink targets included), every path name (directories are part of the path), every commit message, each commit's author and committer name and email, every ref name, and the tagger and message of annotated tags. `--exclude-ref` leaves a ref out (for example `refs/heads/state`); repeat it or give several refs. git runs with `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`, `GIT_NO_REPLACE_OBJECTS=1`, no other `GIT_*` variable, and the flags `--text --no-textconv --no-ext-diff --no-renames --no-use-mailmap --root --src-prefix=a/ --dst-prefix=b/ -c core.quotepath=off`, so user config, `.mailmap`, `.gitattributes` and replace refs cannot change what is scanned. Lines are split on LF only, as git does (`--tree` and `--path` use Python's `splitlines`, which also splits on `\r`, `\x0b`, `\x0c` and a few other characters).
- `--path` scans the given files, for example an HTML page or a SQL dump.

## What it looks for

Built-in generic shapes (committed):

| id | matches |
|---|---|
| `home-path` | a home directory path for any name except `user`, `runner`, `example`, `placeholder` or a `<...>` placeholder |
| `rfc1918` | a private IPv4 address: the 192.168/16, 10/8 or 172.16/12 blocks. Four octets of 0-255 are required, so a version string such as `10.0.0` is not a hit |
| `cgnat` | an IPv4 address in the 100.64/10 block (carrier-grade NAT, Tailscale) |
| `email` | any email address except `@example.com`, `@example.org`, `@example.invalid`, `noreply@anthropic.com`, `noreply@github.com` and GitHub noreply addresses (`<digits>+<handle>@users.noreply.github.com` or `<handle>@users.noreply.github.com`) |
| `github-token` | `ghp_`, `gho_`, `ghs_`, `ghu_` or `ghr_` followed by 36 or more letters and digits |
| `github-pat` | `github_pat_` followed by 22 or more letters, digits and underscores |
| `anthropic-key` | `sk-ant-<segment>-` followed by 20 or more characters |
| `aws-access-key-id` | `AKIA` followed by 16 capital letters or digits |
| `private-key` | a PEM private key header line |
| `railway-token` | a `RAILWAY_TOKEN` or `RAILWAY_API_TOKEN` assignment with a value of 16 or more characters, or a UUID on a line that also contains `railway` (any case) |
| `oauth-secret` | a `client_secret`, `refresh_token` or `access_token` assignment (quoted or bare) with a value of 20 or more characters from `[A-Za-z0-9._~+/=-]` |
| `generic-secret` | an assignment whose key contains `secret`, `token`, `api_key`/`api-key`/`apikey`, `password`/`passwd` or `private_key`, with a value of 16 or more non-space characters that is not a placeholder (`<...>`, `${...}`, `$VAR`, or containing `xxx`, `changeme`, `example`, `dummy`, `test`) or a dotted code reference |
| `env-file` | a tracked path whose basename is `.env`, `*.env` or `.env.*`, except `.env.example` and `.env.sample`. Path only: the content is never printed (`--tree` and `--history`) |
| `owner-term` | a literal term from the terms file (see below) |

## Owner terms

The owner's name, username, email, LAN address, hostname and GitHub handle are never committed. Put them in a file kept outside the repository, one term per line, matched case-insensitively:

```
first-term
second-term
```

Pass it with `--terms-file FILE` and/or the environment variable `SCRUB_TERMS_FILE`. When both are set, the terms are combined. With no terms the scanner prints `no owner terms loaded` to stderr and the generic patterns still run. The scanner prints only how many terms it loaded, never the terms. An owner-term hit prints `owner-term#<n>` instead of any text, where `<n>` is the 1-based position of the term in the loaded list (terms are sorted case-insensitively, duplicates removed). An owner term inside a printed path is replaced by the same `owner-term#<n>`, and any other hit that overlaps a term never shows a prefix of it.

### Required terms-file content

The scrub is a mandatory gate over the working tree and the full history, so the owner's terms file must list at least the following, one per line. Never write the values into the repository, a PR or a message:

- the owner's unix username (it also appears in home paths)
- the owner's first name and last name
- every owner email address (and the local part of each)
- every private hostname, including the development VM
- every private project name (for example the private sibling project, and any other)
- every private domain

A gate run without this file only proves the generic patterns; its output says `no owner terms loaded`.

## Output

One line per hit: `<pattern-id> <path>:<line> <masked>` (`<masked>` is `owner-term#<n>` for an owner-term hit) for `--tree` and `--path`, and `<pattern-id> <commit>:<path> <masked>` for `--history` (the path part is `author`, `committer` or `message` for those fields; `<commit>:<path> (path name)` for a hit in a path name; `ref:<name>` or `tag:<name>` for refs and tags). A printed path has every hit in it masked and control characters escaped as `\xNN`, so a hit never prints the leaked value and a file name cannot forge report lines. The matched text is masked: at most its first 4 characters are shown, fewer for a short match. A per-pattern count and a total (`N hits`) follow.

## Allowlist

`ci/scrub_allowlist.txt` lists reviewed, intentional hits for `--tree` and `--path`. `--history` ignores it and has its own exact-line allowlist (below). Each line has the form:

```
path:pattern-id:regex-of-line  # reason
```

`path` is exact and relative to the scanned directory. `pattern-id` is one of the ids in the table above. `regex` is a Python regex searched in the full source line, so keep it as narrow as the hit. The `# reason` comment is required; a line without one is a usage error. Adding or widening a line changes what the public repository may contain, so it needs a reviewer's explicit approval in the PR.

An entry suppresses only hits of its own pattern id on the lines its regex matches. A line holding an allowlisted email and a token is still reported for the token.

- `owner-term` hits are never suppressed. An entry naming `owner-term` is a usage error.
- Token-shape hits (`github-token`, `github-pat`, `anthropic-key`, `aws-access-key-id`, `private-key`, `railway-token`, `oauth-secret`, `generic-secret`, `cgnat`, `env-file`; for `env-file` the regex is searched in the path) are suppressed only by an entry naming that exact pattern id.
- The old form `path:regex` is still read for one release. It prints a deprecation warning on stderr and suppresses only `home-path`, `rfc1918` and `email` hits. Migrate such lines to the new form.

## History allowlist

`--history-allowlist FILE` (committed one: `ci/scrub_history_allowlist.txt`, used by `ci/fresh_export.py`) lists reviewed blob lines. Without the option `--history` behaves as before. Each line has the form:

```
path:pattern-id:sha256hex  # reason
```

- `path` is the repo-relative path from the diff header after C-unquoting, matched exactly (no glob, directory or regex). `pattern-id` is one of the ids above except `owner-term`.
- `sha256hex` is 64 lowercase hex characters: the SHA-256 of the raw bytes of one diff line, without its single `+` or `-` marker and without the trailing LF (a CR stays in). It is over bytes, never decoded text, and the line is split on LF only. It matches both `+` and `-` lines. It does not name a commit.
- An entry suppresses only a blob-content hit of that pattern id at that path. It never suppresses author, committer, message, path-name, ref or tag hits, and `owner-term` hits are never suppressed.
- Usage errors (exit 2): a missing `# reason`, a hash that is not 64 lowercase hex, an unknown pattern id, an `owner-term` entry, a duplicate entry. An entry that matched nothing prints a warning on stderr and does not change the exit code.
- `--show-line-hash` prints ` sha256=<hex>` after each blob-content hit, to write entries. It is off by default so CI logs do not carry hashes of short lines. A reviewer approves every entry line by line.

## Performance and history

The email pattern is bounded (local part up to 64 characters, labels up to 63), so a very long line with no `@` scans in linear time. `--history` sends git's stderr to a temporary file, so a git that writes a lot to stderr cannot deadlock the scan.

## Exit codes

| code | meaning |
|---|---|
| 0 | no hits |
| 1 | at least one hit |
| 2 | usage or runtime error (no mode, bad argument, unreadable file or terms file, not a git repository) |
