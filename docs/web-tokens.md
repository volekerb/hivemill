# web/tokens.py: worker roots and read limits

Sandboxed workers write their Claude logs under `<workers_root>/<agent>/claude/projects`. A worker controls that
directory, so its files are treated as untrusted paths.

- **Confinement at open time.** Discover checks that each worker file is a regular file whose realpath is inside the
  agent's `claude/projects` dir. The read then opens the file with `O_NOFOLLOW` (no symlink at the last component), checks on the
  open fd that it is a regular file, and re-resolves `/proc/self/fd/<n>` to confirm it is still inside `<agent>/claude/projects`.
  A file or parent dir swapped to an outside symlink after discover is not read and counts as an error. Size and
  inode come from that same fd, so the stat and the read cannot disagree.
- **Bounded reads.** At most `MAX_READ_BYTES` (64 MiB) are read from one file per refresh, for every root. A larger
  unread tail is consumed in chunks over successive refreshes (nothing is held whole in memory). A single line longer
  than the cap is skipped as a whole (up to its next newline), counted once in `errors`, and logged to stderr
  (`web.tokens: ... skipped`). Lines are split in place, so peak memory stays near the cap.

Notes: the fd check needs `/proc` (Linux); without it every worker file is rejected (fails closed).
A worker file with more than one hard link is rejected (a link to a file outside the agent's dir would pass the
realpath check), at discover and again on the open fd.
