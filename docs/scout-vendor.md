# Skill scout: vendoring and the exfil pre-screen

`factory skilleval vendor C-xxxx` copies a prompt-only candidate into the skilleval root so an evaluation can use it. Code: `scout/vendor.py`. Roles: director, lead, owner; every worker role (coder, qa, reviewer, pm, scout, evaluator, …) is refused with exit 3.

## Config

`skilleval_root` (default `~/.cache/factory-skilleval`, `~` expanded). The resolved path must lie outside `~/factory`, `~/.claude` and the factory home, and must not contain them either; otherwise `factory config skilleval_root …` and `vendor` refuse with exit 2.

## What vendor does

1. Refuses (exit 2) unless the stored `install_risk` is exactly `prompt-only`, then lists the tree at the candidate's `head_sha` (`gh api repos/<r>/git/trees/<sha>?recursive=1`) and runs `scout/risk.py` on it again. A changed result is stored and the candidate refused: the DB value may be stale.
2. Copies only allow-listed blobs under the candidate path (`.md`, `.txt`, `.csv`, `plugin.json`, `marketplace.json`, `.json` data under `skills/*/`, extensionless `LICENSE*`/`NOTICE*`/`README*`). Every other file, every mode 100755 blob, symlink (120000) and submodule (160000) is skipped and listed with its reason. Each blob is fetched by its blob sha (`git/blobs/<sha>`, base64-decoded, checked against its git hash). No clone, no download tool, nothing is executed. Files are written mode 0644.
3. Caps: at most 200 files and 2 MB; over either, the partial copy is deleted and vendor exits 2.
4. Layout: `<root>/vendor/<C-id>@<sha>/<plugin>/` plus `VENDOR.json` (repo, path, sha, licence, `copied`, `skipped`, `prescreen_hits`) next to the plugin dir. A loose skill (no `.claude-plugin/plugin.json`) is wrapped: generated `plugin.json` named `cand-<c-id>` with the description `G-009 eval copy of <repo>@<sha>`, the skill under `skills/<name>/`.
5. Re-checks the written tree with `assert_prompt_only_tree(path)` (any symlink, exec bit, non-allow-listed extension, `hooks/`, `hooks.json`, `.mcp.json`, shell line in markdown, `plugin.json` with `hooks`, `mcpServers` or `commands` pointing at a script → delete the copy, exit 2). Spawn code calls the same function before passing `--plugin-dir`.
6. Idempotent: a valid copy of the same C@sha is reused without any gh call.

## Exfil pre-screen

`prescreen(path, own_repo)` scans every vendored text file and returns hits `{file, line, pattern}`. It is a cheap filter on top of the sandbox, not a replacement; false positives go to the owner. The patterns live in one table (`PATTERNS`, plus `base64-run` and `url`):

| id | matches |
|---|---|
| `sk-ant` | `sk-ant-` anywhere |
| `base64-run` | 200 or more chars of `[A-Za-z0-9+/=_-]` (a run of one repeated char, such as a rule of dashes, is ignored) |
| `url` | a URL whose host is not `docs.anthropic.com`, `docs.claude.com`, `code.claude.com`, `anthropic.com`, `github.com/anthropics/…` or the candidate's own repo on github.com |
| `curl`, `wget`, `urllib`, `requests`, `nc`, `netcat`, `printenv`, `environ` | the word (`requests.` with the dot) |
| `env-cmd` | a line starting with `env`, or `env` followed by `|` or `>` |
| `credentials-file`, `credentials` | `.credentials.json`; the word `credentials` |
| `api-key` | `api key`, `api_key`, `ANTHROPIC_API_KEY` |

Evasion handling (fail closed): every line is NFKC-normalised and stripped of zero-width characters and every other Unicode format character (category Cf: zero-width, soft hyphen, bidi controls, BOM) before any pattern runs, so `ｃｕｒｌ` and `c<ZWSP>url` hit. `base64-run` also fires on a blob wrapped over lines or split by whitespace (8+ char chunks that join to 200+ chars with a digit, `+`, `/` or `=`). `url` normalises the path (percent-decoding, `posixpath.normpath`) before the github.com allow-list, so `github.com/anthropics/../evil` hits; userinfo (`user@host`) and IP-literal hosts always hit; hosts after any other scheme (`ftp://`, `wss://`, `file://`), after `@` and bare IPv4/IPv6 literals (a dotted IPv4 with a port, `[::1]`) hit; schemeless hosts (`evil.example/x`, `//evil.example`, `host:8080`) hit unless allowed. Trade-off: a bare token ending in a common file extension and with no path or port (`SKILL.md`, `run.py`, `run.sh`, `main.rs`) is read as a file name, so a real domain such as `attacker.sh` with no path is missed (with a path or port it hits); a bare dotted IPv4 (four numeric parts) hits even when it is a version string. Residual evasions also hit: U+3002, U+FF0E and U+FF61 are folded to `.` before host matching; a host-like segment anywhere in a relative path (`a/b/evil.example`) hits unless it is a file name (`FILE_EXTS`: common source, doc, image and archive extensions; any other alphabetic label counts as a TLD) in the last segment; an integer IP right after a scheme or `@` (`ftp://0x7f000001`, `//2130706433`, up to 0xFFFFFFFF) hits; and base64 split into chunks under 8 chars or wrapped with per-line prefixes (`> `, `# `, quoted string concatenation) hits when 200+ chars join across the lines that look like a blob: each line is scored on its own (at least 30% of its chunks have mixed case plus a digit, `+` or `=`; for chunks under 8 chars, 25% upper case, or 14% with that mix), a plain-prose line is skipped rather than ending the run (up to 3 in a row inside one blob), so adjacent prose does not hide a blob, and a token outside the base64 alphabet ends it. A finished run only counts when it looks random and wrapped: at least 25% upper case, 25% lower case and 5% digits, and no more than a third of its chunks plain words or numbers (`word`, `Word`, `WORD`, `123`); the chunk width is not checked, because the attacker picks it. Version lists, timestamp lists, CSS hex colours (`#2563EB`) and code do not meet that, so they stay clean (a test scans every tracked repo file). Residual: octal or short dotted IP forms (`ftp://0300.0.2.1`, `ftp://127.1`) are not caught, a one-word `label:` at line start is ignored, but a longer prose prefix, or a plain word between chunks (`and <chunk>`), can hide a wrapped blob (accepted residual), and a bare file-like token in a relative path (`factory.db`) can hit as a host; these go to the owner. `zip` is not a file extension here: a bare `attacker.zip` hits. The wrapped-base64 match is anchored to the start of a run, so a scan is linear in file size. The tree check counts a `.md` file over `risk.READ_CAP` (64 KB) as a problem, because the shell-in-markdown scan cannot read all of it.

Any hit: the copy stays on disk, `VENDOR.json` lists the hits, the candidate status becomes `needs-owner`, `risk_detail` holds the hits (`prescreen <id>: <file>:<line>`), and vendor exits 4 naming the hit count. There is no override switch. Note that `scout/risk.py` already flags `curl`/`wget` in SKILL.md, commands and agents as `network` (exit 2 earlier); the pre-screen covers every other file.

`is_eval_allowed(candidate, root=None)` is the one gate for queueing or trying an evaluation: false for `needs-owner` and `dismissed` candidates and for any `install_risk` other than exactly `prompt-only`; with `root`, the vendored copy must also exist, pass the tree check and carry no hits.
