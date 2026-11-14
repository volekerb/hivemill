# iOS TestFlight release

`release.sh` builds an iOS app, uploads it to TestFlight, sets the What to Test text and adds the build to your tester groups. It is driven by one small file in your project repo and by credentials that live only on your machine. The Claude skill `ios-testflight-release` is a thin guide over this script.

Everything on this page uses placeholders. No real id, key or name belongs in a repo.

## Prerequisites

- macOS with Xcode (`xcodebuild` on the PATH).
- Node 20 or later. The helpers are Node ESM, standard library only, no npm install.
- A git checkout of your project.

## Two credentials, two jobs

**(a) A Team API key with the App Manager role.** It makes the App Store Connect REST calls (find the app, read builds, set What to Test, resolve and fill tester groups). It is needed in every signing mode. Create it in App Store Connect > Users and Access > Integrations, and download the `.p8` file once: Apple does not let you download it again.

**(b) Signing.** Select it with `signing` in the config, or `--signing` on the command line.

- `xcodeAccount` (the default) uses the Apple ID signed into Xcode (Xcode > Settings > Accounts, an account that is in your team) with `-allowProvisioningUpdates`. No key is passed to xcodebuild. An App Manager key cannot cloud-sign, which is why this is the default.
- `apiKey` (optional) passes the key to xcodebuild as `-authenticationKeyPath`, `-authenticationKeyID` and `-authenticationKeyIssuerID`. It needs a key with the Admin role.

The App Manager key from (a) is still required for the REST calls in both modes.

## Where the credentials go

The project config names only a profile (`ascProfile`). The values live in your own setup. For each of the three values (`ASC_KEY_ID`, `ASC_ISSUER_ID`, `ASC_KEY_PATH`) the first source that has credentials wins:

1. Environment variables `ASC_KEY_ID`, `ASC_ISSUER_ID`, `ASC_KEY_PATH`, all three. Setting only one or two is an error (exit 4).
2. The profile file `~/.config/hivemill/asc/<profile>.env`. Run `chmod 600` on it; a file readable by group or others is refused. It holds three lines:

   ```
   ASC_KEY_ID=<your key id>
   ASC_ISSUER_ID=<your issuer id>
   ASC_KEY_PATH=~/.appstoreconnect/private_keys/AuthKey_<your key id>.p8
   ```

   Keep the `.p8` in `~/.appstoreconnect/private_keys/` (mode 600). The file is parsed, never sourced as a shell script.
3. The macOS Keychain (on by default on macOS; `HIVEMILL_ASC_KEYCHAIN=off` disables it, `on` forces it). Store each value once:

   ```
   security add-generic-password -s hivemill-asc-<profile> -a ASC_KEY_ID -w '<your key id>'
   security add-generic-password -s hivemill-asc-<profile> -a ASC_ISSUER_ID -w '<your issuer id>'
   security add-generic-password -s hivemill-asc-<profile> -a ASC_KEY_PATH -w '<path to your .p8>'
   ```

No fall-through: a source that is present but broken (one env var missing, a profile file with loose permissions, a missing Keychain item, an unreadable key) stops the run with exit 4. It never silently tries the next source.

## Per-project config: `.hivemill/ios-release.json`

The file lives at `<git top>/.hivemill/ios-release.json`; `--config PATH` overrides it. It is a strict JSON object: an unknown key, or a wrong type, is exit 3 and names the field. A value equal to `~` or starting with `~/` has the `~` replaced by `$HOME`, but only in `notes.path`, `worktreeBase` and `forbiddenWorktrees`. Other relative paths resolve against the build directory.

