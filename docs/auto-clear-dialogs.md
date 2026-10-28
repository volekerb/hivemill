# The tick clears blocking dialogs (T-0268)

Owner, 2026-10-26: "i dont want tk press these buttons on blocked agents, solve kt for me, continue or fix do better i dont want to click".

## What the tick does
A key goes to a pane ONLY when, on the same tick, the pane host reports `agent_status == blocked` AND the dialog text sits in the visible bottom region of the pane: the last 30 non-blank lines (`bottom_region`, then `classify_dialog` in `bin/factory`), never older scrollback. Both conditions are re-checked (fresh `agent list` and `agent read`) right before each key. Text only in scrollback, or an agent that is idle, working or done, gets no keys. Only the dialog at the very bottom is classified (`_dialog_block`): exactly one dialog kind with exactly one marker, and after its tail marker only its own lead-in, sequentially numbered option lines (wrapped continuations allowed) and the dialog footer, up to the last line. Quoted marker text (a grep/cat/diff of these docs, a permission command that contains the warning), two dialog kinds at once, or a dialog that is not at the bottom is `unknown`: no keys, no alert. The boss pane never gets a key while the bottom region holds anything a human may be typing (`has_unsent_input`): any `> text` / `❯ text` line other than the classified dialog's own option lines (so a draft `> 2. merge it` counts), or an empty prompt line followed by more text; this is why L-019 exists, M1063). The startup Settings Warning is reported as blocked (director M4499, `launch_pending` true), so no wider rule is needed. Whitespace is ignored when matching, so a narrow pane that wraps lines, even inside a word, still matches.

Bound on a persisting warning: at most one Esc per pane per 5 min (`SETTINGS_ESC_COOLDOWN_S`) and 3 a day (`SETTINGS_ESC_MAX_PER_DAY`); after that the tick sends no more keys and alerts the director once (`Settings Warning stuck on <agent>`, event `settings-warning-stuck`).

| dialog | match | action |
|---|---|---|
| Settings Warning (startup) | both `Settings Warning` and `The values listed above were skipped; the rest of the file is in effect` | one Esc on the next tick, any agent incl. HQ. Event `settings-warning-dismissed` with the warning text. One `alert` to the director per distinct (settings file, warning) per day. |
| Permission dialog, worker | `Do you want to` + numbered `1. Yes` / `No` options | after `blocked_alert_s` (600 s) blocked: what `factory unblock` does: one Esc, `UNBLOCK` decision message, alert to its lead, event `unblock` (by tick). The tick never waits for the worker to settle: it records a `nudge-pending` event and a later tick sends the inbox nudge once herdr reports the worker idle or done (dropped after 10 min or when the worker is gone). |
| Permission dialog, HQ (boss, director, leads) | same | after `hq_unblock_s` (900 s): one Esc, an `UNBLOCK (auto)` decision message to that agent, an alert to the director (to the boss for the director). No text is typed into the pane. |
| anything else | - | no keys. The existing `blocked >10 min` alert only. |

The only key ever sent is Esc (deny once). Never a digit, Enter, or any option that changes settings (L-021). After an Esc on a permission dialog the blocked timer restarts, so a dialog that survives gets one more Esc only after another full timeout.

## L-019 exception
L-019 says never type into a pane a human may be using (HQ panes). T-0268 gives it a narrow, owner-approved exception (owner quote above): only the tick, only cases 1 (Settings Warning) and 3 (HQ permission dialog after 15 min), only Esc. Agents themselves, `factory unblock` included, still never type into HQ panes.

The lesson text in the DB needs the same note; a lead or the director runs, with the owner's quote kept:
`factory lesson edit L-019 --rule "Never send keystrokes into a pane a human may be using (boss/HQ panes); signal HQ through hooks and badges. Exception (T-0268, owner-approved): the tick alone sends one Esc to an HQ pane for a Settings Warning dialog, or a permission dialog blocked over hq_unblock_s."`

## Tests
`tests/test_auto_clear.py` (fixture pane texts via the herdr stub's `read_text`, keys captured from the stub's call log).
