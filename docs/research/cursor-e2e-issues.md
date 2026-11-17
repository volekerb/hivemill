# Cursor E2E dogfood issues (G-027 / T-0829)

Date: 2026-10-31

| ID | Severity | Symptom | Likely cause | Status |
|---|---|---|---|---|
| C1 | high | `herdr agent prompt NAME --wait --until working "text"` → `unknown option: text` | TEXT is positional and must precede options | documented; use `prompt NAME "text" --wait` |
| C2 | high | Coder cancelled `cd ~/factory; git diff` / apply patch from live home | worker-guard: workers must not read live FACTORY_HOME; source must be copied into the worktree | mitigated: owner copied web slice into worktree |
| C3 | med | `herdr agent get` shows `idle` while pane shows tool calls | Cursor status detection lag / follow-up UI vs Claude | open — watch |
| C4 | med | First kickoff left coder idle until Esc + later prompts | Cursor follow-up composer or trust UI ate first prompt | handled (T-1599): the tick re-submits a kickoff that is still unread after `cursor_kickoff_grace_s`, then relaunches the pane once (docs/runtimes.md, "Kickoff that was never accepted") |
| C5 | low | `agent read` / pane dump sometimes vertical-garbled | ANSI/TUI capture for Cursor | open — cosmetic |
| C6 | high | coder-t-0829 ran `factory done` and exited before commit/PR; herdr agent gone; task still in_progress | Cursor agent treated incomplete kickoff as done / confused by owner-copied files | mitigated: owner respawn |
| C7 | med | test_web_components asserts colspan=5 after Runtime column → colspan=6 HTML | patch added Runtime th but missed grp colspan in test | fixed in worktree (+ live uncommitted) |
| C8 | high | After Cursor turn ends (`done`), `herdr agent prompt` lands in follow-ups drawer; status stays done until Enter steers | Cursor Agent follow-up UI ≠ Claude immediate prompt | handled: Enter is sent after every Cursor prompt (T-0860). A kickoff that still was not accepted is detected by its unread kickoff message (T-1599): event `kickoff-resent` (prompt and Enter again, once), then `kickoff-relaunch` (fresh pane, once), then one escalation to the org director |
| C9 | med | tick logs `session-mismatch` for Cursor qa/reviewer (stored herdr session id ≠ live) | Cursor restart/new session id vs DB spawn_args | open — observe whether it breaks prompt routing |
| C10 | high | lead-g-027-a exited after CI-red / reviewer FAIL before coding attempt 2; task stayed `in_review` | Cursor lead `factory done` or session end; no auto reopen on verdict fail | handled for the respawn half (T-1599): a respawned or relaunched Cursor pane that never accepts its kickoff no longer sits silent; the tick resends it (`kickoff-resent`), relaunches once (`kickoff-relaunch`) and then escalates. A lead that ends its session early is still brought back by the owner or director respawn |
| C11 | high | QA/reviewer spawn into shared worktree overwrites `.cursor/cli.json` with `Write(**)` (from bare Claude Edit/Write deny) — coder cannot edit | materialize_cursor_policy writes into cwd; review_autospawn shares coder worktree | mitigated: rematerialize coder policy; need per-agent policy dir (T-0833) |
| C12 | med | Any shell with `CURSOR_AGENT=1` (incl. owner Auto) appends a Cursor co-author trailer on commit → public-text pre-push refuse | Cursor agent git trailer | mitigated: `env -u CURSOR_AGENT git commit`; unset in harness for factory workers |
| C13 | high | review re-autospawn waits for CI green; main time-bomb (`InsightServer` wall clock vs fixture NOW) kept PR red → no qa/reviewer on attempt 2 | test_web_insights pin used `datetime.now()` | mitigated: owner pinned to NOW; committed on branch |
| C14 | high | Judge merge gate refused: red `origin/main` (test_settings_templates … SETTINGS-LOOSENED) unrelated to T-0829 | main CI broken independently | mitigated: reran main workflow → green; judge merged #236 @ad4657eb |


## Dogfood status (2026-10-31)

- Cursor coder → qa → reviewer → judge loop ran on T-0829 / PR #236.
- Attempt 1: reviewer+qa FAIL (missing tests). Attempt 2: both PASS @ad4657ebd; CI green on PR.
- Judge first rejected scope (InsightServer pin); owner widened acceptance 6.
- Judge then blocked on red main (`test_settings_templates`). Merge pending main green.
- Highest Cursor-specific fixes owed: C8 follow-up Enter, C11 shared-worktree policy clobber, C12 `CURSOR_AGENT` trailer.

**Result:** PR https://github.com/volekerb/hivemill/pull/236 MERGED; T-0829 done. Full Cursor coder→qa→reviewer→judge loop verified.