| field | type | required | meaning |
|---|---|---|---|
| `schemaVersion` | integer, must be 1 | no, default 1 | any other value is exit 3 |
| `ascProfile` | string, 1-64 of `A-Z a-z 0-9 . _ -`, not starting with `.` | no, default `default` | the NAME of the credential profile, never a value |
| `workspace` | path ending `.xcworkspace` | exactly one of `workspace` / `project` | |
| `project` | path ending `.xcodeproj` | exactly one of `workspace` / `project` | both or neither is exit 3 |
| `scheme` | string | yes | |
| `configuration` | string | no, default `Release` | |
| `bundleId` | reverse-DNS string | yes | used to find the app in App Store Connect |
| `teamId` | 10 characters, `A-Z 0-9` | yes | `teamID` in ExportOptions |
| `signing` | `xcodeAccount` or `apiKey` | no, default `xcodeAccount` | see "Two credentials" |
| `marketingVersion` | object with exactly ONE form: `file` + `regex` together; or `buildSetting` (read with `xcodebuild -showBuildSettings`); or `command` (argv); or `value` (string) | yes | `file` is relative to the build dir; `regex` is a JavaScript RegExp source compiled with the `m` flag, must compile and have at least one capture group; the version is capture group 1 of the first match, trimmed. No match, or a result that is not 1-3 dot-separated integers, is exit 5 |
| `testerGroup` | string, or non-empty array of distinct non-empty strings | yes | beta group NAME(s), exact case-sensitive match |
| `submitExternal` | boolean | no, default `false` | `true`: external groups get the build, which submits it to beta app review. `false`: external groups are skipped with a notice |
| `notes` | object with exactly ONE key: `path` (`~` allowed; every `{build}` is replaced by the build number) or `command` (argv) | yes | the What to Test text; 4000 characters at most; empty or missing is exit 5 |
| `notesLocale` | string | no, default `en-US` | |
| `buildReadyCheck` | argv array | no | runs in the build dir; it gets the 40-hex commit sha as its last argument; non-zero is exit 5 |
| `gates` | array; each item an argv array, or an object with `cwd` (a directory inside the build dir) and `argv` | no, default empty | real run only, after `buildReadyCheck` and before the archive; the first non-zero gate is exit 5 and names its index |
| `worktreeBase` | directory (`~` allowed) | no | build in a fresh detached `git worktree` under it, removed on exit (also on failure). Unset: build in the checkout, which must be clean unless `--allow-dirty` |
| `forbiddenWorktrees` | array of directories (`~` allowed) | no | a build dir equal to or inside any entry is exit 5 before anything heavy runs |
| `defaultBranch` | git ref, e.g. `origin/main` | no | the commit to build. If the part before the first `/` is a configured remote, `git fetch --quiet <remote>` runs first. Unresolvable is exit 5. Without `worktreeBase`, HEAD must equal that commit, else exit 5 |
| `heavySlotCommand` | argv array (prefix) | no | prefixed to every gate and to the archive and export xcodebuild calls (for example a lock or a scheduler) |
| `pollTimeoutMinutes` | integer 1-180 | no, default 60 | how long to wait for processing to reach VALID |
| `pollIntervalSeconds` | integer 0-600 | no, default 30 | |

Every command (`argv`) is a JSON array run without a shell. A project that wants a shell names `sh` itself as `argv[0]`.

Example, placeholders only (the same content as `ios-release.example.json`):

```json
{
  "ascProfile": "default",
  "project": "ios/MyApp.xcodeproj",
  "scheme": "MyApp",
  "bundleId": "com.example.myapp",
  "teamId": "ABCDE12345",
  "signing": "xcodeAccount",
  "marketingVersion": { "file": "ios/Config/Version.xcconfig", "regex": "^MARKETING_VERSION = (.+)$" },
  "testerGroup": ["Internal Testers"],
  "submitExternal": false,
  "notes": { "path": "docs/testflight/build{build}.md" },
  "buildReadyCheck": ["./scripts/check-build-ready.sh"],
  "gates": [["make", "test"], { "cwd": "ios/MyKit", "argv": ["swift", "test"] }],
  "worktreeBase": "~/worktrees/myapp",
  "forbiddenWorktrees": ["~/src/myapp-release"],
  "defaultBranch": "origin/main"
}
```

## Tester groups

Group names are matched exactly (case-sensitive). All groups are resolved before anything is built: an unknown or ambiguous name is exit 6 up front. After the build is VALID and What to Test is set, each group is handled in config order, with one of three outcomes:

