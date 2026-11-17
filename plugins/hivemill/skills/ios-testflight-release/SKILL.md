---
name: ios-testflight-release
description: Upload an iOS build to TestFlight through the project's .hivemill/ios-release.json and release.sh. Use when the user asks to ship, upload or release an iOS build to TestFlight, or to dry-run that release.
argument-hint: "[--dry-run]"
allowed-tools: Bash(bash ${CLAUDE_PLUGIN_ROOT}/scripts/ios-release/release.sh *)
---

# iOS TestFlight release

You are a thin guide over one script: `bash ${CLAUDE_PLUGIN_ROOT}/scripts/ios-release/release.sh`. Call it `release.sh` below. Setup for a team is in `${CLAUDE_PLUGIN_ROOT}/scripts/ios-release/README.md`.

## Rules

- Never ask for, accept or repeat an App Store Connect key, key id or issuer id in this chat.
- Never read, print or cat a credential file or a .p8 key.
- Always run --dry-run first and show its result before a real upload.
- Run a real upload only when the user asked for one in this conversation.
- Do not run xcodebuild or call the App Store Connect API yourself. release.sh does them; if it fails, stop and report the exit code.
- Report the final JSON summary line and the exit code; do not paste the full log.
- Ask one question at a time. Plain words, no jargon.

## Steps, in order

1. Run `bash ${CLAUDE_PLUGIN_ROOT}/scripts/ios-release/release.sh --dry-run`. Add `--signing`, `--profile` or `--config` only if the user asked for them.
2. Explain the dry-run result in plain words: the signing mode, the next build number, the length of the What to Test notes, the decision for each tester group (would add or would skip), and the gates that would run.
3. Ask for confirmation of the real upload. If the project config sets `submitExternal` to true, say plainly that an external group would be added and that this submits the build to beta app review. The dry run does not print a group's kind, so check the config's `submitExternal` when a group says it would be added.
4. Only after an explicit yes, run the same command without `--dry-run`. Read only the last stdout line (JSON) and the exit code.
5. Map the exit code to one action, and do not improvise:
   - 0: say the build is uploaded; give the build number and the group results.
   - 2: fix the flags. 3: the config is wrong; show the message. 4: credentials are missing or unsafe; point to the README, never ask for the values.
   - 5: a preflight check failed (dirty tree, gate, notes, version); show the message.
   - 6: App Store Connect refused or did not find the app or group; check the key role and the names in the config.
   - 7: xcodebuild failed. With the signing hint: "sign in to Xcode or use an Admin key with --signing apiKey".
   - 8: processing failed or timed out; check App Store Connect.
   - 9: the build is uploaded but notes or groups failed; ask the user, then run again with `--resume-build N` using the build number from the summary. Repeat the beta app review warning if `submitExternal` is true.
   - 1: internal error; show the message and stop.
