# Boss mail

The owner types in the boss pane, so the tick **never** calls `herdr agent prompt` for an agent with role `boss` (step 3 skips it before any herdr call; the director cycle never targets it). The tick cannot tell whether the owner is mid-sentence (we do not rely on herdr exposing a last-input time), so the boss is never prompted by the tick at all. Instead:

- **Badge.** Step 4 of the tick sets the boss pane's display text to `boss·hq 📬<N>` while it has N unread messages, and back to the plain `boss·hq` at 0.
- **Inbox on each owner message.** `roles/hooks/boss-inbox.py` is a `UserPromptSubmit` hook wired in `roles/settings/boss.json.in`. It runs `factory inbox` (marking the mail read) and injects the output as `additionalContext`, capped at about 8000 characters with a `(truncated; run factory inbox --all)` tail. Worker reports longer than 3 lines arrive as their first line plus `(+N lines: factory inbox --show M####)`; run `factory inbox --show M####` for the full text (daily digests are never collapsed). An empty inbox, a missing or failing `factory`, a timeout or bad stdin print nothing; it always exits 0 and never blocks the owner's message.

Director and leads keep the `📬` nudges. An existing boss needs `factory relaunch boss` to pick up the new settings (the director coordinates that after the merge).


## Needs-you feed (dark)

With config `owner_alert_feed=1` the tick records `owner_attention()` as a deduped feed (table `owner_alert_feed`, one open row per kind + ref, fixed fields only: id, org, kind, ref, since, link; never a subject or body). `factory alerts [--json]` lists the open rows read-only. Default is off. Rows closed more than 30 days ago are deleted by the tick (flag on only); open rows are never deleted.

### Sending to the phone (ntfy)

With `owner_alert_send=1` **and** `owner_alert_feed=1` the tick sends each open, unsent feed row over ntfy. Default `owner_alert_send=0`; with either flag at 0 the tick reads and sends nothing.

- Topic: one line in `~/.config/factory/ntfy-topic`, a regular file owned by the factory user with mode 0600 (a symlink is refused), content matching `[A-Za-z0-9_-]{16,64}`. The topic is a secret: it is never stored in the DB, config, events, messages or logs. A missing or unsafe file means no send and one `owner-alert-send-blocked` event per hour.
- Request: POST to the fixed `https://ntfy.sh/<topic>`, timeout 5 s, no redirects, no Title/Tags/Click header; only a 2xx counts.
- Payload, one line: `id=<id> org=<org> kind=<kind> ref=<ref> since=<since> link=<link>`. Never a subject, title or body.
- Order and cap: oldest first, at most 5 rows per tick; `sent` is set only after a 2xx. A closed row is never sent.
- Failure: the first failure ends the tick's sending, nothing is tried for 300 s, the row stays unsent and is retried. One `owner-alert-send-failed` event per hour, holding only an exception class name or `HTTP <status>`.
- Flap guard: a row is held while the same kind + ref was sent in the last 24 h; if still open after that it is sent.
- Rollback: `factory config owner_alert_send 0`.