1. An internal group with access to all builds (`hasAccessToAllBuilds`) is skipped. Its testers already get every build, and the API would answer 422 if you added the build.
2. Any other internal group gets the build added.
3. An external group gets the build only with `"submitExternal": true`, because that submits the build to beta app review. Without it the group is skipped with a notice.

`--dry-run` prints the decision for every group (`would add`, `would skip (gets all builds)`, `would skip (external; submitExternal false)`), so you see it before a real run. A skipped group is not a failure.

## Usage

```
release.sh [--config PATH] [--profile NAME] [--signing xcodeAccount|apiKey] [--dry-run] [--allow-dirty] [--resume-build N] [--help]
```

| flag | meaning |
|---|---|
| `--config PATH` | project config (default `<git top>/.hivemill/ios-release.json`) |
| `--profile NAME` | credential profile (default: `ascProfile` from the config, else `default`) |
| `--signing MODE` | `xcodeAccount` or `apiKey`; overrides `signing` in the config |
| `--dry-run` | run the checks and print what would happen; builds, uploads and writes nothing |
| `--allow-dirty` | build a dirty checkout (only when `worktreeBase` is not set) |
| `--resume-build N` | skip the build: wait for build N, then set What to Test and add the tester groups |
| `--help` | print the usage text and exit 0 |

`--dry-run` and `--resume-build` cannot be combined.

## Order of a real run

1. Parse the flags; validate the config (3); resolve the credentials and check the key (4).
2. Resolve the commit (`defaultBranch`, else HEAD; fetch when it names a remote); set up the build dir (worktree or checkout); check `forbiddenWorktrees` and a clean tree (5).
3. Read the marketing version (5); find the app, compute the next build number N = highest App Store Connect build for that version + 1 (1 if none), resolve every tester group (6); read the notes with `{build}` = N (5); run `buildReadyCheck` with the commit sha appended (5).
4. Run the gates in order (5).
5. Archive: `xcodebuild archive` with `-allowProvisioningUpdates` and `CURRENT_PROJECT_VERSION=N`; the three `-authenticationKey*` flags only in `apiKey` mode.
6. Export and upload: `xcodebuild -exportArchive` with an ExportOptions plist (method `app-store-connect`, destination `upload`, signingStyle `automatic`, `manageAppVersionAndBuildNumber` false, `teamID`); the three flags only in `apiKey` mode. Failure is exit 7.
7. Poll until the build is VALID (8).
8. Set What to Test (9).
9. Handle each tester group in config order (9 on a real failure).
10. Print the summary.

## Exit codes

| code | meaning |
|---|---|
| 0 | success |
| 1 | internal error |
| 2 | usage: unknown flag, bad `--profile`, bad `--signing`, disallowed `HIVEMILL_ASC_API_BASE` |
| 3 | config: invalid or unknown field |
| 4 | credentials: missing, incomplete, unsafe permissions or an unreadable key |
| 5 | preflight: node < 20 or missing, xcodebuild missing, dirty tree, forbidden build dir, `defaultBranch` unresolvable or checkout not at it, `buildReadyCheck` or a gate non-zero, notes missing, empty or over 4000 characters, marketing version missing or malformed |
| 6 | App Store Connect API: 401/403, app or any named group not found or ambiguous, non-integer builds, retries exhausted including timeouts |
| 7 | xcodebuild archive or export/upload failed |
| 8 | processing INVALID or FAILED, or not VALID in time |
| 9 | post-processing failed (What to Test or an add-to-group that was not skipped); the build IS uploaded: rerun with `--resume-build N` |

## What `--dry-run` does

It runs steps 1-3: it validates the config and credentials, creates and removes the worktree if there is one, runs `xcodebuild -version` (and `-showBuildSettings` for the `buildSetting` form), and prints the next build number, the signing mode, the notes length, the decision for each tester group, each gate, the archive and export command lines it would run (redacted) and the ExportOptions plist. It never runs gates, the archive or the export, and it never sends a POST, PATCH or DELETE to App Store Connect. A real run is the only thing that uploads.

