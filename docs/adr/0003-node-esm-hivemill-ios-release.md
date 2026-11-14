# ADR 0003: Node ESM and bash for the hivemill iOS release script

## Status

Accepted, 2026-11-14. Approved by claude-director (note on T-1424, 2026-11-14T11:35; boss M34657). Basis: the owner's G-044 brief, which names bash plus Node ESM for this shareable plugin script. This ADR does not change ADR 0001.

## Context

G-044 adds a shareable iOS TestFlight release to the public hivemill plugin: `plugins/hivemill/scripts/ios-release/`. Users of the plugin run it on their own Macs, next to Xcode. It needs an ES256 JWT, HTTPS calls to the App Store Connect API, JSON and file-mode checks, and the macOS Keychain. ADR 0001 sets Go as the primary language and Python for glue, but a Mac user of the plugin has no Go toolchain for this and no wish to install one, and Python is not guaranteed to be a recent one on macOS.

## Decision

- The script is bash (`release.sh`, written for the stock macOS bash 3.2) plus Node ESM helpers (`.mjs`), standard library only: `node:crypto`, `node:https`/`fetch`, `node:fs`. No npm, no `package.json` dependencies. Node 20 or later.
- This applies to `plugins/hivemill/scripts/ios-release/` only. Other code keeps ADR 0001.
- JSON, regexes, file modes, `.env` parsing, the Keychain and the JWT live in Node; bash does the flow and runs `xcodebuild`.
- The tests run from the existing Python test module `tests/test_hivemill_plugin.py`, which starts `node --test` over the `.mjs` tests. No workflow change and no new Python file.

## Consequences

- Anyone with Xcode and Node 20 can run it; nothing is installed.
- The Node tests run in the existing CI job and on Linux with fakes (fake key, fake clock, a mock API, stubs on PATH). The real Xcode steps are proven on a Mac before merge.
- A third language appears in the repo, but confined to one directory and one test hookup.
- CI must have Node 20 or later; it fails there if Node is missing, and skips locally.

## Alternatives

- Go: matches ADR 0001, but it would need a build and release of binaries for macOS for a shareable plugin script, and the owner brief names Node.
- Python: the glue language of the repo, but macOS ships no dependable modern Python, and the stdlib has no ES256 signing without a third-party package.
- Pure bash with `openssl` and `curl`: ES256 signatures from `openssl` need DER to raw conversion, and JSON handling is fragile and hard to test; rejected.
