# Boss mail

The owner types in the boss pane, so the tick **never** calls `herdr agent prompt` for an agent with role `boss` (step 3 skips it before any herdr call; the director cycle never targets it). The tick cannot tell whether the owner is mid-sentence (we do not rely on herdr exposing a last-input time), so the boss is never prompted by the tick at all. Instead:

- **Badge.** Step 4 of the tick sets the boss pane's display text to `boss·hq 📬<N>` while it has N unread messages, and back to the plain `boss·hq` at 0.
- **Inbox on each owner message.** `roles/hooks/boss-inbox.py` is a `UserPromptSubmit` hook wired in `roles/settings/boss.json.in`. It runs `factory inbox` (marking the mail read) and injects the output as `additionalContext`, capped at about 8000 characters with a `(truncated; run factory inbox --all)` tail. Worker reports longer than 3 lines arrive as their first line plus `(+N lines: factory inbox --show M####)`; run `factory inbox --show M####` for the full text (daily digests are never collapsed). An empty inbox, a missing or failing `factory`, a timeout or bad stdin print nothing; it always exits 0 and never blocks the owner's message.

Director and leads keep the `📬` nudges. An existing boss needs `factory relaunch boss` to pick up the new settings (the director coordinates that after the merge).