## `--resume-build`

When exit 9 says the build is uploaded but What to Test or a group failed, run again with `--resume-build N` (the build number from the summary). It repeats steps 1-3 without the next-build computation and without `buildReadyCheck`, with build = N, then steps 7-10. It runs no gates and no xcodebuild.

## Summary line and logs

The last stdout line of every run is one JSON object. Everything else goes to stderr. Keys:

- `ok` (bool), `exitCode` (int), `mode` (`release`, `dry-run` or `resume`), `signing`, `marketingVersion`, `build`, `buildId` (null when unknown);
- `groups`: a list of objects with `name` and `status` (`added`, `skipped-all-builds`, `skipped-external`, `would-add`, `would-skip`, `failed`, `pending`);
- `commit` (sha or null), `error` (a redacted one-line string or null).

It never contains a credential value.

Redaction: the key id, the issuer id, the key path and its file name, any JWT and any PEM private-key block are replaced by `[REDACTED]` in all xcodebuild output, helper output, gate and hook output and every logged command line. There is no switch to turn it off. Credentials never enter xcodebuild's environment; they reach it only as the `-authenticationKey*` arguments in `apiKey` mode.

## xcodebuild and PATH

Every xcodebuild call runs with `PATH=/usr/bin:/bin:/usr/sbin:/sbin`. A Homebrew `rsync` earlier on the PATH breaks IPA packaging ("Copy failed", rsync "syntax or usage error"). The path of `xcodebuild` is resolved once, then each call runs as `[heavySlotCommand...] env PATH=/usr/bin:/bin:/usr/sbin:/sbin <xcodebuild> ...`. Gates and hooks keep your normal PATH.

## Timing

After the upload, processing to VALID took about 3 minutes in a real run; 5-15 minutes is typical. `pollTimeoutMinutes` defaults to 60.

## Troubleshooting

- **401 or 403 (exit 6):** the key's role is too low, or the key was revoked. Use a Team key with the App Manager role (or Admin).
- **App or group not found or ambiguous (exit 6):** check `bundleId`, and that each `testerGroup` name matches exactly one group in the app's TestFlight tab, including case.
- **Build number already used:** another upload took the number. Run again; the next build number is computed from App Store Connect each time.
- **Processing stuck, or MISSING_EXPORT_COMPLIANCE:** the build stays in processing or waits for export compliance. Set `ITSAppUsesNonExemptEncryption` in the app's Info.plist (or answer the question once in App Store Connect), then `--resume-build N`.
- **"Cloud signing permission error" or "No signing certificate iOS Distribution found":** an App Manager key was used in `apiKey` mode. Use `xcodeAccount` (the default), or an Admin key with `--signing apiKey`.
- **Signing or provisioning errors with `-allowProvisioningUpdates`:** the Apple ID is not signed into Xcode (Xcode > Settings > Accounts), or it is not a member of the team in `teamId`.
- **"Copy failed" or rsync "syntax or usage error" while packaging the IPA:** a Homebrew `rsync` is on the PATH. `release.sh` already resets the PATH for xcodebuild; check that a custom `heavySlotCommand` does not put it back.
- **A group was "skipped" when you expected an add:** an internal all-builds group is skipped on purpose; an external group needs `"submitExternal": true`. `--dry-run` shows the reason.
- **Profile file permissions (exit 4):** run `chmod 600 ~/.config/hivemill/asc/<profile>.env`.
- **Exit 9:** the build is uploaded. Run again with `--resume-build N`.

## Tests

The tests need no Apple access: a fake key, a fake clock, a mock App Store Connect server on localhost, and `xcodebuild` and `security` stubs on the PATH. On Linux, from the repo root:

```
python3 -m unittest discover -s tests -p test_hivemill_plugin.py -v
```

It runs the Node suite (`node --test` over `plugins/hivemill/scripts/ios-release/test/*.test.mjs`, Node 20 or later) and the skill and README checks.
